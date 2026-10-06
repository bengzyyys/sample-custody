package custody

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// 重新打开带有销毁信息的样品时，销毁时间必须通过与正常销毁功能相同的
// 核对：缺失（无 at 或为 null）或零值一律拒绝（即使没有保管历史）；
// 按实际时刻不得早于该样品自身任何一条保管历史（恰好等于最晚历史时刻
// 合法，时区只影响表示）。核对只看该编号样品自己的历史，不重排、不改
// 写任何记录；不成立时整份文件打开失败：返回 nil Store、错误可用
// errors.Is 判定为 ErrInvalid、原文件保持不变，不能只加载正常记录。

// openDestroyTimeScenario 是任务示例的文件骨架：原样 S-001 初始
// 10.000，在 12:00 完成 TR-001 的接收（10.000 毫升，已确认），随后追加
// 的分装记录时间是 10:00（分出子样 S-001-A 3.250）。历史追加次序与时间
// 先后不一致，最晚历史是 12:00 的接收而不是末尾的分装。占位符：
//   - __AT__：销毁信息中的 at 字段原文（含字段名，便于注入缺失/null）
//   - __QTY__：销毁量（千分之一毫升），用于构造数量守恒的前提
const openDestroyTimeScenario = `{
  "version": 1,
  "samples": {
    "S-001": {"id":"S-001","initial":10000,"remaining":0,"holder":"李四","location":"实验室B","children":["S-001-A"],"history":[
      {"kind":"register","time":"2026-10-02T08:00:00Z","holder":"张三","location":"实验室A","detail":"登记原样"},
      {"kind":"transfer-out","time":"2026-10-02T09:00:00Z","holder":"张三","location":"实验室A","detail":"发起交接 TR-001，待 李四 在 实验室B 接收"},
      {"kind":"transfer-in","time":"2026-10-02T12:00:00Z","holder":"李四","location":"实验室B","detail":"交接 TR-001 确认接收"},
      {"kind":"split","time":"2026-10-02T10:00:00Z","holder":"李四","location":"实验室B","detail":"分装创建子样 S-001-A"}
    ],"destroyed":{"operator":"李四","location":"实验室B",__AT__,"reason":"实验结束按规程销毁","qty":__QTY__}},
    "S-001-A": {"id":"S-001-A","parentId":"S-001","initial":3250,"remaining":3250,"holder":"李四","location":"实验室B","children":[],"history":[
      {"kind":"split","time":"2026-10-02T10:00:00Z","holder":"李四","location":"实验室B","detail":"由样品 S-001 分装"}
    ]}
  },
  "transfers": {
    "TR-001": {"id":"TR-001","sampleId":"S-001","fromHolder":"张三","fromLocation":"实验室A","toHolder":"李四","toLocation":"实验室B","qty":10000,"handedOverAt":"2026-10-02T09:00:00Z","confirmed":true,"receivedAt":"2026-10-02T12:00:00Z","confirmedBy":"李四"}
  }
}`

func writeDestroyTimeScenario(t *testing.T, atJSON, qty string) string {
	t.Helper()
	if atJSON == "" {
		atJSON = `"at":"2026-10-02T11:00:00Z"`
	}
	if qty == "" {
		qty = "6750"
	}
	return writeRawLedger(t, strings.NewReplacer(
		"__AT__", atJSON,
		"__QTY__", qty,
	).Replace(openDestroyTimeScenario))
}

// TestOpenRejectsDestroyedBeforeOwnHistoryDespiteConservation 任务示例：
// 12:00 完成接收、随后追加的分装记录时间是 10:00，保存的销毁时间是
// 11:00。即使 11:00 晚于历史末尾的分装、且销毁量 6.750 + 子样 3.250 =
// 初始量 10.000 完全守恒，打开也必须失败——样品在销毁之后才完成接收。
func TestOpenRejectsDestroyedBeforeOwnHistoryDespiteConservation(t *testing.T) {
	path := writeDestroyTimeScenario(t, "", "")
	mustRejectOpen(t, path, "S-001", "早于",
		"2026-10-02T11:00:00Z", "2026-10-02T12:00:00Z")
}

// TestOpenRejectsDestroyedBeforeHistoryAcrossZones 同一实际时刻换时区
// 表示结果必须相同：历史最晚时刻是 UTC 12:00，销毁时间写作当地 19:00
// （UTC+8）即 UTC 11:00，仍早于接收，必须拒绝，不能按显示钟点放行。
// 错误信息中的销毁时间沿用文件记录的时区表示（含偏移量，便于定位），
// 比较本身只按实际时刻进行。
func TestOpenRejectsDestroyedBeforeHistoryAcrossZones(t *testing.T) {
	path := writeDestroyTimeScenario(t, `"at":"2026-10-02T19:00:00+08:00"`, "")
	mustRejectOpen(t, path, "S-001",
		"2026-10-02T19:00:00+08:00", "2026-10-02T12:00:00Z")
}

// TestOpenRejectsDestroyedOneSecondBeforeLatestHistory 边界严格一侧：
// 早于最晚历史 1 秒也拒绝。
func TestOpenRejectsDestroyedOneSecondBeforeLatestHistory(t *testing.T) {
	path := writeDestroyTimeScenario(t, `"at":"2026-10-02T11:59:59Z"`, "")
	mustRejectOpen(t, path, "S-001",
		"2026-10-02T11:59:59Z", "2026-10-02T12:00:00Z")
}

// TestOpenRestoresDestroyedAtLatestHistoryInstantAcrossZones 边界合法
// 一侧：销毁时间恰好等于自身最晚历史时刻（12:00 接收），用 UTC+8 表示
// （当地 20:00）应正常恢复；恢复后历史原顺序、原时间保留，销毁信息与
// 数量仍按原规则核对并可查询。
func TestOpenRestoresDestroyedAtLatestHistoryInstantAcrossZones(t *testing.T) {
	path := writeDestroyTimeScenario(t, `"at":"2026-10-02T20:00:00+08:00"`, "")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("销毁时间等于最晚历史时刻（跨时区表示）应正常恢复: %v", err)
	}
	p, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	latest := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	if p.Destruction == nil || !p.Destruction.At.Equal(latest) ||
		p.Destruction.Qty != "6.750" || p.Remaining != "0.000" {
		t.Fatalf("销毁信息与数量应原样恢复: %+v", p)
	}
	// 历史追加次序与各条时间原样保留：末尾仍是时间更早的分装事件，
	// 不能因时间核对而重排或改写。
	wantKinds := []string{"register", "transfer-out", "transfer-in", "split"}
	wantTimes := []time.Time{
		time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC),
		latest,
		time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC),
	}
	if len(p.History) != len(wantKinds) {
		t.Fatalf("历史条数不应变化: %+v", p.History)
	}
	for i, k := range wantKinds {
		if p.History[i].Kind != k || !p.History[i].Time.Equal(wantTimes[i]) {
			t.Fatalf("第 %d 条历史的种类/时间被改写: %+v", i, p.History)
		}
	}
}

// TestOpenRejectsMissingDestroyedAt 销毁信息中没有 at 字段时拒绝；即使
// 数量守恒（6.750 + 3.250 = 10.000）也不能恢复。
func TestOpenRejectsMissingDestroyedAt(t *testing.T) {
	// 把 ,__AT__, 整体替换成单个逗号，形成没有 at 字段、仍合法的 JSON。
	content := strings.Replace(openDestroyTimeScenario, `,__AT__,`, `,`, 1)
	content = strings.Replace(content, "__QTY__", "6750", 1)
	path := writeRawLedger(t, content)
	mustRejectOpen(t, path, "S-001", "销毁时间", "缺失")
}

// TestOpenRejectsNullDestroyedAt at 显式为 null 与字段缺失同样报缺失。
func TestOpenRejectsNullDestroyedAt(t *testing.T) {
	path := writeDestroyTimeScenario(t, `"at":null`, "")
	mustRejectOpen(t, path, "S-001", "销毁时间", "缺失")
}

// TestOpenRejectsZeroDestroyedAt at 明确是零值时刻时报“零值”，与缺失
// 区分；零值必然早于历史，但仍应报零值而不是时间倒置。
func TestOpenRejectsZeroDestroyedAt(t *testing.T) {
	path := writeDestroyTimeScenario(t, `"at":"0001-01-01T00:00:00Z"`, "")
	mustRejectOpen(t, path, "S-001", "销毁时间", "零值")
}

// TestOpenRejectsMissingOrZeroDestroyedAtWithoutHistory 销毁时间缺失或
// 为零都应拒绝，即使该样品一条保管历史都没有。
func TestOpenRejectsMissingOrZeroDestroyedAtWithoutHistory(t *testing.T) {
	base := func(atJSON string) string {
		return `{
  "version": 1,
  "samples": {
    "X": {"id":"X","initial":1000,"remaining":0,"holder":"h","location":"l","children":[],"history":[],
      "destroyed":{"operator":"h","location":"l",` + atJSON + `,"reason":"r","qty":1000}}
  },
  "transfers": {}
}`
	}
	t.Run("缺失且无历史", func(t *testing.T) {
		path := writeRawLedger(t, base(`"at":null`))
		mustRejectOpen(t, path, "X", "销毁时间", "缺失")
	})
	t.Run("零值且无历史", func(t *testing.T) {
		path := writeRawLedger(t, base(`"at":"0001-01-01T00:00:00Z"`))
		mustRejectOpen(t, path, "X", "销毁时间", "零值")
	})
}

// TestOpenDestroyedTimeCheckUsesOwnHistoryOnly 核对只看该编号样品自己
// 的历史：
//   - 原样 P 在 12:30 销毁（不早于自身最晚历史 12:00），其已分出子样 C
//     后来在 14:00/15:00 交接，子样更晚的记录不推迟 P 的可销毁时间；
//   - 子样 C 自己销毁时按 C 自己的历史判断：11:00 销毁只晚于 C 的
//     10:00 分装即可，父样 P 在 12:00 的接收不参与 C 的核对。
func TestOpenDestroyedTimeCheckUsesOwnHistoryOnly(t *testing.T) {
	// 场景一：父样销毁早于子样后来的交接，恢复合法。
	parentDestroyed := destroyedSample("P", 10000, 0, 6750, "C")
	parentDestroyed.History = []historyRecord{
		{Kind: "register", Time: time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC), Holder: "李四", Location: "实验室B"},
		{Kind: "split", Time: time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC), Holder: "李四", Location: "实验室B"},
	}
	childLater := plainChild("C", "P", 3250, 3250)
	childLater.History = append(childLater.History,
		historyRecord{Kind: "transfer-out", Time: time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC), Holder: "李四", Location: "实验室B"},
		historyRecord{Kind: "transfer-in", Time: time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC), Holder: "王五", Location: "实验室C"},
	)
	l1 := &ledger{Samples: map[string]*sampleRecord{
		"P": parentDestroyed,
		"C": childLater,
	}}
	s1, err := Open(writeStructLedger(t, l1))
	if err != nil {
		t.Fatalf("父样销毁只须不早于自身历史，子样更晚交接不应影响: %v", err)
	}
	p, _ := s1.GetSample("P")
	if p.Destruction == nil || !p.Destruction.At.Equal(destroyedCheckAt) {
		t.Fatalf("父样销毁信息应恢复: %+v", p.Destruction)
	}

	// 场景二：子样销毁早于父样后来的接收，但不早于子样自身最晚历史，
	// 按子样自己的历史判断，恢复合法。
	parentActive := &sampleRecord{
		ID: "P2", Initial: 10000, Remaining: 6000,
		Holder: "李四", Location: "实验室B", Children: []string{"C2"},
		History: []historyRecord{
			{Kind: "register", Time: time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC), Holder: "李四", Location: "实验室B"},
			// 父样 12:00 才完成接收，晚于子样的销毁。
			{Kind: "transfer-in", Time: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC), Holder: "李四", Location: "实验室B"},
		},
	}
	childDestroyed := &sampleRecord{
		ID: "C2", ParentID: "P2", Initial: 4000, Remaining: 0,
		Holder: "李四", Location: "实验室B", Children: []string{},
		History: []historyRecord{
			{Kind: "split", Time: time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC), Holder: "李四", Location: "实验室B"},
			{Kind: "destroy", Time: time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC), Holder: "李四", Location: "实验室B"},
		},
		Destroyed: &destructionRecord{
			Operator: "李四", Location: "实验室B",
			At:     time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC),
			Reason: "子样用完销毁", Qty: 4000,
		},
	}
	l2 := &ledger{Samples: map[string]*sampleRecord{
		"P2": parentActive,
		"C2": childDestroyed,
	}}
	s2, err := Open(writeStructLedger(t, l2))
	if err != nil {
		t.Fatalf("子样销毁应按自身历史判断，父样更晚的接收不应影响: %v", err)
	}
	c, _ := s2.GetSample("C2")
	if c.Destruction == nil || c.Destruction.Qty != "4.000" {
		t.Fatalf("子样销毁信息应独立恢复: %+v", c.Destruction)
	}
}

// TestOpenDestroyedTimeFailureRejectsWholeFile 一份销毁时间不成立时整份
// 文件打开失败，不能只加载其他正常样品；错误是 ErrInvalid 且原文件不被
// 改写（mustRejectOpen 已断言 nil Store 与文件不变）。
func TestOpenDestroyedTimeFailureRejectsWholeFile(t *testing.T) {
	ok := destroyedSample("OK", 5000, 0, 5000)
	bad := destroyedSample("BAD", 10000, 0, 6750, "BAD-C")
	// BAD 的接收历史晚于其销毁时间，且分装历史追加在接收之后：
	// 销毁 10-03 12:00，历史里加入 10-04 的接收。
	bad.History = append(bad.History, historyRecord{
		Kind: "transfer-in", Time: time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC),
		Holder: "李四", Location: "实验室B",
	})
	l := &ledger{Samples: map[string]*sampleRecord{
		"OK":    ok,
		"BAD":   bad,
		"BAD-C": plainChild("BAD-C", "BAD", 3250, 3250),
	}}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "BAD",
		"2026-10-03T12:00:00Z", "2026-10-04T09:00:00Z")

	// 失败后再次打开仍是同样的失败，绝不会部分加载出 OK。
	s, err := Open(path)
	if err == nil {
		t.Fatalf("损坏文件再次打开仍应失败")
	}
	if s != nil {
		t.Fatalf("失败时不应返回 Store")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("应为 ErrInvalid, got %v", err)
	}
}
