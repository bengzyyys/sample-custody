package custody

import (
	"testing"
)

// 已销毁样品恢复时的直接子样关系一致性核对。
//
// 已销毁样品的分出量必须来自关系一致的直接子样：Children 中的编号各自只
// 出现一次、对应现存样品且来源编号指向本样品；反过来，来源编号指向本样品
// 的样品也必须全部列在 Children 中。即使销毁量加列表合计恰好等于初始量，
// 重复列入、来源不符或漏列都必须拒绝打开。这组测试用公开操作无法产生的
// 损坏 ledger 构造这些场景。

// rootRecord 构造一份未销毁的原样记录（用于充当“另一份原样”）。
func rootRecord(id string, initial, remaining int64) *sampleRecord {
	return &sampleRecord{
		ID:        id,
		Initial:   initial,
		Remaining: remaining,
		Holder:    "李四",
		Location:  "实验室B",
		Children:  []string{},
		History: []historyRecord{{
			Kind: "register", Time: registerCheckAt, Holder: "李四", Location: "实验室B",
		}},
	}
}

// TestOpenRejectsDestroyedDuplicateChildDespiteBalancedTotal 任务示例：
// P 初始 10.000，C 由 P 分出 3.000；P 记录销毁 4.000，子样列表却把 C
// 写了两遍，4.000+3.000+3.000 看似等于 10.000，也必须拒绝，错误要点名
// P、C 与重复列入。
func TestOpenRejectsDestroyedDuplicateChildDespiteBalancedTotal(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": destroyedSample("P", 10000, 0, 4000, "C", "C"),
			"C": plainChild("C", "P", 3000, 3000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "P", "C", "重复")
}

// TestOpenRejectsDestroyedDuplicateChildMultipleIDs 多个子样中任一编号重复
// 都要拒绝，且不能把重复项当成两份子样累计。
func TestOpenRejectsDestroyedDuplicateChildMultipleIDs(t *testing.T) {
	// 销毁 1.000 + C1 3.000*2 + C2 3.000 = 10.000，数字凑齐仍是坏数据。
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P":  destroyedSample("P", 10000, 0, 1000, "C1", "C2", "C1"),
			"C1": plainChild("C1", "P", 3000, 3000),
			"C2": plainChild("C2", "P", 3000, 3000),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, l), "P", "C1", "重复")
}

// TestOpenRestoresDestroyedWithSingleChildListing 任务示例的合法分支：
// C 只列一次且销毁量为 7.000（7.000+3.000=10.000）才符合记录。
func TestOpenRestoresDestroyedWithSingleChildListing(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": destroyedSample("P", 10000, 0, 7000, "C"),
			"C": plainChild("C", "P", 3000, 3000),
		},
	}
	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("C 只列一次且销毁量 7.000 时关系一致，应正常恢复: %v", err)
	}
	p, _ := s.GetSample("P")
	if p.Destruction == nil || p.Destruction.Qty != "7.000" || len(p.Children) != 1 {
		t.Fatalf("合法父子销毁记录恢复错误: %+v", p)
	}
}

// TestOpenRejectsDestroyedChildFromOtherRoot 列入的子样实际来自另一份原样：
// 数量 7.000+3.000=10.000 等式成立，但 C 的来源编号是 Q 而非 P，来源不符
// 必须拒绝，不能靠凑齐数量通过核对。
func TestOpenRejectsDestroyedChildFromOtherRoot(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": destroyedSample("P", 10000, 0, 7000, "C"),
			"Q": rootRecord("Q", 5000, 2000),
			"C": plainChild("C", "Q", 3000, 3000),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, l), "P", "C", "Q", "来源")
}

// TestOpenRejectsDestroyedChildListedAsRoot 列入的编号其实是登记的原样
// （没有来源编号），同样属于来源不符。
func TestOpenRejectsDestroyedChildListedAsRoot(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": destroyedSample("P", 10000, 0, 7000, "C"),
			"C": rootRecord("C", 3000, 3000),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, l), "P", "C", "来源")
}

// TestOpenRejectsDestroyedWhenBackpointingChildOmitted 反向漏列：C 仍记着
// 来源 P，却没有列在 P 的子样列表里。P 列表为空、销毁量写成等于初始量
// 10.000，单看销毁等式是成立的，但来源关系互相矛盾，必须拒绝。
func TestOpenRejectsDestroyedWhenBackpointingChildOmitted(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": destroyedSample("P", 10000, 0, 10000),
			"C": plainChild("C", "P", 3000, 3000),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, l), "P", "C", "漏列")
}

// TestOpenRejectsDestroyedWhenExtraBackpointingChildOmitted 列表自身合计与
// 销毁量已经等于初始量，但还有另一个来源指向 P 的子样 D 没列入：等式成立
// 也必须按漏列拒绝，不能忽略漏列项接受文件。
func TestOpenRejectsDestroyedWhenExtraBackpointingChildOmitted(t *testing.T) {
	// 只列 C：7.000+3.000=10.000 数字恰好守恒；D 却也指向 P。
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": destroyedSample("P", 10000, 0, 7000, "C"),
			"C": plainChild("C", "P", 3000, 3000),
			"D": plainChild("D", "P", 2000, 2000),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, l), "P", "D", "漏列")
}

// TestOpenRejectsDestroyedOtherRootsChildrenNotCounted 来源指向其他原样的
// 子样没有列入本样品时不参与本样品核对：P 无子样销毁 10.000 合法，Q 的
// 子样 C 不能被当成 P 漏列的子样。
func TestOpenRejectsDestroyedOtherRootsChildrenNotCounted(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": destroyedSample("P", 10000, 0, 10000),
			"Q": rootRecord("Q", 5000, 2000),
			"C": plainChild("C", "Q", 3000, 3000),
		},
	}
	if _, err := Open(writeStructLedger(t, l)); err != nil {
		t.Fatalf("其他原样的子样与本样品销毁核对无关，应正常恢复: %v", err)
	}
}

// TestOpenRejectsDestroyedSplitChildDuplicateOwnChild 关系核对同样适用于
// 已销毁的分装子样：它的分出量只来自自己直接创建的子样。C 由 P 分出
// 5.000，自己又分出 D 2.000，C 销毁 1.000；列表把 D 写两遍后
// 1.000+2.000+2.000 看似等于 C 的初始量 5.000，仍必须以 C、D 的重复
// 问题拒绝。
func TestOpenRejectsDestroyedSplitChildDuplicateOwnChild(t *testing.T) {
	c := destroyedSampleWithParent("C", "P", 5000, 1000)
	c.Children = []string{"D", "D"}
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": rootRecord("P", 10000, 5000),
			"C": c,
			"D": plainChild("D", "C", 2000, 2000),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, l), "C", "D", "重复")
}

// TestOpenRestoresDestroyedSplitChildWithOwnChildren 合法的三代场景：
// P 分出 C 5.000；C 分出 D 2.000 后销毁剩余 3.000；C 的核对只统计自己
// 直接分出的 D，P 的记录不受影响。
func TestOpenRestoresDestroyedSplitChildWithOwnChildren(t *testing.T) {
	p := rootRecord("P", 10000, 5000)
	p.Children = []string{"C"}
	c := destroyedSampleWithParent("C", "P", 5000, 3000)
	c.Children = []string{"D"}
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": p,
			"C": c,
			"D": plainChild("D", "C", 2000, 2000),
		},
	}
	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("已销毁分装子样与自己直接子样关系一致时应正常恢复: %v", err)
	}
	gotC, _ := s.GetSample("C")
	if gotC.Destruction == nil || gotC.Destruction.Qty != "3.000" {
		t.Fatalf("子样销毁量应为 3.000: %+v", gotC.Destruction)
	}
	if len(gotC.Children) != 1 || gotC.Children[0] != "D" || gotC.ParentID != "P" {
		t.Fatalf("子样来源与直接子样关系应保留: %+v", gotC)
	}
}

// TestOpenRejectsDestroyedSplitChildMissingOwnChild 已销毁分装子样 C 漏列
// 自己直接分出的 D（D 的来源仍指向 C），即使 C 的销毁量等于自身初始量
// 也要按漏列拒绝。
func TestOpenRejectsDestroyedSplitChildMissingOwnChild(t *testing.T) {
	c := destroyedSampleWithParent("C", "P", 5000, 5000)
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": rootRecord("P", 10000, 5000),
			"C": c,
			"D": plainChild("D", "C", 2000, 2000),
		},
	}
	mustRejectOpen(t, writeStructLedger(t, l), "C", "D", "漏列")
}
