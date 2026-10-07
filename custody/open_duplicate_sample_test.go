package custody

import (
	"errors"
	"testing"
)

// 样品集合里两条记录常用的最小合法对象文本。数量各自守恒（未销毁、无子样、
// 剩余量等于初始量），只是持有人/地点/数量不同，便于注入“同一编号两条记录”。
const rawSampleS001Zhang = `"id":"S-001","initial":10000,"remaining":10000,"holder":"张三","location":"实验室A","children":[],"history":[
	  {"kind":"register","time":"2026-10-02T09:00:00Z","holder":"张三","location":"实验室A"}
	]`

const rawSampleS001Li = `"id":"S-001","initial":2000,"remaining":2000,"holder":"李四","location":"实验室B","children":[],"history":[
	  {"kind":"register","time":"2026-10-03T09:00:00Z","holder":"李四","location":"实验室B"}
	]`

// 另一份正常样品，用于隔着其他条目或验证整份文件失败时不只加载正常部分。
const rawSampleOK = `"OK": {"id":"OK","initial":100,"remaining":100,"holder":"h","location":"l","children":[],"history":[]}`

// escapedS001 是写入文件后形如 "<bslash>u0053-001" 的 JSON 键字面文本：
// 双引号 Go 字符串里的 \\u0053 落盘为反斜杠+u0053，JSON 解码后成为 S，
// 因而与直接写出的 "S-001" 表示同一编号。
const escapedS001 = `"` + "\\u0053" + `-001"`

// escapedS001InText 是说明文字中的同类转义，仅用于确认值文本不参与判重。
const escapedS001InText = "\\u0053" + `-001`

// TestOpenRejectsAdjacentDuplicateSampleID 覆盖任务描述的核心损坏：样品集合
// 里同一编号 S-001 相邻出现两次、内容不同（先张三 10.000 毫升，后李四
// 2.000 毫升，两份都能通过数量核对）。JSON 解码进 map 时后一条会静默覆盖
// 前一条，必须在读取时就判为重复，使整份文件打开失败（nil Store、
// ErrInvalid），并写明重复编号与“同一编号出现多次”的原因。
func TestOpenRejectsAdjacentDuplicateSampleID(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    "S-001": {`+rawSampleS001Zhang+`},
	    "S-001": {`+rawSampleS001Li+`}
	  },
	  "transfers": {}
	}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenRejectsDuplicateSampleIDSeparatedByOthers 重复的两条样品记录隔着
// 其他样品时同样必须拒绝，不能只比较相邻键。
func TestOpenRejectsDuplicateSampleIDSeparatedByOthers(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    "S-001": {`+rawSampleS001Zhang+`},
	    `+rawSampleOK+`,
	    "S-001": {`+rawSampleS001Li+`}
	  },
	  "transfers": {}
	}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenRejectsIdenticalDuplicateSample 两条样品记录内容完全相同也必须
// 拒绝：不能按内容是否一致挑选其中一份。
func TestOpenRejectsIdenticalDuplicateSample(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    "S-001": {`+rawSampleS001Zhang+`},
	    "S-001": {`+rawSampleS001Zhang+`}
	  },
	  "transfers": {}
	}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenRejectsDuplicateSampleIDViaUnicodeEscape 编号按 JSON 解码后实际
// 表示的文字判断：把 S-001 的首字符 S 写成合法 Unicode 转义（U+0053）仍是
// 同一编号，必须判为重复；错误信息写出解码后的实际文字 S-001。
func TestOpenRejectsDuplicateSampleIDViaUnicodeEscape(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    "S-001": {`+rawSampleS001Zhang+`},
	    `+escapedS001+`: {`+rawSampleS001Li+`}
	  },
	  "transfers": {}
	}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenRejectsDuplicateSampleWhenOneIsNull 同一编号先写 null 再写一条能
// 通过全部核对的记录时，不能因后一条覆盖 null 而放行：唯一性检查在解码
// 阶段就拦住它。
func TestOpenRejectsDuplicateSampleWhenOneIsNull(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    "S-001": null,
	    "S-001": {`+rawSampleS001Zhang+`},
	    `+rawSampleOK+`
	  },
	  "transfers": {}
	}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenDuplicateSampleFailsWholeFile 文件里另有完全正常的样品时，也不能
// 只加载正常部分：整份打开失败、返回 nil Store、原文件保持原样
// （mustRejectOpen 已断言文件字节不变）。
func TestOpenDuplicateSampleFailsWholeFile(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    `+rawSampleOK+`,
	    "S-001": {`+rawSampleS001Zhang+`},
	    "S-001": {`+rawSampleS001Li+`}
	  },
	  "transfers": {}
	}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenRejectsDuplicateAcrossTwoSamplesFields 文件顶层有两个同名 samples
// 字段，S-001 分别在两个字段里各保存一次。标准库解码重复字段时只保留最后
// 一个字段的整张表，唯一性判断必须覆盖整份文件里的全部样品集合。
func TestOpenRejectsDuplicateAcrossTwoSamplesFields(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    "S-001": {`+rawSampleS001Zhang+`}
	  },
	  "samples": {
	    "S-001": {`+rawSampleS001Li+`}
	  },
	  "transfers": {}
	}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenRejectsDuplicateSampleAcrossFieldsSeparatedByTopLevelFields 两个同名
// samples 字段之间隔着其他顶层字段、第二个字段内还隔着其他编号的样品，
// 重复编号同样不能漏过。
func TestOpenRejectsDuplicateSampleAcrossFieldsSeparatedByTopLevelFields(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    "S-001": {`+rawSampleS001Zhang+`}
	  },
	  "transfers": {},
	  "note": {"text": "两个样品集合之间隔着其他顶层字段"},
	  "samples": {
	    `+rawSampleOK+`,
	    "S-001": {`+rawSampleS001Li+`}
	  }
	}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenRejectsDuplicateSampleAcrossFieldsViaUnicodeEscape 分处两个字段的
// 编号经 JSON 解码后是同一文字（其中一个首字符写成 U+0053 转义）时仍算重复。
func TestOpenRejectsDuplicateSampleAcrossFieldsViaUnicodeEscape(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    `+escapedS001+`: {`+rawSampleS001Zhang+`}
	  },
	  "samples": {
	    "S-001": {`+rawSampleS001Li+`}
	  },
	  "transfers": {}
	}`)
	mustRejectOpen(t, path, "S-001", "出现多次")
}

// TestOpenMergesDistinctSampleIDsAcrossDuplicateSamplesFields 多个同名 samples
// 字段分别只含不同编号时，不应仅因字段重名就报编号冲突：各条记录合并恢复，
// 仍能按编号查询到各自的数量与保管信息。
func TestOpenMergesDistinctSampleIDsAcrossDuplicateSamplesFields(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    "S-001": {`+rawSampleS001Zhang+`}
	  },
	  "samples": {
	    `+rawSampleOK+`
	  },
	  "transfers": {}
	}`)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("字段重名但编号互不相同应正常打开: %v", err)
	}
	if len(s.data.Samples) != 2 {
		t.Fatalf("两个字段中的不同编号都应保留, got %d 份", len(s.data.Samples))
	}
	p, err := s.GetSample("S-001")
	if err != nil {
		t.Fatalf("样品 S-001 应跨字段合并恢复并可查询: %v", err)
	}
	if p.InitialQty != "10.000" || p.Remaining != "10.000" || p.Holder != "张三" || p.Location != "实验室A" {
		t.Fatalf("S-001 的数量与保管信息应完整保留: %+v", p)
	}
	ok, err := s.GetSample("OK")
	if err != nil {
		t.Fatalf("样品 OK 应跨字段合并恢复并可查询: %v", err)
	}
	if ok.Remaining != "0.100" {
		t.Fatalf("OK 的数量应完整保留: %+v", ok)
	}
}

// TestOpenMergesDistinctSampleIDsAcrossFieldsWithNullBetween 后一个 samples
// 字段为 null 不应清空前一个字段已经保存的样品；中间插入的其他顶层字段
// 也不影响合并恢复。
func TestOpenMergesDistinctSampleIDsAcrossFieldsWithNullBetween(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    "S-001": {`+rawSampleS001Zhang+`},
	    `+rawSampleOK+`
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
		t.Fatalf("前一个字段的两份样品都应保留, got %d 份", len(s.data.Samples))
	}
}

// TestOpenDoesNotTreatSampleIDReferencesAsDuplicate 编号文字只出现在子样来源、
// 子样列表、交接关联或保管历史说明（含 Unicode 转义写法）中，而样品集合的键
// 各自不同，不能误报为重复。父样与子样各有独立编号，正常恢复。
func TestOpenDoesNotTreatSampleIDReferencesAsDuplicate(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    "S-001": {"id":"S-001","initial":10000,"remaining":6000,"holder":"张三","location":"实验室A","children":["S-001-A"],"history":[
	      {"kind":"register","time":"2026-10-02T09:00:00Z","holder":"张三","location":"实验室A"},
	      {"kind":"split","time":"2026-10-02T10:00:00Z","holder":"张三","location":"实验室A","detail":"分装创建子样 S-001-A（转义写法 `+escapedS001InText+`-A），来源是 S-001"}
	    ]},
	    "S-001-A": {"id":"S-001-A","parentId":"S-001","initial":4000,"remaining":4000,"holder":"张三","location":"实验室A","children":[],"history":[
	      {"kind":"split","time":"2026-10-02T10:00:00Z","holder":"张三","location":"实验室A","detail":"由样品 S-001 分装"}
	    ]}
	  },
	  "transfers": {
	    "TR-001": {"id":"TR-001","sampleId":"S-001-A","fromHolder":"张三","fromLocation":"实验室A","toHolder":"李四","toLocation":"实验室B","qty":4000,"handedOverAt":"2026-10-03T10:00:00Z","confirmed":true,"receivedAt":"2026-10-03T11:00:00Z","confirmedBy":"李四"}
	  }
	}`)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("编号仅在来源、子样列表、交接关联或说明文字中出现不应判为重复: %v", err)
	}
	parent, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	if len(parent.Children) != 1 || parent.Children[0] != "S-001-A" {
		t.Fatalf("父子关系应正常恢复: %+v", parent)
	}
	child, err := s.GetSample("S-001-A")
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentID != "S-001" {
		t.Fatalf("子样来源应正常恢复: %+v", child)
	}
}

// TestOpenDistinctSampleIDsWithSameDetailsNotDuplicate 父样和子样（或不同
// 原样）编号不同，即使人员、地点或数量相同也不能被认定为重复。
func TestOpenDistinctSampleIDsWithSameDetailsNotDuplicate(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    "S-001": {"id":"S-001","initial":5000,"remaining":5000,"holder":"张三","location":"实验室A","children":[],"history":[]},
	    "S-002": {"id":"S-002","initial":5000,"remaining":5000,"holder":"张三","location":"实验室A","children":[],"history":[]}
	  },
	  "transfers": {}
	}`)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("编号不同、内容相同的样品不应判为重复: %v", err)
	}
	if len(s.data.Samples) != 2 {
		t.Fatalf("两份样品都应保留, got %d 份", len(s.data.Samples))
	}
}

// TestOpenDuplicateSampleErrorIsInvalid 单独固定错误分类：重复样品编号错误
// 必须能被 errors.Is 判定为 ErrInvalid（而非解码错误或冲突类）。
func TestOpenDuplicateSampleErrorIsInvalid(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    "S-001": {`+rawSampleS001Zhang+`},
	    "S-001": {`+rawSampleS001Li+`}
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
