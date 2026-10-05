package custody

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
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

// 一条双向关联成立、且与样品当前记录一致的待确认交接 JSON 片段：
// 样品剩余 6.750、持有人张三、地点 A，交接记录完全吻合。
const pendingSample6750 = `"S1": {"id":"S1","initial":10000,"remaining":6750,"holder":"张三","location":"A","children":["C1"],"history":[],"pendingId":"T1"}`

func pendingTransfer(qty int64, fromHolder, fromLocation, extra string) string {
	base := `"T1": {"id":"T1","sampleId":"S1","fromHolder":"` + fromHolder +
		`","fromLocation":"` + fromLocation +
		`","toHolder":"李四","toLocation":"B","qty":` +
		strconv.FormatInt(qty, 10) +
		`,"handedOverAt":"2026-10-02T09:00:00Z","confirmed":false` + extra + `}`
	return base
}

// TestOpenRejectsPendingQuantityMismatch 对应题面示例：样品原 10.000 毫升，
// 已分出 3.250 毫升，当前待确认交接若仍写 10.000 毫升，必须拒绝打开，
// 错误同时给出交接量与样品当前剩余量（三位小数毫升）。
func TestOpenRejectsPendingQuantityMismatch(t *testing.T) {
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {`+pendingSample6750+`},
		"transfers": {`+pendingTransfer(10000, "张三", "A", "")+`}
	}`)
	mustRejectOpen(t, path, "S1", "T1", "10.000", "6.750")
}

func TestOpenRejectsPendingHolderMismatch(t *testing.T) {
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {`+pendingSample6750+`},
		"transfers": {`+pendingTransfer(6750, "王五", "A", "")+`}
	}`)
	mustRejectOpen(t, path, "S1", "T1", "王五", "张三")
}

func TestOpenRejectsPendingLocationMismatch(t *testing.T) {
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {`+pendingSample6750+`},
		"transfers": {`+pendingTransfer(6750, "张三", "C", "")+`}
	}`)
	mustRejectOpen(t, path, "S1", "T1", "C", "A")
}

func TestOpenRejectsPendingTransferForDestroyedSample(t *testing.T) {
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {"S1": {"id":"S1","initial":10000,"remaining":0,"holder":"张三","location":"A","children":[],"history":[],"pendingId":"T1","destroyed":{"operator":"张三","location":"A","at":"2026-10-03T09:00:00Z","reason":"废弃","qty":6750}}},
		"transfers": {`+pendingTransfer(6750, "张三", "A", "")+`}
	}`)
	mustRejectOpen(t, path, "S1", "T1", "销毁")
}

func TestOpenRejectsPendingTransferForZeroRemainingSample(t *testing.T) {
	// 未销毁但剩余量为零（例如被篡改过）的样品也不能挂着待确认交接。
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {"S1": {"id":"S1","initial":10000,"remaining":0,"holder":"张三","location":"A","children":[],"history":[],"pendingId":"T1"}},
		"transfers": {`+pendingTransfer(0, "张三", "A", "")+`}
	}`)
	mustRejectOpen(t, path, "S1", "T1", "0.000")
}

func TestOpenAllowsConsistentPendingAfterSplit(t *testing.T) {
	// 题面示例的合法分支：10.000 毫升分出 3.250 毫升后，以剩余 6.750
	// 毫升发起的待确认交接，交出人/地点与当前记录一致时应正常恢复，
	// 保持待确认状态，并仍可由指定接收人在目的地点确认。
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustRegister(t, s, "S1", "10.000", "张三", "实验室A")
	if _, err := s.Split(SplitInput{ParentID: "S1", Parts: []SplitPart{{ID: "C1", Qty: "3.250"}}}); err != nil {
		t.Fatal(err)
	}
	handedAt := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	if _, err := s.Handover(HandoverInput{
		TransferID: "T1", SampleID: "S1",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B", HandedOverAt: handedAt,
	}); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("待确认交接与样品当前记录一致时应能打开: %v", err)
	}
	view, err := s2.GetSample("S1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Remaining != "6.750" || view.Holder != "张三" || view.Location != "实验室A" {
		t.Fatalf("恢复后样品当前记录不应被改动: %+v", view)
	}
	if view.PendingTransfer == nil || view.PendingTransfer.TransferID != "T1" || view.PendingTransfer.Confirmed {
		t.Fatalf("交接应保持待确认状态: %+v", view.PendingTransfer)
	}
	if view.PendingTransfer.Qty != "6.750" {
		t.Fatalf("待确认交接量应为 6.750, got %s", view.PendingTransfer.Qty)
	}
	received, err := s2.Confirm(ConfirmInput{"T1", "李四", "实验室B", handedAt.Add(time.Hour)})
	if err != nil {
		t.Fatalf("恢复后应仍能按原规则由指定接收人确认: %v", err)
	}
	if !received.Confirmed {
		t.Fatalf("确认后交接应结束待确认状态: %+v", received)
	}
	after, err := s2.GetSample("S1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Holder != "李四" || after.Location != "实验室B" || after.PendingTransfer != nil {
		t.Fatalf("确认后持有人和地点应更新且待确认结束: %+v", after)
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
