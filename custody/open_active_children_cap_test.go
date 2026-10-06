package custody

import (
	"math"
	"testing"
	"time"
)

// 重新打开数据时，每份未销毁样品（原样与分装子样）除了自身初始量、剩余量
// 合法外，还必须满足：剩余量加上直接分出的总量不得超过自身初始量。直接子样
// 按子样记录中的来源编号认定，每份只计创建时取得的初始量；子样后来继续
// 分装、交接或销毁都不减少来源样品当时分出的量，孙样不计入祖父样。这些
// 测试构造公开操作无法产生的损坏记录，验证 Open 一律拒绝、整份文件不加载、
// 原文件不改写，且错误可用 errors.Is 判定为 ErrInvalid。

// TestOpenRejectsRemainingPlusDirectChildrenOverInitial 任务示例：原样初始
// 10.000、剩余 7.000，另有一份来源指向它、初始 4.000 的子样，其他记录均
// 合法时旧逻辑仍会接受文件；7.000+4.000=11.000 已超过自身初始量，必须
// 拒绝，不能让多出的量继续参与分装。错误信息写明样品编号，并以三位小数
// 毫升展示初始量、剩余量与直接分出总量。
func TestOpenRejectsRemainingPlusDirectChildrenOverInitial(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":   withChildren(activeSample("S-001", "", 10000, 7000), "S-001-A"),
			"S-001-A": plainChild("S-001-A", "S-001", 4000, 4000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "S-001", "10.000", "7.000", "4.000", "11.000")
}

// TestOpenRejectsSplitChildRemainingPlusGrandchildrenOverOwnInitial 上限同样
// 适用于分装子样：子样 C 自身初始 4.000、剩余 3.000，却又有一份来源指向
// 它、初始 2.000 的孙样（3.000+2.000=5.000>4.000）。不能借用祖父样 P 的
// 初始量，必须以 C 自己创建时取得的量为上限拒绝。
func TestOpenRejectsSplitChildRemainingPlusGrandchildrenOverOwnInitial(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			// 祖父样自身 6.000+4.000=10.000 合法，不能替子样承担超额。
			"P": withChildren(activeSample("P", "", 10000, 6000), "C"),
			"C": withChildren(plainChild("C", "P", 4000, 3000), "G"),
			"G": plainChild("G", "C", 2000, 2000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "C", "4.000", "3.000", "2.000", "5.000")
}

// TestOpenCountsDirectChildEvenWhenParentChildrenListOmitsIt 直接子样按来源
// 编号认定：父样子样列表漏列真正的直接子样，也不能少算它创建时取得的量。
func TestOpenCountsDirectChildEvenWhenParentChildrenListOmitsIt(t *testing.T) {
	parent := activeSample("S-001", "", 10000, 7000)
	parent.Children = []string{} // 漏列 S-001-A
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":   parent,
			"S-001-A": plainChild("S-001-A", "S-001", 4000, 4000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "S-001", "7.000", "4.000", "11.000")
}

// TestOpenDoesNotCountChildrenByListingOnly 核对以子样的来源编号为准：父样
// 列表里列入一份来源不指向它的原样，不能把该原样的量计入直接分出总量
// （满量 10.000 的父样若被误计 1.000 就会错误超限）。
func TestOpenDoesNotCountChildrenByListingOnly(t *testing.T) {
	parent := withChildren(activeSample("P", "", 10000, 10000), "R")
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": parent,
			"R": activeSample("R", "", 1000, 1000), // 原样，ParentID 为空
		},
	}
	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("来源不指向本样品的编号不能计入直接分出总量: %v", err)
	}
	p, _ := s.GetSample("P")
	if p.Remaining != "10.000" {
		t.Fatalf("满量父样应原样恢复: %s", p.Remaining)
	}
}

// TestOpenCountsEachDirectChildOnlyOnceRegardlessOfListing 同一子样在父样的
// 子样列表中重复列入，也只能按来源关系计入一次：剩余 6.000 + 子样初始
// 4.000 = 10.000 恰好等于初始量，不能因重复列入算成 14.000 而误拒。
func TestOpenCountsEachDirectChildOnlyOnceRegardlessOfListing(t *testing.T) {
	parent := activeSample("P", "", 10000, 6000)
	parent.Children = []string{"C", "C"}
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": parent,
			"C": plainChild("C", "P", 4000, 4000),
		},
	}
	if _, err := Open(writeStructLedger(t, l)); err != nil {
		t.Fatalf("直接分出量按来源编号每份只计一次，6.000+4.000=10.000 应恢复: %v", err)
	}
}

// TestOpenGrandchildrenNotCountedIntoGrandparent 孙样不计入祖父样：子样 C
// 初始 4.000 并已全部分给孙样 G（C 未销毁、剩余 0.000），祖父样 P 剩余
// 6.000 只与直接子样 C 的初始量 4.000 合计，6.000+4.000=10.000 合法；
// 不能把 G 的 4.000 再计入 P。
func TestOpenGrandchildrenNotCountedIntoGrandparent(t *testing.T) {
	childExhausted := plainChild("C", "P", 4000, 0)
	childExhausted.Children = []string{"G"}
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChildren(activeSample("P", "", 10000, 6000), "C"),
			"C": childExhausted,
			"G": plainChild("G", "C", 4000, 4000),
		},
	}
	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("孙样不得计入祖父样，6.000+4.000=10.000 应恢复: %v", err)
	}
	p, _ := s.GetSample("P")
	if p.Remaining != "6.000" || len(p.Children) != 1 {
		t.Fatalf("祖父样数量与子样列表应原样保留: %+v", p)
	}
}

// TestOpenDirectChildLaterChangesDoNotReduceParentSplitOut 来源样品当时分出
// 的量不因子样后来的变化而减少：任务给出的合法边界（初始 10.000、剩余
// 6.000、直接子样初始 4.000）即使子样后来只剩 1.000（又分出 3.000）或已
// 独立销毁，父样仍按子样创建时的 4.000 核对，正常打开。
func TestOpenDirectChildLaterChangesDoNotReduceParentSplitOut(t *testing.T) {
	// 场景一：子样继续分装，当前只剩 1.000。
	childSplit := plainChild("C", "P", 4000, 1000)
	childSplit.Children = []string{"G"}
	l1 := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChildren(activeSample("P", "", 10000, 6000), "C"),
			"C": childSplit,
			"G": plainChild("G", "C", 3000, 3000),
		},
	}
	if _, err := Open(writeStructLedger(t, l1)); err != nil {
		t.Fatalf("子样后来分装到只剩 1.000，父样仍按 4.000 核对，应正常恢复: %v", err)
	}

	// 场景二：子样已独立销毁（销毁当时全部 4.000），父样仍按其初始量
	// 4.000 计入，6.000+4.000=10.000 合法。
	l2 := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChildren(activeSample("P", "", 10000, 6000), "C"),
			"C": destroyedSampleWithParent("C", "P", 4000, 4000),
		},
	}
	if _, err := Open(writeStructLedger(t, l2)); err != nil {
		t.Fatalf("子样后来销毁不减少父样当时分出的量，文件应正常恢复: %v", err)
	}
}

// TestOpenSiblingsAndOtherRootsExcludedFromCap 兄弟子样与其他原样不参与本次
// 核对：P 剩余 6.000 加自己唯一的直接子样 C 的 4.000 恰好等于初始量；
// P2 与其子样 D 的数量不能被算进 P 的合计。
func TestOpenSiblingsAndOtherRootsExcludedFromCap(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P":  withChildren(activeSample("P", "", 10000, 6000), "C"),
			"C":  plainChild("C", "P", 4000, 4000),
			"P2": withChildren(activeSample("P2", "", 8000, 0), "D"),
			"D":  plainChild("D", "P2", 8000, 8000),
		},
	}
	if _, err := Open(writeStructLedger(t, l)); err != nil {
		t.Fatalf("其他原样与兄弟子样不应参与本次合计: %v", err)
	}
}

// TestOpenAcceptsRemainingPlusChildrenBelowInitial 保持旧数据接受约定：合计
// 小于自身初始量仍可恢复，不要求强行补齐差额。
func TestOpenAcceptsRemainingPlusChildrenBelowInitial(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":   withChildren(activeSample("S-001", "", 10000, 5000), "S-001-A"),
			"S-001-A": plainChild("S-001-A", "S-001", 4000, 4000),
		},
	}
	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("5.000+4.000=9.000 小于初始量 10.000 的旧数据应正常恢复: %v", err)
	}
	got, _ := s.GetSample("S-001")
	if got.Remaining != "5.000" || got.Destruction != nil {
		t.Fatalf("不得补齐差额或补造销毁记录: %+v", got)
	}
}

// TestOpenZeroRemainingActiveSampleStillSubjectToCap 零剩余量且没有销毁信息
// 仍视为未销毁，不补造销毁记录，但同样要遵守上限：直接子样初始量超过自身
// 初始量时拒绝；恰好相等（分装用尽）正常恢复。
func TestOpenZeroRemainingActiveSampleStillSubjectToCap(t *testing.T) {
	over := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChildren(activeSample("P", "", 10000, 0), "C"),
			"C": plainChild("C", "P", 10001, 10001),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, over), "P", "0.000", "10.001", "10.000")

	exact := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChildren(activeSample("P", "", 10000, 0), "C"),
			"C": plainChild("C", "P", 10000, 10000),
		},
	}
	s, err := Open(writeStructLedger(t, exact))
	if err != nil {
		t.Fatalf("零剩余量且直接分出总量恰好等于初始量应正常恢复: %v", err)
	}
	p, _ := s.GetSample("P")
	if p.Remaining != "0.000" || p.Destruction != nil {
		t.Fatalf("分装用尽约定不变，不能补造销毁记录: %+v", p)
	}
}

// TestOpenActiveCapAtMilliliterThousandthBoundary 按 0.001 毫升精度核对：
// 上限附近的合法合计必须准确接受，超出一个最小刻度即拒绝。
func TestOpenActiveCapAtMilliliterThousandthBoundary(t *testing.T) {
	legal := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChildren(activeSample("P", "", 1000, 600), "C"),
			"C": plainChild("C", "P", 400, 400),
		},
	}
	if _, err := Open(writeStructLedger(t, legal)); err != nil {
		t.Fatalf("0.600+0.400=1.000 恰好等于初始量应准确接受: %v", err)
	}

	overByOneUnit := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChildren(activeSample("P", "", 1000, 601), "C"),
			"C": plainChild("C", "P", 400, 400),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, overByOneUnit), "P", "0.601", "0.400", "1.001", "1.000")
}

// TestOpenRejectsActiveCapOverrunEvenWhenPendingTransferMatches 样品挂有
// 待确认交接时也要遵守本次上限：交接量 7.000 恰好等于剩余量 7.000，也
// 不能让 7.000+4.000=11.000 的超量记录通过。数量核对先于交接核对，错误
// 必须写明数量原因。
func TestOpenRejectsActiveCapOverrunEvenWhenPendingTransferMatches(t *testing.T) {
	parent := withChildren(activeSample("P", "", 10000, 7000), "C")
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
	mustRejectOpen(t, path, "P", "7.000", "4.000", "11.000", "10.000")
}

// TestOpenActiveCapOverflowBeyondSupportedRange 合计超出 int64 支持范围时
// 明确按无效数据拒绝，不能把回绕后的小数量当作合法合计：
//   - 剩余量取上限、直接子样再分出 0.001，最终合计溢出；
//   - 各直接子样初始量累加本身溢出时，在统计直接分出总量时即拒绝。
func TestOpenActiveCapOverflowBeyondSupportedRange(t *testing.T) {
	overflowFinal := &ledger{
		Samples: map[string]*sampleRecord{
			"BIG":   withChildren(activeSample("BIG", "", math.MaxInt64, math.MaxInt64), "BIG-C"),
			"BIG-C": plainChild("BIG-C", "BIG", 1, 1),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, overflowFinal), "BIG", "超出可表示范围")

	overflowChildren := &ledger{
		Samples: map[string]*sampleRecord{
			"BIG": withChildren(activeSample("BIG", "", math.MaxInt64, 0), "C1", "C2"),
			"C1":  plainChild("C1", "BIG", math.MaxInt64, math.MaxInt64),
			"C2":  plainChild("C2", "BIG", 1, 1),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, overflowChildren), "BIG", "超出可表示范围")
}

// TestOpenActiveCapCheckIsWholeFileFailure 任一未销毁样品超量时整份文件打开
// 失败，不返回可用的 Store，也不只加载其他正常记录（原文件不改写由
// mustRejectOpen 统一核对）。
func TestOpenActiveCapCheckIsWholeFileFailure(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"OK":    activeSample("OK", "", 1000, 1000),
			"BAD":   withChildren(activeSample("BAD", "", 1000, 800), "BAD-C"),
			"BAD-C": plainChild("BAD-C", "BAD", 300, 300),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "BAD", "0.800", "0.300", "1.100", "1.000")
}

// withChildren 覆盖构造样品的直接子样列表，便于显式制造漏列、重复列入等
// 场景；数量核对只以子样的来源编号为准，列表内容不影响计入的分出量。
func withChildren(rec *sampleRecord, children ...string) *sampleRecord {
	rec.Children = append([]string{}, children...)
	return rec
}
