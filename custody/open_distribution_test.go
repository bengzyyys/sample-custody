package custody

import (
	"math"
	"testing"
	"time"
)

// 重新打开数据时，每份未销毁样品除自身初始量/剩余量合法外，剩余量加上已经
// 直接分出的总量也不得超过它自身的初始量。直接分出总量按子样记录中的来源
// 编号认定，每份只计子样创建时取得的初始量；子样后来继续分装、交接或销毁
// 不减少来源样品当时分出的量，孙样不计入祖父样。这些测试构造公开操作无法
// 产生的损坏记录，验证 Open 一律拒绝、整份文件不加载、原文件不改写，且
// 错误可用 errors.Is 判定为 ErrInvalid。

// withChild 给样品记录挂上子样列表（仅用于来源关系展示；核对以来源编号
// 为准，漏列也不会少算）。
func withChild(s *sampleRecord, childIDs ...string) *sampleRecord {
	s.Children = append([]string{}, childIDs...)
	return s
}

// TestOpenRejectsRemainingPlusDirectChildrenAboveInitial 任务示例：原样初始
// 10.000、剩余 7.000，另有一份来源指向它、初始 4.000 的子样，7.000+
// 4.000=11.000 超出初始量，即使各自记录都合法也必须拒绝打开，不能让多出
// 的量继续参与分装。
func TestOpenRejectsRemainingPlusDirectChildrenAboveInitial(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":   withChild(activeSample("S-001", "", 10000, 7000), "S-001-A"),
			"S-001-A": plainChild("S-001-A", "S-001", 4000, 4000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "S-001", "10.000", "7.000", "4.000", "11.000", "直接分出")
}

// TestOpenRejectsOverrunForSplitChildToo 上限同样适用于分装子样：子样 C
// 自身初始 4.000，剩余 2.000，却又直接分出 3.500 给孙样 G，合计 5.500
// 超过 C 自身初始量 4.000，必须拒绝；父样 P 与孙样 G 的数量本身合法也不
// 能放行。
func TestOpenRejectsOverrunForSplitChildToo(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChild(activeSample("P", "", 10000, 6000), "C"),
			"C": withChild(plainChild("C", "P", 4000, 2000), "G"),
			"G": plainChild("G", "C", 3500, 3500),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "C", "4.000", "2.000", "3.500", "5.500")
}

// TestOpenRejectsOverrunEvenWhenChildMissingFromList 直接子样按来源编号
// 认定：子样 C 的来源指向 P，但 P 的子样列表漏列 C，分出量仍必须计入；
// P 剩余 7.000 加 C 的初始 4.000 超过 10.000 时不能因漏列而少算。
func TestOpenRejectsOverrunEvenWhenChildMissingFromList(t *testing.T) {
	parent := activeSample("P", "", 10000, 7000)
	parent.Children = []string{}
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": parent,
			"C": plainChild("C", "P", 4000, 4000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "P", "10.000", "7.000", "4.000")
}

// TestOpenRejectsOverrunWithMultipleDirectChildren 多份直接子样按创建时
// 初始量累加：4.000 与 3.500 合计 7.500，加剩余 3.000 为 10.500，超过
// 初始 10.000。
func TestOpenRejectsOverrunWithMultipleDirectChildren(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P":  withChild(activeSample("P", "", 10000, 3000), "C1", "C2"),
			"C1": plainChild("C1", "P", 4000, 4000),
			"C2": plainChild("C2", "P", 3500, 3500),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "P", "10.000", "3.000", "7.500", "10.500")
}

// TestOpenRejectsOverrunWithZeroRemainingNoDestruction 零剩余量且没有销毁
// 信息仍视为未销毁，照样受上限约束：剩余 0.000 但直接分出 11.000 超过
// 初始 10.000 时拒绝，且不补造销毁记录。
func TestOpenRejectsOverrunWithZeroRemainingNoDestruction(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P":  withChild(activeSample("P", "", 10000, 0), "C1", "C2"),
			"C1": plainChild("C1", "P", 4000, 4000),
			"C2": plainChild("C2", "P", 7000, 7000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "P", "10.000", "0.000", "11.000")
}

// TestOpenRejectsDistributionOverrunEvenWhenPendingTransferMatches 样品挂有
// 待确认交接时仍要遵守上限：交接量 7.000 恰好等于错误记录的剩余量，不能
// 让 7.000+4.000 超量的文件通过。
func TestOpenRejectsDistributionOverrunEvenWhenPendingTransferMatches(t *testing.T) {
	parent := withChild(activeSample("P", "", 10000, 7000), "C")
	parent.PendingID = "T1"
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": parent,
			"C": plainChild("C", "P", 4000, 4000),
		},
		Transfers: map[string]*transferRecord{
			"T1": {
				ID: "T1", SampleID: "P",
				FromHolder: "h", FromLocation: "l",
				ToHolder: "x", ToLocation: "y",
				Qty:          7000,
				HandedOverAt: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC),
				Confirmed:    false,
			},
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "P", "10.000", "7.000", "4.000")
}

// TestOpenActiveDistributionCheckIsWholeFileFailure 任一未销毁样品超量时
// 整份文件打开失败，不能只加载其他正常样品。
func TestOpenActiveDistributionCheckIsWholeFileFailure(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"OK": activeSample("OK", "", 1000, 1000),
			"P":  withChild(activeSample("P", "", 10000, 7000), "C"),
			"C":  plainChild("C", "P", 4000, 4000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "P", "11.000")
}

// TestOpenAcceptsRemainingPlusChildrenAtExactInitial 任务给出的合法对照：
// 初始 10.000、剩余 6.000、直接子样初始 4.000，合计恰好等于初始量，必须
// 正常打开。
func TestOpenAcceptsRemainingPlusChildrenAtExactInitial(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":   withChild(activeSample("S-001", "", 10000, 6000), "S-001-A"),
			"S-001-A": plainChild("S-001-A", "S-001", 4000, 4000),
		},
	}
	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("剩余量 6.000 + 直接分出 4.000 = 初始 10.000 应正常恢复: %v", err)
	}
	p, _ := s.GetSample("S-001")
	if p.InitialQty != "10.000" || p.Remaining != "6.000" || p.Destruction != nil {
		t.Fatalf("数量与未销毁状态应原样保留: %+v", p)
	}
}

// TestOpenAcceptsRemainingPlusChildrenBelowInitial 旧数据接受约定不变：
// 合计小于初始量（5.000+4.000=9.000 < 10.000）仍可恢复，不要求补齐差额。
func TestOpenAcceptsRemainingPlusChildrenBelowInitial(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChild(activeSample("P", "", 10000, 5000), "C"),
			"C": plainChild("C", "P", 4000, 4000),
		},
	}
	if _, err := Open(writeStructLedger(t, l)); err != nil {
		t.Fatalf("合计小于初始量的旧数据应按原约定恢复，不补齐差额: %v", err)
	}
}

// TestOpenDistributionCountUsesChildInitialOnly 统计只取各直接子样创建时
// 的初始量：
//   - 子样后来继续分装、当前只剩 1.000，来源样品仍按子样初始 4.000 核对，
//     6.000+4.000=10.000 合法，不能改用子样当前剩余量；
//   - 子样后来已独立销毁，计入来源核对的仍是它创建时的 4.000；
//   - 孙样 G 由 C 分出，不计入祖父样 P 的合计。
func TestOpenDistributionCountUsesChildInitialOnly(t *testing.T) {
	// 场景一：子样自身又分出 3.000，当前只剩 1.000；父样合计仍按 4.000。
	childSplit := plainChild("C", "P", 4000, 1000)
	childSplit.Children = []string{"G"}
	childSplit.History = append(childSplit.History, historyRecord{
		Kind: "split", Time: splitCheckAt.Add(time.Hour), Holder: "李四", Location: "实验室B",
	})
	good := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChild(activeSample("P", "", 10000, 6000), "C"),
			"C": childSplit,
			"G": plainChild("G", "C", 3000, 3000),
		},
	}
	if _, err := Open(writeStructLedger(t, good)); err != nil {
		t.Fatalf("子样当前只剩 1.000 时仍按其创建初始量 4.000 核对，6.000+4.000=10.000 应恢复: %v", err)
	}

	// 场景二：子样已独立销毁全部 4.000，父样仍按 4.000 计入。
	childDestroyed := destroyedSampleWithParent("C", "P", 4000, 4000)
	destroyedLedger := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChild(activeSample("P", "", 10000, 6000), "C"),
			"C": childDestroyed,
		},
	}
	s2, err := Open(writeStructLedger(t, destroyedLedger))
	if err != nil {
		t.Fatalf("子样后来销毁不减少来源样品已分出的量，文件应正常恢复: %v", err)
	}
	child, _ := s2.GetSample("C")
	if child.Destruction == nil || child.InitialQty != "4.000" {
		t.Fatalf("子样销毁记录应独立保留，创建时初始量仍为 4.000: %+v", child)
	}

	// 反向断言：同样的父样剩余 7.000 时，即使子样当前只剩 1.000 也必须
	// 按 4.000 计入并拒绝（不能因子样量“变小”而放行）。
	bad := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChild(activeSample("P", "", 10000, 7000), "C"),
			"C": childSplit,
			"G": plainChild("G", "C", 3000, 3000),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, bad), "P", "10.000", "7.000", "4.000")
}

// TestOpenDistributionIgnoresGrandchildrenAndOtherRoots 祖父样的核对只认
// 来源编号直接指向它的子样：孙样 G（来源为 C）与另一份原样 R、R 的子样
// 都不计入 P 的合计。
func TestOpenDistributionIgnoresGrandchildrenAndOtherRoots(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			// P: 6.000 + 直接子样 C 初始 4.000 = 10.000 合法；若错误地把
			// 孙样 G 的 4.000 或 R-D 计入就会得到 14.000/11.000 而误拒。
			"P":   withChild(activeSample("P", "", 10000, 6000), "C"),
			"C":   withChild(plainChild("C", "P", 4000, 0), "G"),
			"G":   plainChild("G", "C", 4000, 4000),
			"R":   withChild(activeSample("R", "", 8000, 1000), "R-D"),
			"R-D": plainChild("R-D", "R", 7000, 7000),
		},
	}
	if _, err := Open(writeStructLedger(t, l)); err != nil {
		t.Fatalf("孙样、其他原样及其子样都不应参与 P 的核对: %v", err)
	}
}

// TestOpenAcceptsUnlistedChildWhenTotalsConsistent 子样列表漏列时，未销毁
// 样品不做来源关系一致性拒绝，但漏列的子样仍按来源编号计入：合计不超量
// 时文件照常恢复（关系列表原样保留，不替它补列）。
func TestOpenAcceptsUnlistedChildWhenTotalsConsistent(t *testing.T) {
	parent := activeSample("P", "", 10000, 6000)
	parent.Children = []string{}
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": parent,
			"C": plainChild("C", "P", 4000, 4000),
		},
	}
	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("漏列子样但合计恰好等于初始量时应正常恢复: %v", err)
	}
	p, _ := s.GetSample("P")
	if len(p.Children) != 0 {
		t.Fatalf("恢复流程不得替旧数据补列子样: %v", p.Children)
	}
}

// TestOpenActiveDistributionLargeBounds 大数量边界：上限附近的合法合计
// （剩余 0.001 + 直接子样 上限-0.001 = 上限）必须逐位准确接受；合计超出
// int64 支持范围（剩余取上限、另有正量子样）时明确按超出支持范围拒绝，
// 不能接受回绕后的结果。
func TestOpenActiveDistributionLargeBounds(t *testing.T) {
	const (
		maxUnits   int64 = math.MaxInt64
		belowUnits int64 = math.MaxInt64 - 1
	)
	legal := &ledger{
		Samples: map[string]*sampleRecord{
			"BIG": withChild(activeSample("BIG", "", maxUnits, 1), "BIG-C"),
			"BIG-C": func() *sampleRecord {
				c := plainChild("BIG-C", "BIG", belowUnits, belowUnits)
				return c
			}(),
		},
	}
	s, err := Open(writeStructLedger(t, legal))
	if err != nil {
		t.Fatalf("上限附近的合法合计应准确恢复: %v", err)
	}
	big, _ := s.GetSample("BIG")
	if big.InitialQty != maxQtyString || big.Remaining != "0.001" {
		t.Fatalf("上限父样恢复不精确: %+v", big)
	}
	bigC, _ := s.GetSample("BIG-C")
	if bigC.InitialQty != belowMaxString {
		t.Fatalf("上限附近子样恢复不精确: %s", bigC.InitialQty)
	}

	overflow := &ledger{
		Samples: map[string]*sampleRecord{
			"BIG":   withChild(activeSample("BIG", "", maxUnits, maxUnits), "BIG-C"),
			"BIG-C": plainChild("BIG-C", "BIG", 1, 1),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, overflow), "BIG", "超出可表示范围")
}
