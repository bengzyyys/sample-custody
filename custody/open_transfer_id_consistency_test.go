package custody

import (
	"strings"
	"testing"
)

// 本文件覆盖“交接在集合中的编号与记录自身保存的编号必须一致”的恢复规则：
// 两处必须是同一个非空、不含首尾空白的编号，否则整份数据打开失败
// （ErrInvalid、nil Store、原文件不变）；编号比较以 JSON 解码后的实际文字
// 为准，说明文字中提到的编号不参与判断。

// TestOpenRejectsPendingTransferKeyedUnderDifferentID 覆盖任务描述的核心
// 损坏：待确认交接只以 TR-002 保存在集合中，记录自身却写着 TR-009，其余
// 内容全部符合现有规则。旧实现按 TR-002 查询会返回写着 TR-009 的记录，
// 再按 TR-009 却查不到该交接；必须整份拒绝，错误同时指出两处编号。
func TestOpenRejectsPendingTransferKeyedUnderDifferentID(t *testing.T) {
	mismatched := strings.Replace(consistentPendingTR, `"id":"TR-002"`, `"id":"TR-009"`, 1)
	path := writeDivergedScenario(t, "", "", mismatched)
	mustRejectOpen(t, path, "TR-002", "TR-009", "不一致")
}

// TestOpenRejectsConfirmedTransferKeyedUnderDifferentID 已确认交接同样适用：
// 集合键 TR-001 的记录自身写着 TR-009，即使接收事实齐全也必须拒绝。
func TestOpenRejectsConfirmedTransferKeyedUnderDifferentID(t *testing.T) {
	mismatched := strings.Replace(rawTR001Confirmed, `"id":"TR-001"`, `"id":"TR-009"`, 1)
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {`+rawSampleS1TwoHandovers+`},
	  "transfers": {"TR-001": {`+mismatched+`}}
	}`)
	mustRejectOpen(t, path, "TR-001", "TR-009", "不一致")
}

// TestOpenRejectsTransferWithMissingOrBlankOwnID 记录自身编号缺失、为空或
// 只有空白时都按无效数据拒绝，不能按集合中的编号补填接受；错误措辞与
// “两个非空编号不一致”可区分。
func TestOpenRejectsTransferWithMissingOrBlankOwnID(t *testing.T) {
	confirmedNoID := strings.Replace(rawTR001Confirmed, `"id":"TR-001",`, ``, 1)
	confirmedEmptyID := strings.Replace(rawTR001Confirmed, `"id":"TR-001"`, `"id":""`, 1)
	confirmedBlankID := strings.Replace(rawTR001Confirmed, `"id":"TR-001"`, `"id":"   "`, 1)
	pendingNoID := strings.Replace(consistentPendingTR, `"id":"TR-002",`, ``, 1)

	t.Run("已确认缺少自身编号", func(t *testing.T) {
		path := writeRawLedger(t, `{
		  "version": 1,
		  "samples": {`+rawSampleS1TwoHandovers+`},
		  "transfers": {"TR-001": {`+confirmedNoID+`}}
		}`)
		mustRejectOpen(t, path, "TR-001", "缺少")
	})
	t.Run("已确认自身编号为空", func(t *testing.T) {
		path := writeRawLedger(t, `{
		  "version": 1,
		  "samples": {`+rawSampleS1TwoHandovers+`},
		  "transfers": {"TR-001": {`+confirmedEmptyID+`}}
		}`)
		mustRejectOpen(t, path, "TR-001", "缺少")
	})
	t.Run("已确认自身编号只有空白", func(t *testing.T) {
		path := writeRawLedger(t, `{
		  "version": 1,
		  "samples": {`+rawSampleS1TwoHandovers+`},
		  "transfers": {"TR-001": {`+confirmedBlankID+`}}
		}`)
		mustRejectOpen(t, path, "TR-001", "空白")
	})
	t.Run("待确认缺少自身编号", func(t *testing.T) {
		path := writeDivergedScenario(t, "", "", pendingNoID)
		mustRejectOpen(t, path, "TR-002", "缺少")
	})
}

// TestOpenRejectsTransferIDWithSurroundingWhitespace 保存的编号含有首尾空白
// 不符合“去首尾空白后保存”的约定：无论空白出现在记录自身编号还是集合键
// 上，甚至两处空白写法完全相同，都不能通过去掉空白来接受。
func TestOpenRejectsTransferIDWithSurroundingWhitespace(t *testing.T) {
	t.Run("记录自身编号含首尾空白", func(t *testing.T) {
		padded := strings.Replace(rawTR001Confirmed, `"id":"TR-001"`, `"id":" TR-001 "`, 1)
		path := writeRawLedger(t, `{
		  "version": 1,
		  "samples": {`+rawSampleS1TwoHandovers+`},
		  "transfers": {"TR-001": {`+padded+`}}
		}`)
		mustRejectOpen(t, path, "TR-001", "首尾空白")
	})
	t.Run("集合键与记录编号含相同首尾空白", func(t *testing.T) {
		padded := strings.Replace(rawTR001Confirmed, `"id":"TR-001"`, `"id":" TR-001 "`, 1)
		path := writeRawLedger(t, `{
		  "version": 1,
		  "samples": {`+rawSampleS1TwoHandovers+`},
		  "transfers": {" TR-001 ": {`+padded+`}}
		}`)
		mustRejectOpen(t, path, "TR-001", "首尾空白")
	})
	t.Run("集合键含尾部空白而记录编号干净", func(t *testing.T) {
		path := writeRawLedger(t, `{
		  "version": 1,
		  "samples": {`+rawSampleS1TwoHandovers+`},
		  "transfers": {"TR-001 ": {`+rawTR001Confirmed+`}}
		}`)
		mustRejectOpen(t, path, "TR-001", "空白")
	})
}

// TestOpenRejectsTransferWithEmptyCollectionKey 集合中的编号为空同样拒绝，
// 不能把记录挪到它自己写着的编号下接受。
func TestOpenRejectsTransferWithEmptyCollectionKey(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {`+rawSampleS1TwoHandovers+`},
	  "transfers": {"": {`+rawTR001Confirmed+`}}
	}`)
	mustRejectOpen(t, path, "TR-001", "为空")
}

// TestOpenTransferIDMismatchFailsWholeFile 文件里另有完全正常的样品与交接
// 时，也不能只加载正常部分：整份打开失败、返回 nil Store、原文件保持
// 原样（mustRejectOpen 已断言文件字节不变）。
func TestOpenTransferIDMismatchFailsWholeFile(t *testing.T) {
	mismatched := strings.Replace(rawTR001Confirmed, `"id":"TR-001"`, `"id":"TR-009"`, 1)
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    "OK": {"id":"OK","initial":100,"remaining":100,"holder":"h","location":"l","children":[],"history":[]},
	    "S1": {"id":"S1","initial":10000,"remaining":6750,"holder":"王五","location":"C","children":[],"history":[]}
	  },
	  "transfers": {
	    "TR-OK": {"id":"TR-OK","sampleId":"OK","fromHolder":"h","fromLocation":"l","toHolder":"x","toLocation":"y","qty":100,"handedOverAt":"2026-10-02T10:00:00Z","confirmed":true,"receivedAt":"2026-10-02T11:00:00Z","confirmedBy":"x"},
	    "TR-001": {`+mismatched+`}
	  }
	}`)
	mustRejectOpen(t, path, "TR-001", "TR-009", "不一致")
}

// TestOpenAcceptsEscapedTransferIDMatchingRecordOwnID 编号比较以 JSON 解码
// 后实际表示的文字为准：集合键或记录自身编号把同一文字写成合法 Unicode
// 转义（T 即 U+0054）时仍是同一编号，不能因此拒绝合法文件。
func TestOpenAcceptsEscapedTransferIDMatchingRecordOwnID(t *testing.T) {
	escapedID := strings.Replace(rawTR001Confirmed, `"id":"TR-001"`, `"id":"`+escapedTR001InText+`"`, 1)
	for name, transfers := range map[string]string{
		"集合键转义":  `{` + escapedTR001 + `: {` + rawTR001Confirmed + `}}`,
		"记录编号转义": `{"TR-001": {` + escapedID + `}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeRawLedger(t, `{
			  "version": 1,
			  "samples": {`+rawSampleS1TwoHandovers+`},
			  "transfers": `+transfers+`
			}`)
			s, err := Open(path)
			if err != nil {
				t.Fatalf("转义写法与直接写出表示同一编号，文件应正常打开: %v", err)
			}
			tr, err := s.GetTransfer("TR-001")
			if err != nil {
				t.Fatalf("应按解码后的编号 TR-001 查询到交接: %v", err)
			}
			if tr.TransferID != "TR-001" || !tr.Confirmed {
				t.Fatalf("恢复后的交接编号应为 TR-001 且保持已确认: %+v", tr)
			}
		})
	}
}

// TestOpenTransferIDConsistencyIgnoresTextMentions 交接关联的样品编号、保管
// 历史说明与记录其他字段值中提到的交接编号都只是关联或说明文字，不参与
// 两处交接编号的一致性判断：说明里提到并不存在的 TR-999 不影响打开。
func TestOpenTransferIDConsistencyIgnoresTextMentions(t *testing.T) {
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {"S1": {"id":"S1","initial":10000,"remaining":6750,"holder":"王五","location":"C","children":[],"history":[
	    {"kind":"register","time":"2026-10-02T09:00:00Z","holder":"张三","location":"A"},
	    {"kind":"transfer-in","time":"2026-10-02T11:00:00Z","holder":"李四","location":"B","detail":"交接 TR-001 确认接收，曾拟用编号 TR-999"}
	  ]}},
	  "transfers": {
	    "TR-001": {`+rawTR001Confirmed+`,"note":"与 TR-999 无关的说明"}
	  }
	}`)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("说明文字中提到的编号不参与一致性判断，文件应正常打开: %v", err)
	}
	tr, err := s.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	if tr.TransferID != "TR-001" || !tr.Confirmed {
		t.Fatalf("交接应按现有规则恢复: %+v", tr)
	}
}

// TestOpenRestoredTransferIDsAlignedAcrossQueries 合法记录恢复后，按交接
// 编号查询、样品待确认详情与相同交出请求返回的交接编号保持一致，待确认
// 交接仍由原指定接收人按原条件确认。
func TestOpenRestoredTransferIDsAlignedAcrossQueries(t *testing.T) {
	s, hAt1, rAt1, hAt2, rAt2 := setupDivergedHandover(t)

	s2, err := Open(s.path)
	if err != nil {
		t.Fatalf("合法文件应正常恢复: %v", err)
	}
	pending, err := s2.GetTransfer("TR-002")
	if err != nil {
		t.Fatal(err)
	}
	if pending.TransferID != "TR-002" || pending.Confirmed {
		t.Fatalf("按编号查到的待确认交接编号应一致: %+v", pending)
	}
	p, err := s2.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	requireParentMidSecondHandover(t, p, hAt2)

	// 相同交出请求幂等返回同一编号，不另建交接。
	again, err := s2.Handover(HandoverInput{
		TransferID: "TR-002", SampleID: "S-001",
		FromHolder: "李四", FromLocation: "实验室B",
		ToHolder: "王五", ToLocation: "实验室C", HandedOverAt: hAt2,
	})
	if err != nil {
		t.Fatalf("相同交出请求应返回既有交接: %v", err)
	}
	if again.TransferID != "TR-002" {
		t.Fatalf("重复提交返回的交接编号应一致, got %q", again.TransferID)
	}
	if len(s2.data.Transfers) != 2 {
		t.Fatalf("幂等提交不得新增交接, got %d 条", len(s2.data.Transfers))
	}

	// 待确认交接仍由原指定接收人按原条件确认；旧交接保持历史事实。
	done, err := s2.Confirm(ConfirmInput{"TR-002", "王五", "实验室C", rAt2})
	if err != nil {
		t.Fatalf("恢复后的待确认交接应能按原条件确认: %v", err)
	}
	if !done.Confirmed || done.TransferID != "TR-002" {
		t.Fatalf("确认结果错误: %+v", done)
	}
	old, err := s2.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	requireOldFirstTransfer(t, old, hAt1, rAt1)
}
