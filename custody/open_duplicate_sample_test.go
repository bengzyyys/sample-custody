package custody

import (
	"errors"
	"testing"
)

// 任务示例中的两条 S-001：第一份初始量/剩余量 10.000 毫升、张三在实验室A
// 保管；第二份同编号、两项数量均为 2.000 毫升、李四在实验室B 保管。两份
// 记录各自都能通过数量核对，区别仅在编号相同。
const rawSampleS001ZhangSan = `"S-001": {"id":"S-001","initial":10000,"remaining":10000,"holder":"张三","location":"实验室A","children":[],"history":[{"kind":"register","time":"2026-10-02T09:00:00Z","holder":"张三","location":"实验室A","detail":"登记原样"}]}`

const rawSampleS001LiSi = `"S-001": {"id":"S-001","initial":2000,"remaining":2000,"holder":"李四","location":"实验室B","children":[],"history":[{"kind":"register","time":"2026-10-03T09:00:00Z","holder":"李四","location":"实验室B","detail":"登记原样"}]}`

// 另外两份不同编号的最简合法样品，用于构造“重复条目隔着其他样品”以及
// “跨字段合并”场景。
const rawSampleS002 = `"S-002": {"id":"S-002","initial":5000,"remaining":5000,"holder":"王五","location":"实验室C","children":[],"history":[{"kind":"register","time":"2026-10-02T09:00:00Z","holder":"王五","location":"实验室C"}]}`

const rawSampleS003 = `"S-003": {"id":"S-003","initial":3000,"remaining":3000,"holder":"赵六","location":"实验室D","children":[],"history":[{"kind":"register","time":"2026-10-02T09:00:00Z","holder":"赵六","location":"实验室D"}]}`

// escapedS001 落盘后是 JSON 键 "S-001"，解码后与直接写出的 "S-001"
// 表示同一编号。
const escapedS001 = `"` + "\\u0053" + `-001"`

// TestOpenRejectsAdjacentDuplicateSampleID 覆盖任务描述的核心损坏：样品
// 集合里同一编号 S-001 相邻出现两次、数量与保管信息不同。JSON 解码进 map
// 时后一条会静默覆盖前一条，必须在读取时就判为重复，整份文件打开失败
// （nil Store、ErrInvalid、原文件不变），错误写明重复编号与“出现多次”。
func TestOpenRejectsAdjacentDuplicateSampleID(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    `+rawSampleS001ZhangSan+`,
    `+rawSampleS001LiSi+`
  },
  "transfers": {}
}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenRejectsDuplicateSampleIDSeparatedByOthers 重复的两条记录隔着其他
// 样品时同样必须拒绝，不能只比较相邻键。
func TestOpenRejectsDuplicateSampleIDSeparatedByOthers(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    `+rawSampleS001ZhangSan+`,
    `+rawSampleS002+`,
    `+rawSampleS003+`,
    `+rawSampleS001LiSi+`
  },
  "transfers": {}
}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenRejectsIdenticalDuplicateSample 两条记录内容完全相同也必须拒绝：
// 不能按数量、人员或地点是否一致挑选其中一条。
func TestOpenRejectsIdenticalDuplicateSample(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    `+rawSampleS001ZhangSan+`,
    `+rawSampleS001ZhangSan+`
  },
  "transfers": {}
}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenRejectsDuplicateSampleIDViaUnicodeEscape 编号按 JSON 解码后实际
// 表示的文字判断：首字符 S 写成合法 Unicode 转义（U+0053）仍是同一编号。
func TestOpenRejectsDuplicateSampleIDViaUnicodeEscape(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    `+rawSampleS001ZhangSan+`,
    `+escapedS001+`: {"id":"S-001","initial":2000,"remaining":2000,"holder":"李四","location":"实验室B","children":[],"history":[]}
  },
  "transfers": {}
}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenRejectsDuplicateSampleWhenSurvivingRecordWouldValidate 同一编号先
// 写 null 再写一条能通过全部核对的记录时，解码进 map 只留下合法记录，旧
// 实现会照常打开并让另一份被覆盖；唯一性检查必须在解码阶段拦住它，不能
// 因为其中一份为 null 就放行。
func TestOpenRejectsDuplicateSampleWhenSurvivingRecordWouldValidate(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    "S-001": null,
    `+rawSampleS002+`,
    `+rawSampleS001LiSi+`
  },
  "transfers": {}
}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenDuplicateSampleFailsWholeFile 即使文件里另有完全正常的样品，也不
// 能只加载正常部分：整份打开失败、nil Store、原文件字节不变。
func TestOpenDuplicateSampleFailsWholeFile(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    `+rawSampleS002+`,
    `+rawSampleS001ZhangSan+`,
    `+rawSampleS001LiSi+`
  },
  "transfers": {}
}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenRejectsDuplicateAcrossTwoSamplesFields 文件顶层出现两个同名
// samples 字段，S-001 分别在两个集合里各保存一次时，唯一性要求覆盖这些
// 集合里的全部样品：标准库只保留最后一个字段的整张表会静默抹去前一份数量
// 与保管信息，必须拒绝整份文件。
func TestOpenRejectsDuplicateAcrossTwoSamplesFields(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    `+rawSampleS001ZhangSan+`
  },
  "samples": {
    `+rawSampleS001LiSi+`
  },
  "transfers": {}
}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenRejectsDuplicateAcrossSampleFieldsSeparatedByTopLevelFields 两个
// 同名字段之间隔着其他顶层字段、第二个字段内还隔着其他编号样品时，重复
// 编号同样不能漏过。
func TestOpenRejectsDuplicateAcrossSampleFieldsSeparatedByTopLevelFields(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    `+rawSampleS001ZhangSan+`
  },
  "transfers": {},
  "note": {"text": "两个样品集合之间隔着其他顶层字段"},
  "samples": {
    `+rawSampleS002+`,
    `+rawSampleS001LiSi+`
  }
}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenRejectsIdenticalDuplicateSampleAcrossTwoFields 两个字段里的
// S-001 内容完全相同也必须拒绝。
func TestOpenRejectsIdenticalDuplicateSampleAcrossTwoFields(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    `+rawSampleS001ZhangSan+`
  },
  "samples": {
    `+rawSampleS001ZhangSan+`
  },
  "transfers": {}
}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenRejectsDuplicateSampleAcrossFieldsViaUnicodeEscape 分处两个字段
// 的编号经 JSON 解码后是同一文字（其中一个首字符写成 U+0053 转义）时仍
// 算重复。
func TestOpenRejectsDuplicateSampleAcrossFieldsViaUnicodeEscape(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    `+escapedS001+`: {"id":"S-001","initial":10000,"remaining":10000,"holder":"张三","location":"实验室A","children":[],"history":[]}
  },
  "samples": {
    `+rawSampleS001LiSi+`
  },
  "transfers": {}
}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenDuplicateSampleAcrossFieldsFailsWholeFile 跨字段重复时，即使其他
// 编号的样品完全正常，也不能只载入最后一个集合或正常部分：整份打开失败、
// 原文件保持原样。
func TestOpenDuplicateSampleAcrossFieldsFailsWholeFile(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    `+rawSampleS002+`,
    `+rawSampleS001ZhangSan+`
  },
  "samples": {
    `+rawSampleS003+`,
    `+rawSampleS001LiSi+`
  },
  "transfers": {}
}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenMergesDistinctIDsAcrossDuplicateSamplesFields 不同编号分别出现在
// 不同 samples 集合中时，应继续保留全部记录，不能只留下最后一个集合：
// 各条记录跨字段合并恢复，仍能按编号查询各自的数量与保管信息。
func TestOpenMergesDistinctIDsAcrossDuplicateSamplesFields(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    `+rawSampleS001ZhangSan+`
  },
  "note": "中间字段",
  "samples": {
    `+rawSampleS002+`
  },
  "transfers": {}
}`)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("字段重名但编号互不相同应正常打开: %v", err)
	}
	if len(s.data.Samples) != 2 {
		t.Fatalf("两个集合中的不同编号都应保留, got %d 条", len(s.data.Samples))
	}
	first, err := s.GetSample("S-001")
	if err != nil {
		t.Fatalf("前一个集合的 S-001 应跨字段合并恢复: %v", err)
	}
	if first.InitialQty != "10.000" || first.Remaining != "10.000" ||
		first.Holder != "张三" || first.Location != "实验室A" {
		t.Fatalf("S-001 必须保留第一份数量与保管信息，不能被后一个集合覆盖: %+v", first)
	}
	other, err := s.GetSample("S-002")
	if err != nil {
		t.Fatalf("后一个集合的 S-002 也应可查询: %v", err)
	}
	if other.Holder != "王五" || other.Location != "实验室C" {
		t.Fatalf("S-002 的保管信息应原样恢复: %+v", other)
	}
}

// TestOpenMergesDistinctSampleIDsWithNullFieldBetween 后一个 samples 字段为
// null 不应清空前一个字段已经保存的样品。
func TestOpenMergesDistinctSampleIDsWithNullFieldBetween(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    `+rawSampleS001ZhangSan+`,
    `+rawSampleS002+`
  },
  "note": "中间字段",
  "samples": null,
  "transfers": {}
}`)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("后一个 samples 为 null 不应影响前一个字段的记录: %v", err)
	}
	if len(s.data.Samples) != 2 {
		t.Fatalf("前一个字段的两份样品都应保留, got %d 条", len(s.data.Samples))
	}
}

// TestOpenSampleIDReferencesAreNotDuplicateKeys 编号出现在子样来源、子样
// 列表、交接关联或保管历史说明里只是正常引用，不是额外的样品条目；父样与
// 子样人员、地点相同、数量也恰好相同数值（各为 5.000 毫升），仍属于两个
// 独立编号，必须正常打开。
func TestOpenSampleIDReferencesAreNotDuplicateKeys(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    "S-001": {"id":"S-001","initial":10000,"remaining":5000,"holder":"张三","location":"实验室A","children":["S-001-A"],"history":[
      {"kind":"register","time":"2026-10-02T09:00:00Z","holder":"张三","location":"实验室A","detail":"登记原样 S-001"},
      {"kind":"split","time":"2026-10-02T10:00:00Z","holder":"张三","location":"实验室A","detail":"分装创建子样 S-001-A"},
      {"kind":"transfer-in","time":"2026-10-02T12:00:00Z","holder":"张三","location":"实验室A","detail":"交接 TR-001 确认接收，关联样品 S-001"}
    ]},
    "S-001-A": {"id":"S-001-A","parentId":"S-001","initial":5000,"remaining":5000,"holder":"张三","location":"实验室A","children":[],"history":[
      {"kind":"split","time":"2026-10-02T10:00:00Z","holder":"张三","location":"实验室A","detail":"由样品 S-001 分装"}
    ]}
  },
  "transfers": {
    "TR-001": {"id":"TR-001","sampleId":"S-001","fromHolder":"李四","fromLocation":"实验室B","toHolder":"张三","toLocation":"实验室A","qty":5000,"handedOverAt":"2026-10-02T11:00:00Z","confirmed":true,"receivedAt":"2026-10-02T12:00:00Z","confirmedBy":"张三"}
  }
}`)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("编号仅作为引用出现时不应判重，文件应正常打开: %v", err)
	}
	if len(s.data.Samples) != 2 {
		t.Fatalf("父样和子样各有独立编号，都应保留, got %d 条", len(s.data.Samples))
	}
	parent, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.GetSample("S-001-A")
	if err != nil {
		t.Fatal(err)
	}
	if parent.ParentID != "" || child.ParentID != "S-001" {
		t.Fatalf("父子来源关系应原样恢复: parent=%+v child=%+v", parent, child)
	}
	tr, err := s.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	if tr.SampleID != "S-001" || !tr.Confirmed {
		t.Fatalf("关联 S-001 的交接应正常恢复: %+v", tr)
	}
}

// TestOpenDuplicateSampleErrorIsInvalid 固定错误分类：重复样品编号错误必须
// 能被 errors.Is 判定为 ErrInvalid，而不是解码错误或冲突类。
func TestOpenDuplicateSampleErrorIsInvalid(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    `+rawSampleS001ZhangSan+`,
    `+rawSampleS001LiSi+`
  },
  "transfers": {}
}`)
	s, err := Open(path)
	if s != nil {
		t.Fatalf("打开失败必须返回 nil Store")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("重复样品编号应判定为 ErrInvalid, got %v", err)
	}
	if errors.Is(err, ErrConflict) {
		t.Fatalf("重复样品编号不应归类为 ErrConflict: %v", err)
	}
}
