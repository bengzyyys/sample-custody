package custody

import (
	"math"
	"testing"
	"time"
)

// 重新打开数据时，每份未销毁样品（原样与分装子样）的自身数量必须自洽：
// 初始量大于零，剩余量可为零但不能为负、也不能超过自身初始量。这些测试
// 构造公开操作无法产生的损坏记录，验证 Open 一律拒绝、整份文件不加载、
// 原文件不改写，且错误可用 errors.Is 判定为 ErrInvalid。

// activeSample 构造一份未销毁样品记录，数量单位均为千分之一毫升。
func activeSample(id string, parent string, initial, remaining int64) *sampleRecord {
	s := &sampleRecord{
		ID:        id,
		ParentID:  parent,
		Initial:   initial,
		Remaining: remaining,
		Holder:    "h",
		Location:  "l",
		Children:  []string{},
		History: []historyRecord{{
			Kind: "register", Time: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
			Holder: "h", Location: "l",
		}},
	}
	if parent != "" {
		s.History[0].Kind = "split"
	}
	return s
}

// TestOpenRejectsRemainingAboveInitial 任务示例：初始量 10.000 毫升，剩余量
// 却被写成 12.000 毫升。没有销毁信息也必须拒绝，不能让多出的剩余量继续
// 参与分装。
func TestOpenRejectsRemainingAboveInitial(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001": activeSample("S-001", "", 10000, 12000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "S-001", "12.000", "10.000", "超过自身初始量")
}

// TestOpenRejectsChildRemainingAboveOwnInitial 子样的上限是它自己创建时取
// 得的初始量：子样自身初始 3.000、剩余 3.500 时拒绝，不能借用父样的量。
func TestOpenRejectsChildRemainingAboveOwnInitial(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": activeSample("P", "", 10000, 7000),
			"C": plainChild("C", "P", 3000, 3500),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "C", "3.500", "3.000", "超过自身初始量")
}

// TestOpenRejectsNegativeRemaining 未销毁样品剩余量为负必须拒绝，错误以
// 三位小数毫升展示负值。
func TestOpenRejectsNegativeRemaining(t *testing.T) {
	cases := []struct {
		name      string
		remaining int64
		text      string
	}{
		{"负一个最小刻度", -1, "-0.001"},
		{"负一毫升", -1000, "-1.000"},
		{"极端负数量", math.MinInt64, "-9223372036854775.808"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := &ledger{
				Samples: map[string]*sampleRecord{
					"S1": activeSample("S1", "", 10000, c.remaining),
				},
			}
			path := writeStructLedger(t, l)
			mustRejectOpen(t, path, "S1", c.text, "10.000", "不能为负")
		})
	}
}

// TestOpenRejectsNonPositiveInitialOnActiveSample 未销毁样品初始量必须大于
// 零，零初始量与负初始量都要拒绝（原样、子样一致）。
func TestOpenRejectsNonPositiveInitialOnActiveSample(t *testing.T) {
	cases := []struct {
		name    string
		parent  string
		initial int64
		text    string
	}{
		{"原样初始量为零", "", 0, "0.000"},
		{"原样初始量为负", "", -5000, "-5.000"},
		{"子样初始量为零", "P", 0, "0.000"},
		{"子样初始量为负", "P", -1, "-0.001"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			samples := map[string]*sampleRecord{
				"BAD": activeSample("BAD", c.parent, c.initial, 0),
			}
			if c.parent != "" {
				samples["P"] = activeSample("P", "", 10000, 10000)
			}
			l := &ledger{Samples: samples}
			path := writeStructLedger(t, l)
			mustRejectOpen(t, path, "BAD", c.text, "初始量", "大于零")
		})
	}
}

// TestOpenRejectsOverrunEvenWhenPendingTransferMatches 待确认交接不影响数量
// 检查：交接量、交出人、地点都与错误的剩余量 12.000 一致时，数量越界仍
// 必须先按数量规则拒绝，不能被交接关联检查接受。
func TestOpenRejectsOverrunEvenWhenPendingTransferMatches(t *testing.T) {
	sample := activeSample("S1", "", 10000, 12000)
	sample.PendingID = "T1"
	l := &ledger{
		Samples: map[string]*sampleRecord{"S1": sample},
		Transfers: map[string]*transferRecord{
			"T1": {
				ID: "T1", SampleID: "S1",
				FromHolder: "h", FromLocation: "l",
				ToHolder: "x", ToLocation: "y",
				Qty:          12000,
				HandedOverAt: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC),
				Confirmed:    false,
			},
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "S1", "12.000", "10.000", "超过自身初始量")
}

// TestOpenActiveQuantityCheckIsWholeFileFailure 一份记录数量越界时整份文件
// 打开失败，不能只加载其他正常样品。
func TestOpenActiveQuantityCheckIsWholeFileFailure(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"OK":  activeSample("OK", "", 1000, 1000),
			"BAD": activeSample("BAD", "", 1000, 2000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "BAD", "2.000", "1.000")
}

// TestOpenAcceptsActiveQuantityBoundaries 数量边界的合法记录不能被误拒：
// 剩余量等于初始量（满量）、等于零（旧数据分装用尽、无销毁信息、无子样）
// 都按原约定加载；支持上限附近的大数量也逐位准确。
func TestOpenAcceptsActiveQuantityBoundaries(t *testing.T) {
	cases := []struct {
		name              string
		id                string
		initial, remain   int64
		wantInit, wantRem string
	}{
		{"满量", "FULL", 10000, 10000, "10.000", "10.000"},
		{"零剩余量旧记录", "ZERO", 1000, 0, "1.000", "0.000"},
		{"最小正数量", "MIN", 1, 1, "0.001", "0.001"},
		{"上限附近", "BIG", math.MaxInt64, math.MaxInt64, maxQtyString, maxQtyString},
		{"上限附近零剩余", "BIG0", math.MaxInt64, 0, maxQtyString, "0.000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := &ledger{
				Samples: map[string]*sampleRecord{
					c.id: activeSample(c.id, "", c.initial, c.remain),
				},
			}
			s, err := Open(writeStructLedger(t, l))
			if err != nil {
				t.Fatalf("数量在合法边界内应正常恢复: %v", err)
			}
			got, err := s.GetSample(c.id)
			if err != nil {
				t.Fatal(err)
			}
			if got.InitialQty != c.wantInit || got.Remaining != c.wantRem {
				t.Fatalf("数量恢复错误: init=%s want=%s remaining=%s want=%s",
					got.InitialQty, c.wantInit, got.Remaining, c.wantRem)
			}
			if got.Destruction != nil {
				t.Fatalf("无销毁信息的记录不能补出销毁记录: %+v", got.Destruction)
			}
		})
	}
}

// TestFormatUnitsHandlesNegatives 负数数量只出现在损坏数据的错误说明中，
// 必须稳定渲染，尤其 math.MinInt64 不能因取负回绕而异常或不返回。
func TestFormatUnitsHandlesNegatives(t *testing.T) {
	cases := []struct {
		units int64
		want  string
	}{
		{0, "0.000"},
		{1, "0.001"},
		{1000, "1.000"},
		{-1, "-0.001"},
		{-1000, "-1.000"},
		{-1001, "-1.001"},
		{-1234, "-1.234"},
		{math.MaxInt64, "9223372036854775.807"},
		{math.MinInt64, "-9223372036854775.808"},
	}
	for _, c := range cases {
		if got := formatUnits(c.units); got != c.want {
			t.Fatalf("formatUnits(%d) = %q, want %q", c.units, got, c.want)
		}
	}
}
