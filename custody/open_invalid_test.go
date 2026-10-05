package custody

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeRawLedger 把原始 JSON 写入临时目录并返回路径，用于构造
// 无法通过公开操作产生的损坏数据文件。
func writeRawLedger(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入测试数据文件: %v", err)
	}
	return path
}

// mustRejectOpen 断言打开损坏文件失败：返回 nil Store、错误可用
// errors.Is 判定为 ErrInvalid、错误文字提及全部给定编号，且原文件
// 内容不被改写。
func mustRejectOpen(t *testing.T, path string, mentions ...string) {
	t.Helper()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取测试数据文件: %v", err)
	}
	s, err := Open(path)
	if err == nil {
		t.Fatalf("损坏的数据文件不应打开成功")
	}
	if s != nil {
		t.Fatalf("打开失败时不应返回可用的 Store")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("错误应可用 errors.Is 判定为 ErrInvalid, got %v", err)
	}
	for _, m := range mentions {
		if !strings.Contains(err.Error(), m) {
			t.Fatalf("错误文字应提及 %q: %v", m, err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("重新读取测试数据文件: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("打开失败不得改写原文件")
	}
}

// 一份合法的最简样品记录 JSON 片段（无待确认交接）。
const rawSampleS1 = `"S1": {"id":"S1","initial":1000,"remaining":1000,"holder":"张三","location":"A","children":[],"history":[{"kind":"register","time":"2026-10-02T09:00:00Z","holder":"张三","location":"A"}]}`

func TestOpenRejectsDanglingPendingID(t *testing.T) {
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {"S1": {"id":"S1","initial":1000,"remaining":1000,"holder":"张三","location":"A","children":[],"history":[],"pendingId":"T-ghost"}},
		"transfers": {}
	}`)
	mustRejectOpen(t, path, "S1", "T-ghost")
}

func TestOpenRejectsPendingIDToNullTransfer(t *testing.T) {
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {"S1": {"id":"S1","initial":1000,"remaining":1000,"holder":"张三","location":"A","children":[],"history":[],"pendingId":"T1"}},
		"transfers": {"T1": null}
	}`)
	mustRejectOpen(t, path, "S1", "T1")
}

func TestOpenRejectsPendingIDToConfirmedTransfer(t *testing.T) {
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {"S1": {"id":"S1","initial":1000,"remaining":1000,"holder":"张三","location":"A","children":[],"history":[],"pendingId":"T1"}},
		"transfers": {"T1": {"id":"T1","sampleId":"S1","fromHolder":"张三","fromLocation":"A","toHolder":"李四","toLocation":"B","qty":1000,"handedOverAt":"2026-10-02T09:00:00Z","confirmed":true,"receivedAt":"2026-10-02T10:00:00Z","confirmedBy":"李四"}}
	}`)
	mustRejectOpen(t, path, "S1", "T1")
}

func TestOpenRejectsPendingIDToOtherSamplesTransfer(t *testing.T) {
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {
			"S1": {"id":"S1","initial":1000,"remaining":1000,"holder":"张三","location":"A","children":[],"history":[],"pendingId":"T1"},
			"S2": {"id":"S2","initial":500,"remaining":500,"holder":"王五","location":"C","children":[],"history":[]}
		},
		"transfers": {"T1": {"id":"T1","sampleId":"S2","fromHolder":"王五","fromLocation":"C","toHolder":"李四","toLocation":"B","qty":500,"handedOverAt":"2026-10-02T09:00:00Z","confirmed":false}}
	}`)
	mustRejectOpen(t, path, "S1", "T1")
}

func TestOpenRejectsUnconfirmedTransferWithoutSample(t *testing.T) {
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {`+rawSampleS1+`},
		"transfers": {"T1": {"id":"T1","sampleId":"S-ghost","fromHolder":"张三","fromLocation":"A","toHolder":"李四","toLocation":"B","qty":1000,"handedOverAt":"2026-10-02T09:00:00Z","confirmed":false}}
	}`)
	mustRejectOpen(t, path, "T1", "S-ghost")
}

func TestOpenRejectsUnconfirmedTransferWhenSampleClearedPending(t *testing.T) {
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {`+rawSampleS1+`},
		"transfers": {"T1": {"id":"T1","sampleId":"S1","fromHolder":"张三","fromLocation":"A","toHolder":"李四","toLocation":"B","qty":1000,"handedOverAt":"2026-10-02T09:00:00Z","confirmed":false}}
	}`)
	mustRejectOpen(t, path, "T1", "S1")
}

func TestOpenRejectsTwoUnconfirmedTransfersForSameSample(t *testing.T) {
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {"S1": {"id":"S1","initial":1000,"remaining":1000,"holder":"张三","location":"A","children":[],"history":[],"pendingId":"T1"}},
		"transfers": {
			"T1": {"id":"T1","sampleId":"S1","fromHolder":"张三","fromLocation":"A","toHolder":"李四","toLocation":"B","qty":1000,"handedOverAt":"2026-10-02T09:00:00Z","confirmed":false},
			"T2": {"id":"T2","sampleId":"S1","fromHolder":"张三","fromLocation":"A","toHolder":"王五","toLocation":"C","qty":1000,"handedOverAt":"2026-10-02T11:00:00Z","confirmed":false}
		}
	}`)
	mustRejectOpen(t, path, "T2", "S1")
}

func TestOpenRejectsNullSampleEntry(t *testing.T) {
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {"S1": null},
		"transfers": {}
	}`)
	mustRejectOpen(t, path, "S1")
}

func TestOpenRejectsNullTransferEntry(t *testing.T) {
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {`+rawSampleS1+`},
		"transfers": {"T1": null}
	}`)
	mustRejectOpen(t, path, "T1")
}

func TestOpenRejectsPartialLoad(t *testing.T) {
	// 文件中同时存在完全合法的记录时，也不得只加载其中一部分。
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {
			"OK": {"id":"OK","initial":100,"remaining":100,"holder":"h","location":"l","children":[],"history":[]},
			"BAD": {"id":"BAD","initial":100,"remaining":100,"holder":"h","location":"l","children":[],"history":[],"pendingId":"T-ghost"}
		},
		"transfers": {}
	}`)
	mustRejectOpen(t, path, "BAD", "T-ghost")
}

func TestOpenAllowsConfirmedTransferHistory(t *testing.T) {
	// 样品完成旧交接后分装，再发起新交接：旧交接的持有人、地点、数量
	// 与样品当前状态不同是正常历史，不应拒绝打开。
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustRegister(t, s, "P", "7.500", "张三", "A")
	at := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	if _, err := s.Handover(HandoverInput{
		TransferID: "T-old", SampleID: "P",
		FromHolder: "张三", FromLocation: "A",
		ToHolder: "李四", ToLocation: "B", HandedOverAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Confirm(ConfirmInput{"T-old", "李四", "B", at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{{ID: "C", Qty: "2.500"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Handover(HandoverInput{
		TransferID: "T-new", SampleID: "P",
		FromHolder: "李四", FromLocation: "B",
		ToHolder: "王五", ToLocation: "C", HandedOverAt: at.Add(2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("含已确认历史交接与当前待确认交接的合法文件应能打开: %v", err)
	}
	p, err := s2.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if p.PendingTransfer == nil || p.PendingTransfer.TransferID != "T-new" {
		t.Fatalf("重开后当前待确认交接应保留: %+v", p.PendingTransfer)
	}
	old, err := s2.GetTransfer("T-old")
	if err != nil {
		t.Fatal(err)
	}
	if !old.Confirmed {
		t.Fatalf("旧交接应保持已确认: %+v", old)
	}
}

func TestOpenAllowsEmptyCollections(t *testing.T) {
	for name, content := range map[string]string{
		"集合整体缺省":   `{"version": 1}`,
		"集合为 null": `{"version": 1, "samples": null, "transfers": null}`,
		"集合为空对象":   `{"version": 1, "samples": {}, "transfers": {}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeRawLedger(t, content)
			s, err := Open(path)
			if err != nil {
				t.Fatalf("空集合应沿用现有含义正常打开: %v", err)
			}
			if _, err := s.GetSample("X"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("空数据中查询应返回 ErrNotFound, got %v", err)
			}
		})
	}
}

// divergedScenario 是任务示例的完整文件：原样 S-001 初始 10.000，完成
// TR-001（已确认，记录当时的 10.000）后分出 3.250 的子样 S-001-A，又以
// 剩余的 6.750 发起 TR-002（待确认）。占位符可注入单项矛盾：
//   - __REMAINING__：原样当前剩余量（千分之一毫升）
//   - __SAMPLE_EXTRA__：原样记录内的额外字段（用于注入销毁信息）
//   - __PENDING_TR__：TR-002 记录的字段内容
const divergedScenario = `{
  "version": 1,
  "samples": {
    "S-001": {"id":"S-001","initial":10000,"remaining":__REMAINING__,"holder":"李四","location":"实验室B","children":["S-001-A"],"history":[
      {"kind":"register","time":"2026-10-02T09:00:00Z","holder":"张三","location":"实验室A","detail":"登记原样"},
      {"kind":"transfer-out","time":"2026-10-02T10:00:00Z","holder":"张三","location":"实验室A","detail":"发起交接 TR-001，待 李四 在 实验室B 接收"},
      {"kind":"transfer-in","time":"2026-10-02T11:00:00Z","holder":"李四","location":"实验室B","detail":"交接 TR-001 确认接收（交出时间 2026-10-02T10:00:00Z）"},
      {"kind":"split","time":"2026-10-02T12:00:00Z","holder":"李四","location":"实验室B","detail":"分装创建子样 S-001-A"},
      {"kind":"transfer-out","time":"2026-10-03T09:00:00Z","holder":"李四","location":"实验室B","detail":"发起交接 TR-002，待 王五 在 实验室C 接收"}
    ],"pendingId":"TR-002"__SAMPLE_EXTRA__},
    "S-001-A": {"id":"S-001-A","parentId":"S-001","initial":3250,"remaining":3250,"holder":"李四","location":"实验室B","children":[],"history":[
      {"kind":"split","time":"2026-10-02T12:00:00Z","holder":"李四","location":"实验室B","detail":"由样品 S-001 分装"}
    ]}
  },
  "transfers": {
    "TR-001": {"id":"TR-001","sampleId":"S-001","fromHolder":"张三","fromLocation":"实验室A","toHolder":"李四","toLocation":"实验室B","qty":10000,"handedOverAt":"2026-10-02T10:00:00Z","confirmed":true,"receivedAt":"2026-10-02T11:00:00Z","confirmedBy":"李四"},
    "TR-002": {__PENDING_TR__}
  }
}`

const consistentPendingTR = `"id":"TR-002","sampleId":"S-001","fromHolder":"李四","fromLocation":"实验室B","toHolder":"王五","toLocation":"实验室C","qty":6750,"handedOverAt":"2026-10-03T09:00:00Z","confirmed":false`

// writeDivergedScenario 写入可替换占位符的示例文件。
func writeDivergedScenario(t *testing.T, remaining, sampleExtra, pendingTR string) string {
	t.Helper()
	if remaining == "" {
		remaining = "6750"
	}
	if pendingTR == "" {
		pendingTR = consistentPendingTR
	}
	content := strings.NewReplacer(
		"__REMAINING__", remaining,
		"__SAMPLE_EXTRA__", sampleExtra,
		"__PENDING_TR__", pendingTR,
	).Replace(divergedScenario)
	return writeRawLedger(t, content)
}

func TestOpenRejectsPendingHandoverWithStaleQuantity(t *testing.T) {
	// 待确认交接仍写着分出子样前的 10.000，而样品当前只剩 6.750。
	stale := strings.Replace(consistentPendingTR, `"qty":6750`, `"qty":10000`, 1)
	path := writeDivergedScenario(t, "6750", "", stale)
	mustRejectOpen(t, path, "S-001", "TR-002", "交接量", "10.000", "6.750")
}

func TestOpenRejectsPendingHandoverWithWrongHolder(t *testing.T) {
	wrong := strings.Replace(consistentPendingTR, `"fromHolder":"李四"`, `"fromHolder":"张三"`, 1)
	path := writeDivergedScenario(t, "", "", wrong)
	mustRejectOpen(t, path, "S-001", "TR-002", "交出人", "持有人", "张三", "李四")
}

func TestOpenRejectsPendingHandoverWithWrongLocation(t *testing.T) {
	wrong := strings.Replace(consistentPendingTR, `"fromLocation":"实验室B"`, `"fromLocation":"实验室A"`, 1)
	path := writeDivergedScenario(t, "", "", wrong)
	mustRejectOpen(t, path, "S-001", "TR-002", "交出地点", "当前地点", "实验室A", "实验室B")
}

func TestOpenRejectsPendingHandoverForDestroyedSample(t *testing.T) {
	extra := `,"destroyed":{"operator":"李四","location":"实验室B","at":"2026-10-03T08:00:00Z","reason":"实验结束按规程销毁","qty":6750}`
	path := writeDivergedScenario(t, "", extra, "")
	mustRejectOpen(t, path, "S-001", "TR-002", "销毁")
}

func TestOpenRejectsPendingHandoverForZeroRemainingSample(t *testing.T) {
	path := writeDivergedScenario(t, "0", "", "")
	mustRejectOpen(t, path, "S-001", "TR-002", "0.000")
}

// rawConfirmedTransfer 是一条合法的已确认交接记录 JSON 片段，
// 可通过 strings.Replace 注入单项接收事实异常。
const rawConfirmedTransfer = `"T1": {"id":"T1","sampleId":"S1","fromHolder":"张三","fromLocation":"A","toHolder":"李四","toLocation":"B","qty":1000,"handedOverAt":"2026-10-02T09:00:00Z","confirmed":true,"receivedAt":"2026-10-02T10:00:00Z","confirmedBy":"李四"}`

// writeConfirmedScenario 写入只含一份样品和一条已确认交接的数据文件。
func writeConfirmedScenario(t *testing.T, transfer string) string {
	t.Helper()
	return writeRawLedger(t, `{
		"version": 1,
		"samples": {`+rawSampleS1+`},
		"transfers": {`+transfer+`}
	}`)
}

func TestOpenRejectsConfirmedTransferWithoutReceivedAt(t *testing.T) {
	tr := strings.Replace(rawConfirmedTransfer, `,"receivedAt":"2026-10-02T10:00:00Z"`, ``, 1)
	mustRejectOpen(t, writeConfirmedScenario(t, tr), "T1", "S1", "接收时间")
}

func TestOpenRejectsConfirmedTransferWithZeroReceivedAt(t *testing.T) {
	tr := strings.Replace(rawConfirmedTransfer, `"receivedAt":"2026-10-02T10:00:00Z"`, `"receivedAt":"0001-01-01T00:00:00Z"`, 1)
	mustRejectOpen(t, writeConfirmedScenario(t, tr), "T1", "S1", "接收时间")
}

func TestOpenRejectsConfirmedTransferWithReceivedBeforeHandover(t *testing.T) {
	tr := strings.Replace(rawConfirmedTransfer, `"receivedAt":"2026-10-02T10:00:00Z"`, `"receivedAt":"2026-10-02T08:00:00Z"`, 1)
	mustRejectOpen(t, writeConfirmedScenario(t, tr), "T1", "S1", "接收时间", "早于", "交出时间")
}

func TestOpenRejectsConfirmedTransferWithoutConfirmer(t *testing.T) {
	tr := strings.Replace(rawConfirmedTransfer, `,"confirmedBy":"李四"`, ``, 1)
	mustRejectOpen(t, writeConfirmedScenario(t, tr), "T1", "S1", "确认人")
}

func TestOpenRejectsConfirmedTransferWithBlankConfirmer(t *testing.T) {
	tr := strings.Replace(rawConfirmedTransfer, `"confirmedBy":"李四"`, `"confirmedBy":"  "`, 1)
	mustRejectOpen(t, writeConfirmedScenario(t, tr), "T1", "S1", "确认人")
}

func TestOpenRejectsConfirmedTransferWithWrongConfirmer(t *testing.T) {
	tr := strings.Replace(rawConfirmedTransfer, `"confirmedBy":"李四"`, `"confirmedBy":"王五"`, 1)
	mustRejectOpen(t, writeConfirmedScenario(t, tr), "T1", "S1", "确认人", "王五", "李四")
}

func TestOpenRejectsConfirmedTransferPartialLoad(t *testing.T) {
	// 文件中同时存在完全合法的记录时，也不得只加载其中一部分。
	tr := strings.Replace(rawConfirmedTransfer, `,"receivedAt":"2026-10-02T10:00:00Z"`, ``, 1)
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {
			"OK": {"id":"OK","initial":100,"remaining":100,"holder":"h","location":"l","children":[],"history":[]},
			`+rawSampleS1+`
		},
		"transfers": {`+tr+`}
	}`)
	mustRejectOpen(t, path, "T1", "S1")
}

// TestOpenRestoresConfirmedTransferWithEqualInstants 覆盖接收时间的合法边界：
// 接收时间恰好等于交出时间可以恢复；时间先后按实际时刻判断，交出时间记为
// 北京时间十八点、接收时间记为同一天的协调世界时十点，二者是同一时刻。
// 恢复后按交接编号查询仍显示已确认及原确认人、接收时间与交接量。
func TestOpenRestoresConfirmedTransferWithEqualInstants(t *testing.T) {
	tr := `"T1": {"id":"T1","sampleId":"S1","fromHolder":"张三","fromLocation":"A","toHolder":"李四","toLocation":"B","qty":1000,"handedOverAt":"2026-10-02T18:00:00+08:00","confirmed":true,"receivedAt":"2026-10-02T10:00:00Z","confirmedBy":"李四"}`
	path := writeConfirmedScenario(t, tr)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("接收时间等于交出时间的已确认交接应正常恢复: %v", err)
	}
	v, err := s.GetTransfer("T1")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Confirmed || v.ConfirmedBy != "李四" || v.Qty != "1.000" {
		t.Fatalf("已确认交接应保留原确认人与交接量: %+v", v)
	}
	want := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	if v.ReceivedAt == nil || !v.ReceivedAt.Equal(want) {
		t.Fatalf("接收时间应原样恢复: %+v", v)
	}
}

// TestOpenRestoresConsistentPendingHandover 覆盖任务示例的合法分支：
// 待确认交接正确记录 6.750、当前持有人李四和地点实验室B 时正常恢复，
// 保持待确认状态，仍由指定接收人王五在目的地点实验室C 确认；旧交接
// TR-001 当时的 10.000 记录继续保留且可查询，不被拒绝或改写。
func TestOpenRestoresConsistentPendingHandover(t *testing.T) {
	s, hAt1, rAt1, hAt2, rAt2 := setupDivergedHandover(t)

	s2, err := Open(s.path)
	if err != nil {
		t.Fatalf("内容一致的待确认交接应随文件正常恢复: %v", err)
	}
	p, err := s2.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	requireParentMidSecondHandover(t, p, hAt2)

	old, err := s2.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	requireOldFirstTransfer(t, old, hAt1, rAt1)

	done, err := s2.Confirm(ConfirmInput{"TR-002", "王五", "实验室C", rAt2})
	if err != nil {
		t.Fatalf("恢复后的待确认交接应仍能由指定接收人在目的地点确认: %v", err)
	}
	if !done.Confirmed || done.Qty != "6.750" || done.ConfirmedBy != "王五" {
		t.Fatalf("确认结果错误: %+v", done)
	}
	p, _ = s2.GetSample("S-001")
	if p.Holder != "王五" || p.Location != "实验室C" || p.PendingTransfer != nil {
		t.Fatalf("确认后持有人和地点才改变，待确认状态结束: %+v", p)
	}
}
