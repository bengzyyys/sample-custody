package custody

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 销毁数量恢复核对相关测试。
//
// 规则：带销毁信息的样品恢复时，当前剩余量必须为 0.000，自身初始量与
// 实际销毁量都大于零，且实际销毁量加上各直接子样创建时取得的初始量之和
// 恰好等于该样品自身的初始量。子样后来的分装、转交、销毁都不改变原样
// 当时已分出的量，孙样不重复计入。

// destroyedAt 为销毁类构造数据使用的固定时间。
var destroyedCheckAt = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

// registerHistoryAt / splitHistoryAt 为构造数据中的既有历史时间。
var (
	registerCheckAt = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	splitCheckAt    = time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
)

// writeStructLedger 把内存 ledger 结构序列化为临时数据文件并返回路径，
// 便于构造公开操作无法产生的各种数量组合。
func writeStructLedger(t *testing.T, l *ledger) string {
	t.Helper()
	if l.Version == 0 {
		l.Version = 1
	}
	if l.Samples == nil {
		l.Samples = map[string]*sampleRecord{}
	}
	if l.Transfers == nil {
		l.Transfers = map[string]*transferRecord{}
	}
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		t.Fatalf("序列化测试数据: %v", err)
	}
	return writeRawLedger(t, string(raw))
}

// destroyedSample 构造一份带销毁信息的样品记录，数量单位均为千分之一毫升。
func destroyedSample(id string, initial, remaining, destroyedQty int64, children ...string) *sampleRecord {
	s := &sampleRecord{
		ID:        id,
		Initial:   initial,
		Remaining: remaining,
		Holder:    "李四",
		Location:  "实验室B",
		Children:  append([]string{}, children...),
		History: []historyRecord{{
			Kind: "register", Time: registerCheckAt, Holder: "李四", Location: "实验室B",
		}},
		Destroyed: &destructionRecord{
			Operator: "李四",
			Location: "实验室B",
			At:       destroyedCheckAt,
			Reason:   "实验结束按规程销毁",
			Qty:      destroyedQty,
		},
	}
	if len(children) > 0 {
		s.History = append(s.History, historyRecord{
			Kind: "split", Time: splitCheckAt, Holder: "李四", Location: "实验室B",
		})
	}
	s.History = append(s.History, historyRecord{
		Kind: "destroy", Time: destroyedCheckAt, Holder: "李四", Location: "实验室B",
	})
	return s
}

// plainChild 构造一份未销毁子样记录。
func plainChild(id, parent string, initial, remaining int64) *sampleRecord {
	return &sampleRecord{
		ID:        id,
		ParentID:  parent,
		Initial:   initial,
		Remaining: remaining,
		Holder:    "李四",
		Location:  "实验室B",
		Children:  []string{},
		History: []historyRecord{{
			Kind: "split", Time: splitCheckAt, Holder: "李四", Location: "实验室B",
		}},
	}
}

// TestOpenRejectsDestroyedFullInitialAfterSplit 任务示例：原样初始
// 10.000、已分出 3.250 子样，正确销毁量应是 6.750；文件却把销毁量写成
// 10.000（剩余量已归零、交接均合法），必须拒绝打开，不能把多出的量当作
// 真实销毁。错误信息写明样品编号、初始量、销毁量与直接子样总量。
func TestOpenRejectsDestroyedFullInitialAfterSplit(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":   destroyedSample("S-001", 10000, 0, 10000, "S-001-A"),
			"S-001-A": plainChild("S-001-A", "S-001", 3250, 3250),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "S-001", "10.000", "3.250", "销毁")
}

// TestOpenRejectsDestroyedWithNonZeroRemaining 带销毁信息但剩余量未归零，
// 即使销毁量加子样量恰好等于初始量也拒绝。
func TestOpenRejectsDestroyedWithNonZeroRemaining(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":   destroyedSample("S-001", 10000, 1000, 6750, "S-001-A"),
			"S-001-A": plainChild("S-001-A", "S-001", 3250, 3250),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "S-001", "0.000", "1.000")
}

// TestOpenRejectsNonPositiveQuantities 参与核对的初始量与实际销毁量都必须
// 大于零，零值与负值一律拒绝。
func TestOpenRejectsNonPositiveQuantities(t *testing.T) {
	cases := []struct {
		name      string
		initial   int64
		destroyed int64
		mention   string
	}{
		{"销毁量为零", 1000, 0, "销毁量"},
		{"销毁量为负", 1000, -1000, "销毁量"},
		{"初始量为零", 0, 0, "初始量"},
		{"初始量为负", -1000, -1000, "初始量"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := &ledger{
				Samples: map[string]*sampleRecord{
					"BAD": destroyedSample("BAD", c.initial, 0, c.destroyed),
				},
			}
			path := writeStructLedger(t, l)
			mustRejectOpen(t, path, "BAD", c.mention, "大于零")
		})
	}
}

// TestOpenRejectsDestroyedMismatchWithoutChildren 没有分出子样的样品，
// 实际销毁量必须等于自身初始量；4.000 != 5.000 时拒绝，信息展示初始量
// 5.000、销毁量 4.000 与直接子样总量 0.000。
func TestOpenRejectsDestroyedMismatchWithoutChildren(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"LEAF": destroyedSample("LEAF", 5000, 0, 4000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "LEAF", "5.000", "4.000", "0.000")
}

// TestOpenRejectsDestroyedWhenChildMissing Children 列着不存在的直接子样，
// 无法统计分出量，按损坏数据拒绝。
func TestOpenRejectsDestroyedWhenChildMissing(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001": destroyedSample("S-001", 10000, 0, 6750, "GHOST"),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "S-001", "GHOST")
}

// TestOpenRejectsDestroyedDuplicateChildListing 任务示例：原样 P 初始
// 10.000，C 由 P 分出 3.000，P 销毁剩余 4.000；子样列表却把 C 写了两遍，
// 销毁量加两次 C 的合计碰巧等于初始量（4.000+3.000+3.000=10.000）。重复
// 列入使来源关系自相矛盾，必须拒绝，不能靠删除重复项接受文件。
func TestOpenRejectsDestroyedDuplicateChildListing(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": destroyedSample("P", 10000, 0, 4000, "C", "C"),
			"C": plainChild("C", "P", 3000, 3000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "P", "C", "重复")
}

// TestOpenRejectsDestroyedChildFromOtherRoot 列入的子样实际来自另一份原样，
// 即使销毁量 7.000 加该子样初始量 3.000 恰好等于初始量 10.000，来源不符
// 也必须拒绝，不能靠凑齐数量通过核对。
func TestOpenRejectsDestroyedChildFromOtherRoot(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P":  destroyedSample("P", 10000, 0, 7000, "C"),
			"C":  plainChild("C", "P2", 3000, 3000),
			"P2": plainChild("P2", "", 8000, 8000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "P", "C", "P2", "来源")
}

// TestOpenRejectsDestroyedChildWithoutParent 列入的编号其实是没有来源的
// 原样，不能当作本样品的直接子样凑数量。
func TestOpenRejectsDestroyedChildWithoutParent(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": destroyedSample("P", 10000, 0, 7000, "R"),
			"R": plainChild("R", "", 3000, 3000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "P", "R", "来源")
}

// TestOpenRejectsDestroyedMissingChildListing 反向核对：子样 C 的来源编号
// 指向 P，P 的子样列表却没有列 C。P 销毁量 10.000、空列表使数量等式
// 单独成立（10.000=10.000），漏列仍必须拒绝，不能忽略漏列项接受文件。
func TestOpenRejectsDestroyedMissingChildListing(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": destroyedSample("P", 10000, 0, 10000),
			"C": plainChild("C", "P", 3000, 3000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "P", "C", "漏列")
}

// TestOpenRejectsDestroyedSplitChildMissingGrandchild 关系核对同样适用于
// 已销毁的分装子样：C 已销毁且孙样 G 的来源指向 C，C 的子样列表漏列 G，
// 即使 C 的销毁量 3.000 与其自身初始量相等也必须拒绝。
func TestOpenRejectsDestroyedSplitChildMissingGrandchild(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": plainChild("P", "", 7000, 7000),
			"C": destroyedSampleWithParent("C", "P", 3000, 3000),
			"G": plainChild("G", "C", 1000, 1000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "C", "G", "漏列")
}

// TestOpenRestoresDestroyedWithSingleDirectChild 任务示例的合法对照：P 初始
// 10.000、C 由 P 分出 3.000 且只列一次，P 销毁 7.000（7.000+3.000=
// 10.000），关系一致、数量守恒，正常恢复并保留子样顺序。
func TestOpenRestoresDestroyedWithSingleDirectChild(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": destroyedSample("P", 10000, 0, 7000, "C"),
			"C": plainChild("C", "P", 3000, 3000),
		},
	}
	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("C 只列一次且销毁量为 7.000 应符合记录、正常恢复: %v", err)
	}
	p, _ := s.GetSample("P")
	if p.Destruction == nil || p.Destruction.Qty != "7.000" ||
		len(p.Children) != 1 || p.Children[0] != "C" {
		t.Fatalf("销毁量与子样列表应原样保留: %+v", p)
	}
}

// TestOpenRejectsDestroyedWhenChildInitialNotPositive 直接子样初始量必须
// 大于零，否则销毁数量核对无意义，拒绝恢复。
func TestOpenRejectsDestroyedWhenChildInitialNotPositive(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":   destroyedSample("S-001", 10000, 0, 10000, "S-001-A"),
			"S-001-A": plainChild("S-001-A", "S-001", 0, 0),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "S-001", "S-001-A", "大于零")
}

// TestOpenRejectsDestroyedAccountingDespiteOtherValidSamples 一份销毁记录
// 不符时整份文件打开失败，不能只加载其他正常样品，也不改写原文件。
func TestOpenRejectsDestroyedAccountingDespiteOtherValidSamples(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"OK":    destroyedSample("OK", 1000, 0, 1000),
			"BAD":   destroyedSample("BAD", 10000, 0, 10000, "BAD-C"),
			"BAD-C": plainChild("BAD-C", "BAD", 3250, 3250),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "BAD", "10.000", "3.250")
}

// TestOpenRestoresDestroyedParentAfterSplit 合法分支：初始 10.000、分出
// 3.250 后销毁剩余 6.750，重开正常恢复；销毁记录、子样与数量均保留。
func TestOpenRestoresDestroyedParentAfterSplit(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":   destroyedSample("S-001", 10000, 0, 6750, "S-001-A"),
			"S-001-A": plainChild("S-001-A", "S-001", 3250, 3250),
		},
	}
	path := writeStructLedger(t, l)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("销毁量 6.750 + 直接子样 3.250 = 初始 10.000 应正常恢复: %v", err)
	}
	p, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	if p.InitialQty != "10.000" || p.Remaining != "0.000" {
		t.Fatalf("父样数量错误: init=%s remaining=%s", p.InitialQty, p.Remaining)
	}
	if p.Destruction == nil || p.Destruction.Qty != "6.750" {
		t.Fatalf("父样销毁量应恢复为 6.750: %+v", p.Destruction)
	}
	if len(p.Children) != 1 || p.Children[0] != "S-001-A" {
		t.Fatalf("直接子样列表应保留: %v", p.Children)
	}
	c, err := s.GetSample("S-001-A")
	if err != nil {
		t.Fatal(err)
	}
	if c.InitialQty != "3.250" || c.Remaining != "3.250" || c.Destruction != nil {
		t.Fatalf("子样不应被父样销毁核对改动: %+v", c)
	}
}

// TestOpenRestoresDestroyedLeafWithoutChildren 没有分出子样时销毁量等于
// 初始量即可正常恢复。
func TestOpenRestoresDestroyedLeafWithoutChildren(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"LEAF": destroyedSample("LEAF", 5000, 0, 5000),
		},
	}
	path := writeStructLedger(t, l)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("无子样且销毁量等于初始量应正常恢复: %v", err)
	}
	got, err := s.GetSample("LEAF")
	if err != nil {
		t.Fatal(err)
	}
	if got.InitialQty != "5.000" || got.Remaining != "0.000" ||
		got.Destruction == nil || got.Destruction.Qty != "5.000" {
		t.Fatalf("销毁信息恢复错误: %+v", got)
	}
}

// TestOpenDestroyedAccountingUsesChildInitialOnly 核对只取各直接子样创建
// 时的初始量：
//   - 孙样不重复计入：父样分出 4.000 给子样，子样又把 4.000 全部分给孙样，
//     父样销毁 6.000 仍满足 6.000 + 4.000 = 10.000；
//   - 子样后来继续分装、转交或销毁不改变父样已分出的量：子样当前只剩
//     2.250（自身又分出 1.000）或已全部销毁，父样仍按子样初始量 3.250 核
//     对，不能改用子样当前剩余量。
func TestOpenDestroyedAccountingUsesChildInitialOnly(t *testing.T) {
	// 场景一：孙样不重复计入；子样分装用尽（无销毁信息）也合法。
	grandchild := plainChild("G", "C", 4000, 4000)
	childExhausted := plainChild("C", "P", 4000, 0)
	childExhausted.Children = []string{"G"}
	childExhausted.History = append(childExhausted.History, historyRecord{
		Kind: "split", Time: splitCheckAt.Add(time.Hour), Holder: "李四", Location: "实验室B",
	})
	l1 := &ledger{
		Samples: map[string]*sampleRecord{
			"P": destroyedSample("P", 10000, 0, 6000, "C"),
			"C": childExhausted,
			"G": grandchild,
		},
	}
	if s, err := Open(writeStructLedger(t, l1)); err != nil {
		t.Fatalf("孙样不得计入父样销毁核对，6.000+4.000=10.000 应恢复: %v", err)
	} else {
		p, _ := s.GetSample("P")
		if p.Destruction == nil || p.Destruction.Qty != "6.000" {
			t.Fatalf("父样销毁量恢复错误: %+v", p.Destruction)
		}
	}

	// 场景二：子样自己又分出 1.000，当前只剩 2.250；父样仍按子样初始量
	// 3.250 核对（6.750 + 3.250 = 10.000）。
	childSplit := plainChild("S-001-A", "S-001", 3250, 2250)
	childSplit.Children = []string{"S-001-A-G"}
	childSplit.History = append(childSplit.History, historyRecord{
		Kind: "split", Time: splitCheckAt.Add(time.Hour), Holder: "李四", Location: "实验室B",
	})
	l2 := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":     destroyedSample("S-001", 10000, 0, 6750, "S-001-A"),
			"S-001-A":   childSplit,
			"S-001-A-G": plainChild("S-001-A-G", "S-001-A", 1000, 1000),
		},
	}
	if _, err := Open(writeStructLedger(t, l2)); err != nil {
		t.Fatalf("必须按子样创建时初始量 3.250 核对，不能用其当前剩余 2.250: %v", err)
	}

	// 场景三：子样已独立销毁（销毁量取其当时剩余 2.250），父样核对仍按
	// 子样初始量 3.250；父子销毁记录恢复后各自独立。
	childDestroyed := destroyedSampleWithParent("S-001-A", "S-001", 3250, 2250)
	childDestroyed.Children = []string{"S-001-A-G"}
	grand := plainChild("S-001-A-G", "S-001-A", 1000, 1000)
	l3 := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":     destroyedSample("S-001", 10000, 0, 6750, "S-001-A"),
			"S-001-A":   childDestroyed,
			"S-001-A-G": grand,
		},
	}
	s3, err := Open(writeStructLedger(t, l3))
	if err != nil {
		t.Fatalf("子样后来销毁不改变父样已分出量，文件应正常恢复: %v", err)
	}
	parent, _ := s3.GetSample("S-001")
	child, _ := s3.GetSample("S-001-A")
	if parent.Destruction == nil || parent.Destruction.Qty != "6.750" {
		t.Fatalf("父样销毁量应为 6.750: %+v", parent.Destruction)
	}
	if child.Destruction == nil || child.Destruction.Qty != "2.250" || child.Remaining != "0.000" {
		t.Fatalf("子样销毁记录应独立保留为 2.250: %+v", child.Destruction)
	}
}

// destroyedSampleWithParent 构造一份有来源、带销毁信息的子样记录。
func destroyedSampleWithParent(id, parent string, initial, destroyedQty int64) *sampleRecord {
	s := destroyedSample(id, initial, 0, destroyedQty)
	s.ParentID = parent
	return s
}

// TestOpenDestroyedAccountingIndependentAcrossRoots 同一份数据里的其他原样
// 不参与某次核对：原样 A 的销毁等式不成立时，即使原样 B 的记录完全正常，
// 也不能拿 B 的数量去凑 A 的差额（整份文件仍失败）；反之各自守恒时互不
// 影响、正常恢复。
func TestOpenDestroyedAccountingIndependentAcrossRoots(t *testing.T) {
	// 各自守恒：A=10.000 分出 3.250 销毁 6.750；B=5.000 全部销毁。
	good := &ledger{
		Samples: map[string]*sampleRecord{
			"A":   destroyedSample("A", 10000, 0, 6750, "A-C"),
			"A-C": plainChild("A-C", "A", 3250, 3250),
			"B":   destroyedSample("B", 5000, 0, 5000),
		},
	}
	s, err := Open(writeStructLedger(t, good))
	if err != nil {
		t.Fatalf("两份原样各自守恒应正常恢复: %v", err)
	}
	a, _ := s.GetSample("A")
	b, _ := s.GetSample("B")
	if a.Destruction == nil || a.Destruction.Qty != "6.750" {
		t.Fatalf("原样 A 销毁量错误: %+v", a.Destruction)
	}
	if b.Destruction == nil || b.Destruction.Qty != "5.000" {
		t.Fatalf("原样 B 销毁量错误: %+v", b.Destruction)
	}

	// A 的销毁量被错写成 10.000（多 3.250），不能用 B 的任何数量抵消。
	bad := &ledger{
		Samples: map[string]*sampleRecord{
			"A":   destroyedSample("A", 10000, 0, 10000, "A-C"),
			"A-C": plainChild("A-C", "A", 3250, 3250),
			"B":   destroyedSample("B", 5000, 0, 5000),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, bad), "A", "10.000", "3.250")
}

// TestOpenDestroyedAtLargeBounds 大数量边界：支持上限附近的合法销毁记录
// 必须逐位准确恢复；销毁量与直接子样初始量合计超出 int64 支持范围时按
// 无效数据拒绝，不能因数量过大忽略差额或接受回绕后的错误等式。
func TestOpenDestroyedAtLargeBounds(t *testing.T) {
	const (
		maxUnits   int64 = 9223372036854775807 // 9223372036854775.807 毫升
		belowUnits int64 = 9223372036854775806 // 9223372036854775.806 毫升
	)

	// 合法：初始量取上限，分出上限-0.001 的直接子样，销毁最后的 0.001，
	// 合计恰好等于上限，重开后三位小数逐位一致。
	legal := &ledger{
		Samples: map[string]*sampleRecord{
			"BIG": destroyedSample("BIG", maxUnits, 0, 1, "BIG-C"),
			"BIG-C": func() *sampleRecord {
				c := plainChild("BIG-C", "BIG", belowUnits, belowUnits)
				return c
			}(),
		},
	}
	s, err := Open(writeStructLedger(t, legal))
	if err != nil {
		t.Fatalf("上限附近的合法销毁记录应准确恢复: %v", err)
	}
	big, _ := s.GetSample("BIG")
	if big.InitialQty != maxQtyString || big.Remaining != "0.000" ||
		big.Destruction == nil || big.Destruction.Qty != "0.001" {
		t.Fatalf("上限父样恢复不精确: %+v", big)
	}
	bigC, _ := s.GetSample("BIG-C")
	if bigC.InitialQty != belowMaxString || bigC.Remaining != belowMaxString {
		t.Fatalf("上限附近子样恢复不精确: init=%s remaining=%s",
			bigC.InitialQty, bigC.Remaining)
	}

	// 溢出场景一：销毁量 + 直接子样初始量在上限之外一个最小刻度
	//（子样 上限-0.001，销毁 0.002：合计 上限+0.001），最终求和溢出，
	// 必须按超出支持范围拒绝。
	overflowFinal := &ledger{
		Samples: map[string]*sampleRecord{
			"BIG":   destroyedSample("BIG", maxUnits, 0, 2, "BIG-C"),
			"BIG-C": plainChild("BIG-C", "BIG", belowUnits, belowUnits),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, overflowFinal), "BIG", "超出可表示范围")

	// 溢出场景二：各直接子样初始量累加本身溢出（子样合计超过上限），
	// 在统计直接子样总量时即拒绝，不能回绕成一个小数量再去凑等式。
	overflowChildren := &ledger{
		Samples: map[string]*sampleRecord{
			"BIG": destroyedSample("BIG", maxUnits, 0, 1, "C1", "C2"),
			"C1":  plainChild("C1", "BIG", belowUnits, belowUnits),
			"C2":  plainChild("C2", "BIG", 2, 2),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, overflowChildren), "BIG", "超出可表示范围")
}

// TestOpenLegacySamplesWithoutDestructionUnaffected 没有销毁信息的旧样品
// 继续按现有规则恢复：无论剩余量是否为零都不参与销毁数量核对，也不能据
// 剩余量为零补出销毁记录。
func TestOpenLegacySamplesWithoutDestructionUnaffected(t *testing.T) {
	// 旧样品分装用尽、剩余量为零、无销毁信息；另带一个正常有量旧样品。
	exhausted := plainChild("OLD", "", 10000, 0)
	exhausted.ParentID = ""
	exhausted.Children = []string{"OLD-C"}
	exhausted.History = []historyRecord{{
		Kind: "register", Time: registerCheckAt, Holder: "h", Location: "l",
	}, {
		Kind: "split", Time: splitCheckAt, Holder: "h", Location: "l",
	}}
	exhausted.Holder, exhausted.Location = "h", "l"
	child := plainChild("OLD-C", "OLD", 10000, 10000)
	fresh := &sampleRecord{
		ID: "NEW", Initial: 5000, Remaining: 5000, Holder: "h", Location: "l",
		Children: []string{},
		History: []historyRecord{{
			Kind: "register", Time: registerCheckAt, Holder: "h", Location: "l",
		}},
	}
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"OLD":   exhausted,
			"OLD-C": child,
			"NEW":   fresh,
		},
	}
	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("无销毁信息的旧样品应按原规则恢复，不做销毁数量核对: %v", err)
	}
	old, _ := s.GetSample("OLD")
	if old.Remaining != "0.000" || old.Destruction != nil {
		t.Fatalf("旧样品剩余为零也不能补出销毁记录: %+v", old)
	}
	newSample, _ := s.GetSample("NEW")
	if newSample.Destruction != nil || newSample.Remaining != "5.000" {
		t.Fatalf("正常旧样品不应受新核对影响: %+v", newSample)
	}
}

// TestOpenRestoresParentAndChildDestructionViaAPI 端到端：通过公开操作产生
// 父子各自销毁的合法文件，重开后两份销毁记录独立保留，来源、人员、地点、
// 保管历史与销毁量都可查询，且恢复后操作限制与幂等行为保持兼容。
func TestOpenRestoresParentAndChildDestructionViaAPI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return registerCheckAt }
	mustRegister(t, s, "P", "10.000", "张三", "A")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C", Qty: "3.250"},
	}}); err != nil {
		t.Fatal(err)
	}
	parentAt := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	if _, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "张三", Location: "A", At: parentAt, Reason: "原样销毁",
	}); err != nil {
		t.Fatalf("销毁父样剩余 6.750: %v", err)
	}
	childAt := parentAt.Add(time.Hour)
	if _, err := s.Destroy(DestroyInput{
		SampleID: "C", Operator: "张三", Location: "A", At: childAt, Reason: "子样销毁",
	}); err != nil {
		t.Fatalf("销毁子样全部 3.250: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("父子各自销毁的合法文件应正常恢复: %v", err)
	}
	p, err := s2.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s2.GetSample("C")
	if err != nil {
		t.Fatal(err)
	}
	if p.InitialQty != "10.000" || p.Remaining != "0.000" ||
		p.Destruction == nil || p.Destruction.Qty != "6.750" ||
		p.Destruction.Operator != "张三" || p.Destruction.Location != "A" ||
		p.Destruction.Reason != "原样销毁" || !p.Destruction.At.Equal(parentAt) {
		t.Fatalf("父样销毁记录恢复错误: %+v", p)
	}
	if len(p.Children) != 1 || p.Children[0] != "C" {
		t.Fatalf("父样来源/子样关系应保留: %+v", p.Children)
	}
	if c.ParentID != "P" || c.InitialQty != "3.250" || c.Remaining != "0.000" ||
		c.Destruction == nil || c.Destruction.Qty != "3.250" ||
		c.Destruction.Reason != "子样销毁" || !c.Destruction.At.Equal(childAt) {
		t.Fatalf("子样销毁记录恢复错误: %+v", c)
	}

	// 恢复后销毁幂等与操作限制继续生效。
	if _, err := s2.Destroy(DestroyInput{
		SampleID: "P", Operator: "张三", Location: "A", At: parentAt, Reason: "原样销毁",
	}); err != nil {
		t.Fatalf("恢复后相同销毁请求应幂等返回: %v", err)
	}
	if _, err := s2.Split(SplitInput{ParentID: "P", Parts: []SplitPart{{ID: "X", Qty: "0.001"}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("恢复后已销毁父样仍不得分装, got %v", err)
	}
	if _, err := s2.Handover(HandoverInput{
		TransferID: "T-X", SampleID: "C",
		FromHolder: "张三", FromLocation: "A",
		ToHolder: "李四", ToLocation: "B", HandedOverAt: childAt.Add(time.Hour),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("恢复后已销毁子样仍不得发起交接, got %v", err)
	}

	// 原始文件没有被恢复流程改写（仍为最后一次成功操作落盘的内容，
	// 重新打开前后再次读取一致）。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"qty": 6750`) || !strings.Contains(string(raw), `"qty": 3250`) {
		t.Fatalf("落盘文件应原样保留两份销毁量: %s", string(raw))
	}
}
