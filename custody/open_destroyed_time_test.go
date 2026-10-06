package custody

import (
	"os"
	"strings"
	"testing"
	"time"
)

// 销毁时间恢复核对相关测试。
//
// 规则：带销毁信息的样品恢复时，销毁时间必须存在且非零值（与是否有保管
// 历史无关），且按实际时刻不得早于该样品自身任何一条保管历史的发生时刻。
// 保管历史按操作发生顺序保存、不保证时间递增，因此核对针对全部历史而非
// 末尾一条；子样或其他样品的更晚记录不影响本样品。恢复不得重排或改写
// 历史，时间合法的已销毁样品继续按原有数量规则恢复。

// destroyedTimeScenario 构造任务示例：样品 P 十二点完成接收，随后追加的
// 分装记录时间是十点（历史末尾不是最晚时刻），保存的销毁时间是十一点。
// 数量完全守恒（初始 10.000 = 销毁 6.000 + 子样 4.000）。
func destroyedTimeScenario(destroyAt time.Time) *ledger {
	regAt := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	recvAt := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	splitAt := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC) // 追加在后、时刻更早
	p := &sampleRecord{
		ID:        "P",
		Initial:   10000,
		Remaining: 0,
		Holder:    "李四",
		Location:  "实验室B",
		Children:  []string{"C"},
		History: []historyRecord{
			{Kind: "register", Time: regAt, Holder: "张三", Location: "实验室A"},
			{Kind: "transfer-in", Time: recvAt, Holder: "李四", Location: "实验室B"},
			{Kind: "split", Time: splitAt, Holder: "李四", Location: "实验室B"},
			{Kind: "destroy", Time: destroyAt, Holder: "李四", Location: "实验室B"},
		},
		Destroyed: &destructionRecord{
			Operator: "李四", Location: "实验室B",
			At: destroyAt, Reason: "实验结束按规程销毁", Qty: 6000,
		},
	}
	c := &sampleRecord{
		ID: "C", ParentID: "P", Initial: 4000, Remaining: 4000,
		Holder: "李四", Location: "实验室B", Children: []string{},
		History: []historyRecord{
			{Kind: "split", Time: splitAt, Holder: "李四", Location: "实验室B"},
		},
	}
	return &ledger{Samples: map[string]*sampleRecord{"P": p, "C": c}}
}

// TestOpenRejectsDestroyedAtBeforeOwnHistory 任务示例：销毁时间十一点晚于
// 历史末尾的分装（十点），却早于更晚追加的接收（十二点），即使数量完全
// 守恒也必须拒绝打开——样品在销毁之后才完成接收。错误同时给出销毁时间
// 与冲突历史的时间。
func TestOpenRejectsDestroyedAtBeforeOwnHistory(t *testing.T) {
	destroyAt := time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC)
	path := writeStructLedger(t, destroyedTimeScenario(destroyAt))
	mustRejectOpen(t, path, "P", "销毁时间",
		"2026-10-02T11:00:00Z", "2026-10-02T12:00:00Z")
}

// TestOpenRejectsDestroyedAtZeroOrMissing 销毁时间缺失或为零值一律拒绝，
// 即使没有保管历史也一样。
func TestOpenRejectsDestroyedAtZeroOrMissing(t *testing.T) {
	// 显式零值时间，且带有保管历史。
	zero := destroyedSample("ZERO", 5000, 0, 5000)
	zero.Destroyed.At = time.Time{}
	path := writeStructLedger(t, &ledger{Samples: map[string]*sampleRecord{"ZERO": zero}})
	mustRejectOpen(t, path, "ZERO", "销毁时间")

	// 数据文件里根本没有 at 字段，且样品没有任何保管历史。
	path = writeRawLedger(t, `{
		"version": 1,
		"samples": {"NOHIST": {"id":"NOHIST","initial":5000,"remaining":0,"holder":"李四","location":"实验室B","children":[],"history":[],"destroyed":{"operator":"李四","location":"实验室B","reason":"实验结束按规程销毁","qty":5000}}},
		"transfers": {}
	}`)
	mustRejectOpen(t, path, "NOHIST", "销毁时间")
}

// TestOpenRestoresDestroyedAtLatestHistoryInstant 销毁时间恰好等于自身历史
// 中最晚的时刻（接收时刻）可以接受；同一时刻用不同时区表示也得到相同
// 结果。恢复后保管历史保持原顺序与原时间，不因核对而重排或改写。
func TestOpenRestoresDestroyedAtLatestHistoryInstant(t *testing.T) {
	recvAt := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	splitAt := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	tokyo := time.FixedZone("UTC+8", 8*60*60)

	// 销毁时间用 UTC+8 表示同一时刻（当地 20:00 == UTC 12:00）。
	l := destroyedTimeScenario(recvAt.In(tokyo))
	path := writeStructLedger(t, l)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("销毁时间等于自身最晚历史时刻（跨时区表示）应正常恢复: %v", err)
	}
	p, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if p.Destruction == nil || p.Destruction.Qty != "6.000" ||
		!p.Destruction.At.Equal(recvAt) {
		t.Fatalf("销毁信息应按原数量规则恢复: %+v", p.Destruction)
	}
	// 历史保持追加顺序与原时间：末尾仍是时刻更早的分装，不被重排。
	wantKinds := []string{"register", "transfer-in", "split", "destroy"}
	if len(p.History) != len(wantKinds) {
		t.Fatalf("历史条数应保持不变: %+v", p.History)
	}
	for i, k := range wantKinds {
		if p.History[i].Kind != k {
			t.Fatalf("历史顺序不应因核对而重排: %+v", p.History)
		}
	}
	if !p.History[2].Time.Equal(splitAt) || !p.History[3].Time.Equal(recvAt) {
		t.Fatalf("历史时间应保持原值: %+v", p.History)
	}
}

// TestOpenDestroyedTimeIgnoresChildAndOtherSamples 销毁时间只看该编号样品
// 自己的历史：已分出的子样或同一文件中其他样品后来发生的交接，不延后
// 它可以销毁的时间；分装子样也按自己的历史判断。
func TestOpenDestroyedTimeIgnoresChildAndOtherSamples(t *testing.T) {
	recvAt := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	splitAt := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	destroyAt := recvAt.Add(30 * time.Minute) // 12:30，晚于 P 自身全部历史

	l := destroyedTimeScenario(destroyAt)
	// 子样 C 后来发生的交接远晚于 P 的销毁时间，不影响 P 的恢复。
	c := l.Samples["C"]
	c.Holder, c.Location = "王五", "实验室C"
	c.History = append(c.History,
		historyRecord{Kind: "transfer-out", Time: recvAt.Add(2 * time.Hour), Holder: "李四", Location: "实验室B"},
		historyRecord{Kind: "transfer-in", Time: recvAt.Add(3 * time.Hour), Holder: "王五", Location: "实验室C"},
	)
	// 另一份独立原样 Q 的历史更晚，同样不影响 P。
	q := &sampleRecord{
		ID: "Q", Initial: 2000, Remaining: 2000, Holder: "赵六", Location: "实验室D",
		Children: []string{},
		History: []historyRecord{
			{Kind: "register", Time: recvAt.Add(5 * time.Hour), Holder: "赵六", Location: "实验室D"},
		},
	}
	l.Samples["Q"] = q

	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("子样与其他样品的更晚历史不应影响 P 的销毁时间核对: %v", err)
	}
	p, _ := s.GetSample("P")
	if p.Destruction == nil || !p.Destruction.At.Equal(destroyAt) {
		t.Fatalf("P 的销毁记录应正常恢复: %+v", p.Destruction)
	}

	// 分装子样按自己的历史判断：C 的销毁时间早于自己的分装历史，拒绝。
	bad := destroyedSampleWithParent("C2", "P2", 4000, 4000)
	bad.Destroyed.At = splitAt.Add(-time.Hour) // 09:00，早于自身 split 历史 10:00
	bad.History = bad.History[:1]              // 只保留 split@10:00 一条自身历史
	bad.History[0].Kind = "split"
	bad.History[0].Time = splitAt
	p2 := plainChild("P2", "", 10000, 6000)
	p2.Children = []string{"C2"}
	l2 := &ledger{Samples: map[string]*sampleRecord{"P2": p2, "C2": bad}}
	mustRejectOpen(t, writeStructLedger(t, l2), "C2", "销毁时间",
		"2026-10-02T09:00:00Z", "2026-10-02T10:00:00Z")
}

// TestOpenRejectsDestroyedTimeDespiteOtherValidSamples 一份样品的销毁时间
// 倒置时整份文件打开失败，不能只加载其他正常记录；原文件内容保持不变。
func TestOpenRejectsDestroyedTimeDespiteOtherValidSamples(t *testing.T) {
	destroyAt := time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC)
	l := destroyedTimeScenario(destroyAt)
	l.Samples["OK"] = destroyedSample("OK", 1000, 0, 1000)
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "P", "销毁时间")

	// 原文件内容保持不变：销毁信息没有被删除、没有改成未销毁、时间没有
	// 被自动修正（mustRejectOpen 已逐字节比对，这里再点明关键内容仍在）。
	raw := readFileString(t, path)
	if !strings.Contains(raw, `"destroyed"`) || !strings.Contains(raw, "2026-10-02T11:00:00Z") {
		t.Fatalf("打开失败不得删除销毁信息或自动修正时间: %s", raw)
	}
}

// readFileString 读取测试数据文件内容。
func readFileString(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取测试数据文件: %v", err)
	}
	return string(raw)
}
