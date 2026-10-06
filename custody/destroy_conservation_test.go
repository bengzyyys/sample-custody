package custody

import (
	"bytes"
	"errors"
	"math"
	"os"
	"testing"
	"time"
)

// 销毁前数量守恒核对相关测试。
//
// 旧记录重新打开时允许“剩余量 + 直接分出总量 < 初始量”的缺口存在（仍可
// 打开和查询）；但销毁会把剩余量归零并记下实际销毁量，恢复时要求数量恰好
// 守恒，因此销毁前必须先核对：当前剩余量与各直接子样创建时取得的初始量
// 之和恰好等于样品自身初始量，否则返回包装了 ErrConflict 的错误，不返回
// 成功结果，数量、持有人、地点、来源关系、子样列表、保管历史与文件内容
// 全部保持原样，之后仍能重新打开并查询这份旧记录。
//
// 带缺口的状态无法通过公开操作产生（分装精确扣减），所以这些测试直接构造
// 旧数据文件，再用公开的 Destroy 走完整流程。

// destroyGapAt 晚于构造数据中的全部既有保管历史。
var destroyGapAt = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

// legacyParentWithChild 构造“原样 P 初始 initial、当前剩余 remaining，
// 已直接分出子样 C（创建时取得 childInitial，当前剩余 childRemaining）”
// 的未销毁旧记录；holder/loc 同时用于原样与子样，便于销毁入参填写。
func legacyParentWithChild(t *testing.T, parent string, initial, remaining int64, child string, childInitial, childRemaining int64) *ledger {
	t.Helper()
	p := activeSample(parent, "", initial, remaining)
	p.Holder, p.Location = "李四", "实验室B"
	p.Children = []string{child}
	p.History = append(p.History, historyRecord{
		Kind: "split", Time: splitCheckAt, Holder: "李四", Location: "实验室B",
	})
	c := plainChild(child, parent, childInitial, childRemaining)
	return &ledger{
		Samples: map[string]*sampleRecord{
			parent: p,
			child:  c,
		},
	}
}

// TestDestroyRejectsLegacyShortfallTaskExample 任务示例：原样初始
// 10.000 毫升，直接子样创建时取得 4.000 毫升，原样当前只剩 5.000 毫升
// （缺口 1.000）。这份旧记录本来可以打开；销毁必须被明确拒绝，不能记下
// 5.000 毫升返回成功，否则同一文件将因少 1.000 毫升无法再次打开。
func TestDestroyRejectsLegacyShortfallTaskExample(t *testing.T) {
	l := legacyParentWithChild(t, "S-001", 10000, 5000, "S-001-A", 4000, 4000)
	path := writeStructLedger(t, l)

	// 旧记录带缺口仍按约定打开并查询。
	s, err := Open(path)
	if err != nil {
		t.Fatalf("带缺口的旧记录应仍能打开: %v", err)
	}
	before, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	if before.Remaining != "5.000" || before.Destruction != nil ||
		len(before.Children) != 1 || before.Children[0] != "S-001-A" {
		t.Fatalf("前置状态错误: %+v", before)
	}
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	in := DestroyInput{
		SampleID: "S-001", Operator: "李四", Location: "实验室B",
		At: destroyGapAt, Reason: "实验结束按规程销毁",
	}
	done, err := s.Destroy(in)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("数量有缺口时销毁应返回 ErrConflict, got %v", err)
	}
	if done != nil {
		t.Fatalf("被拒绝的销毁不得返回成功的样品结果: %+v", done)
	}
	// 错误说明写明样品编号，并以三位小数毫升列出初始量、剩余量、直接分出
	// 总量和缺口。
	msg := err.Error()
	for _, want := range []string{"S-001", "10.000", "5.000", "4.000", "1.000", "缺口"} {
		if !bytes.Contains([]byte(msg), []byte(want)) {
			t.Fatalf("错误信息应包含 %q: %v", want, err)
		}
	}

	// 拒绝后样品仍为未销毁，数量、持有人、地点、来源关系、子样列表和保管
	// 历史都保持原样。
	after, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	if after.Destruction != nil || after.Remaining != "5.000" ||
		after.Holder != "李四" || after.Location != "实验室B" ||
		len(after.Children) != 1 || after.Children[0] != "S-001-A" ||
		len(after.History) != len(before.History) {
		t.Fatalf("被拒绝的销毁改动了原样状态: before=%+v after=%+v", before, after)
	}
	for _, h := range after.History {
		if h.Kind == "destroy" {
			t.Fatalf("被拒绝的销毁不得追加销毁历史: %+v", h)
		}
	}
	child, err := s.GetSample("S-001-A")
	if err != nil {
		t.Fatal(err)
	}
	if child.InitialQty != "4.000" || child.Remaining != "4.000" ||
		child.ParentID != "S-001" || child.Destruction != nil {
		t.Fatalf("被拒绝的销毁不得改动子样: %+v", child)
	}

	// 文件内容保持原样，之后仍能重新打开并查询这份旧记录。
	diskAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(diskBefore, diskAfter) {
		t.Fatalf("被拒绝的销毁改写了文件")
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("拒绝销毁后旧记录应仍能重新打开: %v", err)
	}
	reopened, err := s2.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Remaining != "5.000" || reopened.Destruction != nil ||
		len(reopened.Children) != 1 || reopened.Children[0] != "S-001-A" {
		t.Fatalf("重开后旧记录应原样可查: %+v", reopened)
	}

	// 用同一入参再次提交仍被拒绝（没有被当成已完成的销毁幂等返回）。
	if done2, err2 := s2.Destroy(in); !errors.Is(err2, ErrConflict) || done2 != nil {
		t.Fatalf("缺口未补齐时再次销毁仍应 ErrConflict 且无成功结果: view=%+v err=%v",
			done2, err2)
	}
	if _, err := Open(path); err != nil {
		t.Fatalf("再次被拒后文件仍应可打开: %v", err)
	}
}

// TestDestroyRejectsTinyGapWithoutChildren 没有直接子样时，剩余量也必须
// 等于自身初始量，哪怕只缺 0.001 毫升也应拒绝。
func TestDestroyRejectsTinyGapWithoutChildren(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": activeSample("P", "", 1000, 999),
		},
	}
	path := writeStructLedger(t, l)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("旧记录应能打开: %v", err)
	}
	done, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "h", Location: "l",
		At: destroyGapAt, Reason: "r",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("缺 0.001 毫升也应 ErrConflict, got %v", err)
	}
	if done != nil {
		t.Fatalf("被拒绝的销毁不得返回成功结果: %+v", done)
	}
	msg := err.Error()
	for _, want := range []string{"P", "1.000", "0.999", "0.000", "0.001", "缺口"} {
		if !bytes.Contains([]byte(msg), []byte(want)) {
			t.Fatalf("错误信息应包含 %q（直接分出总量为 0.000）: %v", want, err)
		}
	}
	got, _ := s.GetSample("P")
	if got.Destruction != nil || got.Remaining != "0.999" {
		t.Fatalf("拒绝后状态应保持: %+v", got)
	}
	if _, err := Open(path); err != nil {
		t.Fatalf("拒绝销毁后文件应仍可打开: %v", err)
	}
}

// TestDestroyConservationUsesChildCreationInitialOnly 核对只计各直接子样
// 创建时取得的初始量：
//   - 子样后来继续分装、当前只剩 1.000（又分出 3.000 给孙样），原样仍按
//     子样初始 4.000 核对，6.000+4.000=10.000，销毁成功、记录 6.000；
//   - 子样后来已独立销毁，计入原样核对的仍是它创建时的 4.000；
//   - 孙样不重复计入原样的合计。
func TestDestroyConservationUsesChildCreationInitialOnly(t *testing.T) {
	// 场景一：子样继续分装给孙样，当前只剩 1.000；原样按子样初始 4.000
	// 核对，销毁 6.000 成功，重开守恒可查。
	childSplit := plainChild("C", "P", 4000, 1000)
	childSplit.Children = []string{"G"}
	childSplit.History = append(childSplit.History, historyRecord{
		Kind: "split", Time: splitCheckAt.Add(time.Hour), Holder: "李四", Location: "实验室B",
	})
	l1 := &ledger{
		Samples: map[string]*sampleRecord{
			"P": func() *sampleRecord {
				p := activeSample("P", "", 10000, 6000)
				p.Holder, p.Location = "李四", "实验室B"
				p.Children = []string{"C"}
				p.History = append(p.History, historyRecord{
					Kind: "split", Time: splitCheckAt, Holder: "李四", Location: "实验室B",
				})
				return p
			}(),
			"C": childSplit,
			"G": plainChild("G", "C", 3000, 3000),
		},
	}
	path1 := writeStructLedger(t, l1)
	s1, err := Open(path1)
	if err != nil {
		t.Fatalf("旧记录应能打开: %v", err)
	}
	done, err := s1.Destroy(DestroyInput{
		SampleID: "P", Operator: "李四", Location: "实验室B",
		At: destroyGapAt, Reason: "r",
	})
	if err != nil {
		t.Fatalf("应按子样创建初始量 4.000 核对（6.000+4.000=10.000），不能用其当前剩余 1.000: %v", err)
	}
	if done.Remaining != "0.000" || done.Destruction == nil || done.Destruction.Qty != "6.000" {
		t.Fatalf("销毁视图应记录真实销毁量 6.000: %+v", done)
	}
	s1r, err := Open(path1)
	if err != nil {
		t.Fatalf("守恒销毁后文件应能重新打开: %v", err)
	}
	p, _ := s1r.GetSample("P")
	if p.Remaining != "0.000" || p.Destruction == nil || p.Destruction.Qty != "6.000" {
		t.Fatalf("重开后销毁记录应保留真实数量 6.000: %+v", p)
	}

	// 场景二：子样已独立销毁（销毁其当时剩余 1.000，孙样 3.000 合计守恒），
	// 原样核对仍按子样创建初始量 4.000，6.000+4.000=10.000 销毁成功。
	childDestroyed := destroyedSampleWithParent("C", "P", 4000, 1000)
	childDestroyed.Children = []string{"G"}
	l2 := &ledger{
		Samples: map[string]*sampleRecord{
			"P": func() *sampleRecord {
				p := activeSample("P", "", 10000, 6000)
				p.Holder, p.Location = "李四", "实验室B"
				p.Children = []string{"C"}
				p.History = append(p.History, historyRecord{
					Kind: "split", Time: splitCheckAt, Holder: "李四", Location: "实验室B",
				})
				return p
			}(),
			"C": childDestroyed,
			"G": plainChild("G", "C", 3000, 3000),
		},
	}
	path2 := writeStructLedger(t, l2)
	s2, err := Open(path2)
	if err != nil {
		t.Fatalf("子样已销毁的旧记录应能打开: %v", err)
	}
	done2, err := s2.Destroy(DestroyInput{
		SampleID: "P", Operator: "李四", Location: "实验室B",
		At: destroyGapAt, Reason: "r",
	})
	if err != nil {
		t.Fatalf("子样后来销毁不改变原样已分出量，应按 4.000 核对通过: %v", err)
	}
	if done2.Destruction.Qty != "6.000" {
		t.Fatalf("原样销毁量应为 6.000, got %s", done2.Destruction.Qty)
	}
	if _, err := Open(path2); err != nil {
		t.Fatalf("守恒销毁后文件应能重新打开: %v", err)
	}
}

// TestDestroySplitChildCheckedAgainstOwnInitial 分装子样申请销毁时按它自身
// 创建时取得的初始量核对：子样 C 初始 4.000、剩余 2.000，只直接分出孙样
// G 1.000，缺口 1.000 必须拒绝；父样 P 与兄弟子样 D 的数量不参与 C 的
// 核对（直接分出总量只列 G 的 1.000，不能把 D 的 2.000 或父样的量算进
// 来凑齐）。父样自身数量恰好守恒时仍可独立销毁，销毁父样不改变 C 的
// 缺口，C 仍为未销毁、可重新打开查询，再次申请销毁仍被拒绝。
func TestDestroySplitChildCheckedAgainstOwnInitial(t *testing.T) {
	p := activeSample("P", "", 10000, 4000)
	p.Holder, p.Location = "李四", "实验室B"
	p.Children = []string{"C", "D"}
	p.History = append(p.History, historyRecord{
		Kind: "split", Time: splitCheckAt, Holder: "李四", Location: "实验室B",
	})
	c := plainChild("C", "P", 4000, 2000)
	c.Children = []string{"G"}
	c.History = append(c.History, historyRecord{
		Kind: "split", Time: splitCheckAt.Add(time.Hour), Holder: "李四", Location: "实验室B",
	})
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": p,
			"C": c,
			"D": plainChild("D", "P", 2000, 2000),
			"G": plainChild("G", "C", 1000, 1000),
		},
	}
	path := writeStructLedger(t, l)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("旧记录应能打开: %v", err)
	}

	childIn := DestroyInput{
		SampleID: "C", Operator: "李四", Location: "实验室B",
		At: destroyGapAt, Reason: "r",
	}
	done, err := s.Destroy(childIn)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("子样数量有缺口应 ErrConflict, got %v", err)
	}
	if done != nil {
		t.Fatalf("被拒绝的销毁不得返回成功结果: %+v", done)
	}
	msg := err.Error()
	for _, want := range []string{"C", "4.000", "2.000", "1.000", "缺口"} {
		if !bytes.Contains([]byte(msg), []byte(want)) {
			t.Fatalf("错误信息应包含 %q: %v", want, err)
		}
	}

	got, _ := s.GetSample("C")
	if got.Destruction != nil || got.Remaining != "2.000" {
		t.Fatalf("拒绝后子样应保持未销毁: %+v", got)
	}

	// 父样自身守恒：4.000 + C 4.000 + D 2.000 = 10.000（孙样 G 不重复
	// 计入），销毁成功，记录真实销毁量 4.000。
	pDone, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "李四", Location: "实验室B",
		At: destroyGapAt.Add(time.Hour), Reason: "r",
	})
	if err != nil {
		t.Fatalf("父样数量守恒应能销毁，孙样不得重复计入: %v", err)
	}
	if pDone.Destruction.Qty != "4.000" || pDone.Remaining != "0.000" {
		t.Fatalf("父样销毁量应为 4.000: %+v", pDone)
	}

	// 重开：父样销毁守恒可恢复；子样 C 仍带缺口、未销毁，可查询。
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("父样守恒销毁后文件应能打开: %v", err)
	}
	c2, err := s2.GetSample("C")
	if err != nil {
		t.Fatal(err)
	}
	if c2.Destruction != nil || c2.Remaining != "2.000" || c2.InitialQty != "4.000" {
		t.Fatalf("父样销毁不得改变子样 C 的缺口记录: %+v", c2)
	}
	// 父样已销毁后，子样 C 的销毁仍按它自身的数量缺口拒绝。
	if done2, err := s2.Destroy(childIn); !errors.Is(err, ErrConflict) || done2 != nil {
		t.Fatalf("父样销毁后子样的缺口仍应拒绝: view=%+v err=%v", done2, err)
	}
}

// TestDestroyBalancedLegacyRecordSucceedsAndReopens 数量恰好守恒的旧记录
// 继续按原规则销毁：只销毁当时全部剩余量，返回剩余 0.000，销毁记录保留
// 真实数量，文件再次打开后仍可查询。
func TestDestroyBalancedLegacyRecordSucceedsAndReopens(t *testing.T) {
	// 初始 10.000、直接分出 4.000、剩余 6.000：6.000+4.000=10.000。
	l := legacyParentWithChild(t, "P", 10000, 6000, "C", 4000, 4000)
	path := writeStructLedger(t, l)

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "李四", Location: "实验室B",
		At: destroyGapAt, Reason: "实验结束按规程销毁",
	})
	if err != nil {
		t.Fatalf("数量恰好守恒应按原规则销毁: %v", err)
	}
	if done.Remaining != "0.000" || done.InitialQty != "10.000" ||
		done.Destruction == nil || done.Destruction.Qty != "6.000" {
		t.Fatalf("销毁视图应保留初始量并记录真实销毁量 6.000: %+v", done)
	}
	if len(done.Children) != 1 || done.Children[0] != "C" {
		t.Fatalf("销毁应保留子样列表: %v", done.Children)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("守恒销毁后文件必须能再次打开: %v", err)
	}
	p, err := s2.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if p.Remaining != "0.000" || p.Destruction == nil || p.Destruction.Qty != "6.000" {
		t.Fatalf("重开后销毁记录应保留真实数量: %+v", p)
	}
	c, err := s2.GetSample("C")
	if err != nil {
		t.Fatal(err)
	}
	if c.InitialQty != "4.000" || c.Remaining != "4.000" || c.Destruction != nil {
		t.Fatalf("销毁父样不得改变已分出的子样: %+v", c)
	}
}

// TestDestroyConservationOverflowConflict 剩余量与直接分出总量合计超出
// int64 可表示范围时按冲突拒绝，不能把回绕后的差额当作守恒结果销毁。
func TestDestroyConservationOverflowConflict(t *testing.T) {
	p := activeSample("BIG", "", math.MaxInt64, math.MaxInt64)
	p.Children = []string{"BIG-C"}
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"BIG":   p,
			"BIG-C": plainChild("BIG-C", "BIG", 1, 1),
		},
	}
	// 该记录剩余量本身等于初始量、子样来源也指向它，但“剩余量 + 子样
	// 初始量”溢出：守恒核对本身必须返回 ErrConflict，不能把回绕后的结果
	// 当作守恒放行销毁。
	if err := checkDestroyConservation("BIG", p, l); !errors.Is(err, ErrConflict) {
		t.Fatalf("合计溢出应返回 ErrConflict, got %v", err)
	}
}
