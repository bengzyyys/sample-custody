package custody

import (
	"errors"
	"testing"
)

// 交接集合里两条已确认交接常用的最小合法对象文本。确认人、接收时间等
// 接收事实齐全，只是编号/人员/数量不同，便于注入“同一编号两条记录”。
const rawTR001Confirmed = `"id":"TR-001","sampleId":"S1","fromHolder":"张三","fromLocation":"A","toHolder":"李四","toLocation":"B","qty":10000,"handedOverAt":"2026-10-02T10:00:00Z","confirmed":true,"receivedAt":"2026-10-02T11:00:00Z","confirmedBy":"李四"`

const rawTR002Confirmed = `"id":"TR-002","sampleId":"S1","fromHolder":"李四","fromLocation":"B","toHolder":"王五","toLocation":"C","qty":6750,"handedOverAt":"2026-10-03T10:00:00Z","confirmed":true,"receivedAt":"2026-10-03T11:00:00Z","confirmedBy":"王五"`

// 一份可正常打开的样品：初始 10.000、当前由王五在 C 持有剩余 6.750，
// 保管历史说明里提到 TR-001 仅是文字。
const rawSampleS1TwoHandovers = `"S1": {"id":"S1","initial":10000,"remaining":6750,"holder":"王五","location":"C","children":[],"history":[
	  {"kind":"register","time":"2026-10-02T09:00:00Z","holder":"张三","location":"A"},
	  {"kind":"transfer-in","time":"2026-10-02T11:00:00Z","holder":"李四","location":"B","detail":"交接 TR-001 确认接收"},
	  {"kind":"transfer-in","time":"2026-10-03T11:00:00Z","holder":"王五","location":"C","detail":"交接 TR-002 确认接收，上一条是 TR-001"}
	]}`

// escapedTR001 是写入文件后形如 "<bslash>u0054R-001" 的 JSON 键字面文本：
// 双引号 Go 字符串里的 \\u0054 落盘为反斜杠+u0054，JSON 解码后成为 T，
// 因而与直接写出的 "TR-001" 表示同一编号。
const escapedTR001 = `"` + "\\u0054" + `R-001"`

// escapedTR001InText 是说明文字中的同类转义，仅用于确认值文本不参与判重。
const escapedTR001InText = "\\u0054" + `R-001`

// TestOpenRejectsAdjacentDuplicateTransferID 覆盖任务描述的核心损坏：
// 交接集合里同一编号 TR-001 相邻出现两次、内容不同。JSON 解码进 map 时
// 后一条会静默覆盖前一条，必须在读取时就判为重复，使整份文件打开失败
// （nil Store、ErrInvalid），并写明重复编号与“同一编号出现多次”的原因。
func TestOpenRejectsAdjacentDuplicateTransferID(t *testing.T) {
	second := `"id":"TR-001","sampleId":"S1","fromHolder":"张三","fromLocation":"A","toHolder":"赵六","toLocation":"D","qty":10000,"handedOverAt":"2026-10-04T10:00:00Z","confirmed":false`
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {`+rawSampleS1TwoHandovers+`},
  "transfers": {
    "TR-001": {`+rawTR001Confirmed+`},
    "TR-001": {`+second+`}
  }
}`)
	mustRejectOpen(t, path, "TR-001", "出现多次")
}

// TestOpenRejectsDuplicateTransferIDSeparatedByOthers 重复的两条记录隔着
// 其他交接时同样必须拒绝，不能只比较相邻键。
func TestOpenRejectsDuplicateTransferIDSeparatedByOthers(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {`+rawSampleS1TwoHandovers+`},
  "transfers": {
    "TR-001": {`+rawTR001Confirmed+`},
    "TR-002": {`+rawTR002Confirmed+`},
    "TR-001": {`+rawTR001Confirmed+`}
  }
}`)
	mustRejectOpen(t, path, "TR-001", "出现多次")
}

// TestOpenRejectsIdenticalDuplicateTransfer 两条记录内容完全相同也必须
// 拒绝：不能按确认状态、关联样品或内容是否一致挑选其中一条。
func TestOpenRejectsIdenticalDuplicateTransfer(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {`+rawSampleS1TwoHandovers+`},
  "transfers": {
    "TR-001": {`+rawTR001Confirmed+`},
    "TR-001": {`+rawTR001Confirmed+`}
  }
}`)
	mustRejectOpen(t, path, "TR-001", "出现多次")
}

// TestOpenRejectsDuplicateTransferIDViaUnicodeEscape 编号按 JSON 解码后
// 实际表示的文字判断：把 TR-001 的首字符 T 写成合法 Unicode 转义
// （U+0054）仍是同一编号，必须判为重复；错误信息写出解码后的实际文字
// TR-001。反引号字符串里的 T 原样写入文件，由 JSON 解码解释。
func TestOpenRejectsDuplicateTransferIDViaUnicodeEscape(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {`+rawSampleS1TwoHandovers+`},
  "transfers": {
    "TR-001": {`+rawTR001Confirmed+`},
    `+escapedTR001+`: {`+rawTR001Confirmed+`}
  }
}`)
	mustRejectOpen(t, path, "TR-001", "出现多次")
}

// TestOpenRejectsDuplicateWhenSurvivingRecordWouldValidate 覆盖“静默覆盖”
// 最危险的形态：同一编号先写 null 再写一条能通过全部核对的记录。解码进
// map 后只留下那条合法记录，旧实现会照常打开；唯一性检查必须在解码阶段
// 拦住它。
func TestOpenRejectsDuplicateWhenSurvivingRecordWouldValidate(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {`+rawSampleS1TwoHandovers+`},
  "transfers": {
    "TR-001": null,
    "TR-001": {`+rawTR001Confirmed+`},
    "TR-002": {`+rawTR002Confirmed+`}
  }
}`)
	mustRejectOpen(t, path, "TR-001", "出现多次")
}

// TestOpenDuplicateTransferFailsWholeFile 文件里另有完全正常的样品和交接
// 时，也不能只加载正常部分：整份打开失败、返回 nil Store、原文件保持
// 原样（mustRejectOpen 已断言文件字节不变）。
func TestOpenDuplicateTransferFailsWholeFile(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {
    "OK": {"id":"OK","initial":100,"remaining":100,"holder":"h","location":"l","children":[],"history":[]},
    "S1": {"id":"S1","initial":10000,"remaining":6750,"holder":"王五","location":"C","children":[],"history":[]}
  },
  "transfers": {
    "TR-001": {`+rawTR001Confirmed+`},
    "TR-001": {`+rawTR001Confirmed+`}
  }
}`)
	mustRejectOpen(t, path, "TR-001", "出现多次")
}

// TestOpenDoesNotMentionTransferIDInTextAsDuplicate 编号文字只出现在保管
// 历史说明或交接记录字段值（含 Unicode 转义写法、含未知字段）中，而键
// 各自不同，不能误报为重复。同一样品的两次转手按现有规则正常恢复。
func TestOpenDoesNotMentionTransferIDInTextAsDuplicate(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {`+rawSampleS1TwoHandovers+`},
  "transfers": {
    "TR-001": {`+rawTR001Confirmed+`},
    "TR-002": {`+rawTR002Confirmed+`,"note":"参见 TR-001（转义写法 `+escapedTR001InText+`），仅为说明文字"}
  }
}`)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("编号仅在说明文字中出现不应判为重复，文件应正常打开: %v", err)
	}
	for _, id := range []string{"TR-001", "TR-002"} {
		tr, err := s.GetTransfer(id)
		if err != nil {
			t.Fatalf("交接 %s 应按现有规则恢复: %v", id, err)
		}
		if !tr.Confirmed {
			t.Fatalf("交接 %s 应为已确认记录: %+v", id, tr)
		}
	}
	p, err := s.GetSample("S1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Remaining != "6.750" || p.Holder != "王五" || p.Location != "C" {
		t.Fatalf("样品多次转手后的当前状态应正常恢复: %+v", p)
	}
}

// TestHandoverResubmitStillIdempotentWithReopen 调用交接功能重复提交同一
// 请求，仍按原规则幂等返回原记录；落盘文件只有一条该编号的交接，重新
// 打开也不会被新的唯一性检查误伤。
func TestHandoverResubmitStillIdempotentWithReopen(t *testing.T) {
	s, hAt1, rAt1, _, _ := setupDivergedHandover(t)

	again, err := s.Handover(HandoverInput{
		TransferID: "TR-001", SampleID: "S-001",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B", HandedOverAt: hAt1,
	})
	if err != nil {
		t.Fatalf("重复提交同一交接请求应返回原记录: %v", err)
	}
	requireOldFirstTransfer(t, again, hAt1, rAt1)
	if len(s.data.Transfers) != 2 {
		t.Fatalf("幂等提交不得新增交接, got %d 条", len(s.data.Transfers))
	}

	s2, err := Open(s.path)
	if err != nil {
		t.Fatalf("正常保存的文件重新打开不应受影响: %v", err)
	}
	old, err := s2.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	requireOldFirstTransfer(t, old, hAt1, rAt1)
}

// TestOpenDuplicateTransferErrorIsInvalid 单独固定错误分类：重复编号错误
// 必须能被 errors.Is 判定为 ErrInvalid（而非解码错误或冲突类）。
func TestOpenDuplicateTransferErrorIsInvalid(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {`+rawSampleS1TwoHandovers+`},
  "transfers": {
    "TR-001": {`+rawTR001Confirmed+`},
    "TR-001": {`+rawTR001Confirmed+`}
  }
}`)
	s, err := Open(path)
	if s != nil {
		t.Fatalf("打开失败必须返回 nil Store")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("重复交接编号应判定为 ErrInvalid, got %v", err)
	}
	if errors.Is(err, ErrConflict) {
		t.Fatalf("重复交接编号不应归类为 ErrConflict: %v", err)
	}
}
