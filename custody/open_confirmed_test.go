package custody

import (
	"strings"
	"testing"
	"time"
)

// 一份已完成交接、可正常恢复的最小文件：样品 S1 已由李四在 B 持有，
// 交接 T1 记着完整的接收事实（确认人李四、接收时间 11:00）。
const rawConfirmedTransfer = `"T1": {"id":"T1","sampleId":"S1","fromHolder":"张三","fromLocation":"A","toHolder":"李四","toLocation":"B","qty":1000,"handedOverAt":"2026-10-02T10:00:00Z","confirmed":true,"receivedAt":"2026-10-02T11:00:00Z","confirmedBy":"李四"}`

func rawLedgerWithConfirmed(transferJSON string) string {
	return `{
  "version": 1,
  "samples": {"S1": {"id":"S1","initial":1000,"remaining":1000,"holder":"李四","location":"B","children":[],"history":[]}},
  "transfers": {` + transferJSON + `}
}`
}

func TestOpenRejectsConfirmedTransferWithoutConfirmer(t *testing.T) {
	// 已确认却没有确认人字段。
	bad := `"T1": {"id":"T1","sampleId":"S1","fromHolder":"张三","fromLocation":"A","toHolder":"李四","toLocation":"B","qty":1000,"handedOverAt":"2026-10-02T10:00:00Z","confirmed":true,"receivedAt":"2026-10-02T11:00:00Z"}`
	path := writeRawLedger(t, rawLedgerWithConfirmed(bad))
	mustRejectOpen(t, path, "T1", "S1", "确认人", "缺少")
}

func TestOpenRejectsConfirmedTransferWithEmptyConfirmer(t *testing.T) {
	bad := strings.Replace(rawConfirmedTransfer, `"confirmedBy":"李四"`, `"confirmedBy":""`, 1)
	path := writeRawLedger(t, rawLedgerWithConfirmed(bad))
	mustRejectOpen(t, path, "T1", "S1", "确认人", "缺少")
}

func TestOpenRejectsConfirmedTransferWithBlankConfirmer(t *testing.T) {
	bad := strings.Replace(rawConfirmedTransfer, `"confirmedBy":"李四"`, `"confirmedBy":" \t "`, 1)
	path := writeRawLedger(t, rawLedgerWithConfirmed(bad))
	mustRejectOpen(t, path, "T1", "S1", "确认人", "空白")
}

func TestOpenRejectsConfirmedTransferWithPaddedConfirmer(t *testing.T) {
	// 带首尾空白、不能与原定接收人精确一致，也按确认人不符拒绝，
	// 不能在恢复时替它去空白后接受。
	bad := strings.Replace(rawConfirmedTransfer, `"confirmedBy":"李四"`, `"confirmedBy":" 李四 "`, 1)
	path := writeRawLedger(t, rawLedgerWithConfirmed(bad))
	mustRejectOpen(t, path, "T1", "S1", "确认人", "李四", "不一致")
}

func TestOpenRejectsConfirmedTransferWithWrongConfirmer(t *testing.T) {
	bad := strings.Replace(rawConfirmedTransfer, `"confirmedBy":"李四"`, `"confirmedBy":"王五"`, 1)
	path := writeRawLedger(t, rawLedgerWithConfirmed(bad))
	mustRejectOpen(t, path, "T1", "S1", "王五", "李四", "不一致")
}

func TestOpenRejectsConfirmedTransferWithoutReceivedAt(t *testing.T) {
	bad := strings.Replace(rawConfirmedTransfer, `,"receivedAt":"2026-10-02T11:00:00Z"`, "", 1)
	path := writeRawLedger(t, rawLedgerWithConfirmed(bad))
	mustRejectOpen(t, path, "T1", "S1", "接收时间", "缺少")
}

func TestOpenRejectsConfirmedTransferWithZeroReceivedAt(t *testing.T) {
	bad := strings.Replace(rawConfirmedTransfer,
		`"receivedAt":"2026-10-02T11:00:00Z"`,
		`"receivedAt":"0001-01-01T00:00:00Z"`, 1)
	path := writeRawLedger(t, rawLedgerWithConfirmed(bad))
	mustRejectOpen(t, path, "T1", "S1", "接收时间", "零值")
}

func TestOpenRejectsConfirmedTransferWithReceivedBeforeHanded(t *testing.T) {
	bad := strings.Replace(rawConfirmedTransfer,
		`"receivedAt":"2026-10-02T11:00:00Z"`,
		`"receivedAt":"2026-10-02T09:00:00Z"`, 1)
	path := writeRawLedger(t, rawLedgerWithConfirmed(bad))
	mustRejectOpen(t, path, "T1", "S1", "早于", "2026-10-02T09:00:00Z", "2026-10-02T10:00:00Z")
}

func TestOpenRejectsBadConfirmedTransferDespiteOtherValidRecords(t *testing.T) {
	// 文件里另有完全正常的样品与交接时，也不得只加载正常部分。
	content := `{
  "version": 1,
  "samples": {
    "S1": {"id":"S1","initial":1000,"remaining":1000,"holder":"李四","location":"B","children":[],"history":[]},
    "S2": {"id":"S2","initial":500,"remaining":500,"holder":"赵六","location":"D","children":[],"history":[]}
  },
  "transfers": {
    "T1": {"id":"T1","sampleId":"S1","fromHolder":"张三","fromLocation":"A","toHolder":"李四","toLocation":"B","qty":1000,"handedOverAt":"2026-10-02T10:00:00Z","confirmed":true,"receivedAt":"2026-10-02T11:00:00Z","confirmedBy":"李四"},
    "T2": {"id":"T2","sampleId":"S2","fromHolder":"赵六","fromLocation":"D","toHolder":"王五","toLocation":"E","qty":500,"handedOverAt":"2026-10-02T10:00:00Z","confirmed":true}
  }
}`
	path := writeRawLedger(t, content)
	mustRejectOpen(t, path, "T2", "S2")
}

// TestOpenRestoresConfirmedTransferWithEqualTimes：接收时间恰好等于交出
// 时间是合法的，应正常恢复。
func TestOpenRestoresConfirmedTransferWithEqualTimes(t *testing.T) {
	equal := strings.Replace(rawConfirmedTransfer,
		`"receivedAt":"2026-10-02T11:00:00Z"`,
		`"receivedAt":"2026-10-02T10:00:00Z"`, 1)
	path := writeRawLedger(t, rawLedgerWithConfirmed(equal))
	s, err := Open(path)
	if err != nil {
		t.Fatalf("接收时间等于交出时间应正常恢复: %v", err)
	}
	tr, err := s.GetTransfer("T1")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	if !tr.Confirmed || tr.ConfirmedBy != "李四" || tr.ReceivedAt == nil || !tr.ReceivedAt.Equal(want) {
		t.Fatalf("恢复后的已确认交接信息错误: %+v", tr)
	}
	if tr.Qty != "1.000" {
		t.Fatalf("交接量显示错误: %s", tr.Qty)
	}
}

// TestOpenRestoresConfirmedTransferAcrossTimezones：交出时间记为北京时间
// 18:00（UTC+8），接收时间记为同一天 UTC 10:00，二者是同一实际时刻，
// 不得误判为时间倒置。
func TestOpenRestoresConfirmedTransferAcrossTimezones(t *testing.T) {
	beijing := strings.Replace(rawConfirmedTransfer,
		`"handedOverAt":"2026-10-02T10:00:00Z"`,
		`"handedOverAt":"2026-10-02T18:00:00+08:00"`, 1)
	sameInstant := strings.Replace(beijing,
		`"receivedAt":"2026-10-02T11:00:00Z"`,
		`"receivedAt":"2026-10-02T10:00:00Z"`, 1)
	path := writeRawLedger(t, rawLedgerWithConfirmed(sameInstant))
	s, err := Open(path)
	if err != nil {
		t.Fatalf("跨时区表示同一时刻应视为接收时间不早于交出时间: %v", err)
	}
	tr, err := s.GetTransfer("T1")
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Confirmed || tr.ConfirmedBy != "李四" {
		t.Fatalf("交接应保持李四确认的已确认状态: %+v", tr)
	}
}

// TestOpenRestoresConfirmedTransferAndKeepsFacts：正常恢复后按交接编号
// 查询仍显示已确认，保留原确认人、接收时间与交接量，无需再次确认接收；
// 原样以 10.000 完成交接后分出 3.250 子样、剩余 6.750 又交给另一人，
// 旧交接仍按当时的 10.000 与原接收人恢复；样品后来被销毁也不影响旧
// 确认信息的有效性。
func TestOpenRestoresConfirmedTransferAfterSplitTransferAndDestroy(t *testing.T) {
	s, hAt1, rAt1, hAt2, rAt2 := setupDivergedHandover(t)
	if _, err := s.Confirm(ConfirmInput{"TR-002", "王五", "实验室C", rAt2}); err != nil {
		t.Fatalf("第二次交接接收: %v", err)
	}
	if _, err := s.Destroy(DestroyInput{
		SampleID: "S-001-A", Operator: "李四", Location: "实验室B",
		At: hAt2.Add(2 * time.Hour), Reason: "子样实验结束按规程销毁",
	}); err != nil {
		t.Fatalf("销毁子样: %v", err)
	}

	s2, err := Open(s.path)
	if err != nil {
		t.Fatalf("含分装、多次交接与销毁的合法历史文件应正常恢复: %v", err)
	}
	old, err := s2.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	requireOldFirstTransfer(t, old, hAt1, rAt1)

	second, err := s2.GetTransfer("TR-002")
	if err != nil {
		t.Fatal(err)
	}
	if !second.Confirmed || second.ConfirmedBy != "王五" || second.ReceivedAt == nil ||
		!second.ReceivedAt.Equal(rAt2) || second.Qty != "6.750" {
		t.Fatalf("第二次交接恢复后应仍为已确认并保留接收信息: %+v", second)
	}

	// 恢复后不需要再次确认接收：重复提交完全相同的接收信息幂等返回，
	// 不会另记一次转手。
	again, err := s2.Confirm(ConfirmInput{"TR-001", "李四", "实验室B", rAt1})
	if err != nil {
		t.Fatalf("恢复后的已确认交接重复提交应幂等返回: %v", err)
	}
	requireOldFirstTransfer(t, again, hAt1, rAt1)
	child, err := s2.GetSample("S-001-A")
	if err != nil {
		t.Fatal(err)
	}
	if child.Destruction == nil {
		t.Fatalf("子样的销毁信息也应原样保留: %+v", child)
	}
}
