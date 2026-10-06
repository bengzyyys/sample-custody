package custody

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 销毁前数量守恒核对的测试。
//
// 背景：重新打开旧文件时，未销毁样品允许“剩余量 + 直接分出总量 < 自身
// 初始量”（差额作为旧记录保留，可打开、可查询）；但销毁会写下重新打开
// 时必须恰好守恒的销毁记录，因此销毁前必须先核对同一等式，数量有缺口
// 就明确返回 ErrConflict，不能销毁成功后让文件再也无法打开。
//
// 这些缺口状态无法通过公开的登记/分装操作产生（分装总是精确扣减），
// 因此沿用 writeStructLedger 构造旧文件，先证明它能按旧约定打开，再
// 通过公开的 Destroy 验证拒绝行为。

// openGapLedger 写入构造数据并按旧约定打开；打开本身必须成功，否则
// 后续的销毁拒绝测试没有意义。
func openGapLedger(t *testing.T, l *ledger) (*Store, string) {
	t.Helper()
	path := writeStructLedger(t, l)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("前置条件：有缺口的未销毁旧记录必须能打开: %v", err)
	}
	return s, path
}

// destroyGapAt 晚于构造数据中的登记/分装历史，用于发起销毁请求。
var destroyGapAt = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

// assertStillOpenAndUndestroyed 在销毁被拒绝后重新打开同一文件，确认旧
// 记录仍可打开、查询，且仍是未销毁、数量与子样关系保持原样。
func assertStillOpenAndUndestroyed(t *testing.T, path, id, remaining string, children []string) {
	t.Helper()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("被拒绝销毁的旧文件必须仍能重新打开: %v", err)
	}
	got, err := s2.GetSample(id)
	if err != nil {
		t.Fatalf("重新打开后旧记录必须仍可查询: %v", err)
	}
	if got.Destruction != nil {
		t.Fatalf("拒绝销毁后不得留下销毁记录: %+v", got.Destruction)
	}
	if got.Remaining != remaining {
		t.Fatalf("剩余量应保持 %s, got %s", remaining, got.Remaining)
	}
	if len(got.Children) != len(children) {
		t.Fatalf("子样列表被改动: got %v want %v", got.Children, children)
	}
	for i, c := range children {
		if got.Children[i] != c {
			t.Fatalf("子样列表被改动: got %v want %v", got.Children, children)
		}
	}
}

// TestDestroyRejectsTaskExampleGap 任务示例：原样初始 10.000，直接子样
// 创建时取得 4.000，原样却只剩 5.000（旧记录 5.000+4.000=9.000 <
// 10.000，仍可打开）。销毁必须被拒绝：返回 nil 结果与 ErrConflict，
// 错误说明写明样品编号并以三位小数毫升列出初始量 10.000、剩余量
// 5.000、直接分出总量 4.000 和缺口 1.000；样品仍为未销毁，文件原样
// 保留，之后同一文件仍能打开并查询。
func TestDestroyRejectsTaskExampleGap(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":   withChild(activeSample("S-001", "", 10000, 5000), "S-001-A"),
			"S-001-A": plainChild("S-001-A", "S-001", 4000, 4000),
		},
	}
	s, path := openGapLedger(t, l)

	before, _ := s.GetSample("S-001")
	rawBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Destroy(DestroyInput{
		SampleID: "S-001", Operator: "h", Location: "l",
		At: destroyGapAt, Reason: "实验结束按规程销毁",
	})
	if err == nil {
		t.Fatalf("数量有缺口时销毁必须被拒绝，却返回了成功结果: %+v", got)
	}
	if got != nil {
		t.Fatalf("销毁被拒绝时不得返回样品结果, got %+v", got)
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("缺口拒绝应可用 errors.Is 判定为 ErrConflict, got %v", err)
	}
	for _, want := range []string{"S-001", "10.000", "5.000", "4.000", "1.000", "缺口"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误说明应包含 %q: %v", want, err)
		}
	}

	// 内存状态：数量、持有人、地点、来源关系、子样列表、保管历史全不变。
	after, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	if after.Destruction != nil || after.Remaining != "5.000" ||
		after.Holder != "h" || after.Location != "l" ||
		len(after.Children) != 1 || after.Children[0] != "S-001-A" ||
		after.ParentID != before.ParentID ||
		len(after.History) != len(before.History) {
		t.Fatalf("被拒绝的销毁改动了样品: before=%+v after=%+v", before, after)
	}
	child, _ := s.GetSample("S-001-A")
	if child.InitialQty != "4.000" || child.Remaining != "4.000" || child.Destruction != nil {
		t.Fatalf("被拒绝的销毁不应改动直接子样: %+v", child)
	}

	// 文件内容原样保留：缺口没有被计入销毁量，也没有补建子样或调整初始量。
	rawAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(rawBefore) != string(rawAfter) {
		t.Fatalf("被拒绝的销毁不得改写文件")
	}

	// 再次提交仍是同样的拒绝；旧记录随后仍能重新打开并查询。
	if _, err := s.Destroy(DestroyInput{
		SampleID: "S-001", Operator: "h", Location: "l",
		At: destroyGapAt, Reason: "实验结束按规程销毁",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("缺口未补齐前重复销毁应继续拒绝, got %v", err)
	}
	assertStillOpenAndUndestroyed(t, path, "S-001", "5.000", []string{"S-001-A"})
}

// TestDestroyRejectsGapWithoutChildren 没有直接子样时剩余量必须恰好等于
// 自身初始量：差 1.000 与只差 0.001 都拒绝，直接分出总量显示 0.000。
func TestDestroyRejectsGapWithoutChildren(t *testing.T) {
	cases := []struct {
		name       string
		initial    int64
		remaining  int64
		wantInit   string
		wantRemain string
		wantGap    string
	}{
		{"缺一毫升", 5000, 4000, "5.000", "4.000", "1.000"},
		{"只差一个最小刻度", 1000, 999, "1.000", "0.999", "0.001"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := &ledger{
				Samples: map[string]*sampleRecord{
					"LEAF": activeSample("LEAF", "", c.initial, c.remaining),
				},
			}
			s, path := openGapLedger(t, l)
			_, err := s.Destroy(DestroyInput{
				SampleID: "LEAF", Operator: "h", Location: "l",
				At: destroyGapAt, Reason: "r",
			})
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("want ErrConflict, got %v", err)
			}
			for _, want := range []string{"LEAF", c.wantInit, c.wantRemain, "0.000", c.wantGap} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("错误说明应包含 %q: %v", want, err)
				}
			}
			assertStillOpenAndUndestroyed(t, path, "LEAF", c.wantRemain, nil)
		})
	}
}

// TestDestroyRejectsGapWithMultipleChildren 多份直接子样按创建时初始量
// 累加：C1 4.000 + C2 2.000 = 直接分出总量 6.000，加剩余 3.000 合计
// 9.000，距初始 10.000 缺 1.000；错误说明列出剩余 3.000、直接分出总量
// 6.000、合计 9.000 与缺口 1.000。
func TestDestroyRejectsGapWithMultipleChildren(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P":  withChild(activeSample("P", "", 10000, 3000), "C1", "C2"),
			"C1": plainChild("C1", "P", 4000, 4000),
			"C2": plainChild("C2", "P", 2000, 2000),
		},
	}
	s, path := openGapLedger(t, l)
	_, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "h", Location: "l", At: destroyGapAt, Reason: "r",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	for _, want := range []string{"P", "10.000", "3.000", "6.000", "9.000", "1.000"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误说明应包含 %q: %v", want, err)
		}
	}
	assertStillOpenAndUndestroyed(t, path, "P", "3.000", []string{"C1", "C2"})
}

// TestDestroyConservationUsesChildInitialOnly 核对只认各直接子样创建时
// 取得的初始量：
//   - 子样后来继续分装、当前只剩 1.000，父样有缺口时仍按子样初始 4.000
//     计缺口（5.000+4.000=9.000，缺 1.000），不能改用子样当前剩余量；
//   - 孙样不重复计入，也不替父样凑数；
//   - 数量恰好守恒时（6.000+4.000=10.000），即使子样已把自己的量继续
//     分出或独立销毁，父样仍正常销毁 6.000，重开后可查询。
func TestDestroyConservationUsesChildInitialOnly(t *testing.T) {
	// 场景一：子样 C 自身又分出 3.000 给孙样 G、当前只剩 1.000；父样剩余
	// 5.000 有 1.000 缺口。直接分出总量必须仍显示 4.000、缺口 1.000。
	childSplit := plainChild("C", "P", 4000, 1000)
	childSplit.Children = []string{"G"}
	childSplit.History = append(childSplit.History, historyRecord{
		Kind: "split", Time: splitCheckAt.Add(time.Hour), Holder: "李四", Location: "实验室B",
	})
	bad := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChild(activeSample("P", "", 10000, 5000), "C"),
			"C": childSplit,
			"G": plainChild("G", "C", 3000, 3000),
		},
	}
	s, path := openGapLedger(t, bad)
	_, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "h", Location: "l", At: destroyGapAt, Reason: "r",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("按子样当前剩余量会误判缺口，仍应按其创建初始量拒绝: %v", err)
	}
	for _, want := range []string{"P", "5.000", "4.000", "1.000"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误说明应包含 %q（按子样创建初始量核对）: %v", want, err)
		}
	}
	assertStillOpenAndUndestroyed(t, path, "P", "5.000", []string{"C"})

	// 场景二：父样剩余 6.000 恰好守恒，子样已分装用尽（无销毁信息）、
	// 孙样持有全部 4.000；父样销毁成功，销毁量 6.000，重开可查询。
	childExhausted := plainChild("C", "P", 4000, 0)
	childExhausted.Children = []string{"G"}
	childExhausted.History = append(childExhausted.History, historyRecord{
		Kind: "split", Time: splitCheckAt.Add(time.Hour), Holder: "李四", Location: "实验室B",
	})
	good1 := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChild(activeSample("P", "", 10000, 6000), "C"),
			"C": childExhausted,
			"G": plainChild("G", "C", 4000, 4000),
		},
	}
	s1, path1 := openGapLedger(t, good1)
	view, err := s1.Destroy(DestroyInput{
		SampleID: "P", Operator: "h", Location: "l", At: destroyGapAt, Reason: "r",
	})
	if err != nil {
		t.Fatalf("孙样不重复计入，6.000+4.000=10.000 应销毁成功: %v", err)
	}
	if view.Destruction == nil || view.Destruction.Qty != "6.000" || view.Remaining != "0.000" {
		t.Fatalf("父样销毁视图错误: %+v", view)
	}
	reopened, err := Open(path1)
	if err != nil {
		t.Fatalf("守恒销毁后的文件必须仍能重新打开（这正是修复点）: %v", err)
	}
	p, _ := reopened.GetSample("P")
	if p.Destruction == nil || p.Destruction.Qty != "6.000" || p.Remaining != "0.000" {
		t.Fatalf("重开后销毁记录应保留真实销毁量 6.000: %+v", p)
	}

	// 场景三：子样 C 已独立销毁 3.000（另分出 1.000 给孙样），父样仍按
	// 子样创建初始量 4.000 核对，6.000+4.000=10.000 销毁成功。
	childDestroyed := destroyedSampleWithParent("C", "P", 4000, 3000)
	childDestroyed.Children = []string{"G"}
	good2 := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChild(activeSample("P", "", 10000, 6000), "C"),
			"C": childDestroyed,
			"G": plainChild("G", "C", 1000, 1000),
		},
	}
	s2, path2 := openGapLedger(t, good2)
	if _, err := s2.Destroy(DestroyInput{
		SampleID: "P", Operator: "h", Location: "l", At: destroyGapAt, Reason: "r",
	}); err != nil {
		t.Fatalf("子样后来销毁不改变父样已分出量，数量守恒应销毁成功: %v", err)
	}
	reopened2, err := Open(path2)
	if err != nil {
		t.Fatalf("父子各自销毁的守恒文件应正常重开: %v", err)
	}
	p2, _ := reopened2.GetSample("P")
	c2, _ := reopened2.GetSample("C")
	if p2.Destruction == nil || p2.Destruction.Qty != "6.000" {
		t.Fatalf("父样销毁量应为 6.000: %+v", p2.Destruction)
	}
	if c2.Destruction == nil || c2.Destruction.Qty != "3.000" {
		t.Fatalf("子样销毁记录应独立保留 3.000: %+v", c2.Destruction)
	}
}

// TestDestroyConservationForSplitChildUsesOwnInitial 分装子样申请销毁时按
// 它自身创建时取得的初始量核对：自身初始 4.000、已直接分出 1.000、剩余
// 2.000 时有 1.000 缺口必须拒绝；父样 P 的数量不参与本次核对，也不被
// 改动。数量恰好守恒（剩余 3.000 + 孙样 1.000 = 4.000）时正常销毁。
func TestDestroyConservationForSplitChildUsesOwnInitial(t *testing.T) {
	// 缺口分支：P 自身守恒（6.000 + C 的 4.000 = 10.000），不替 C 凑数。
	bad := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChild(activeSample("P", "", 10000, 6000), "C"),
			"C": withChild(plainChild("C", "P", 4000, 2000), "G"),
			"G": plainChild("G", "C", 1000, 1000),
		},
	}
	s, path := openGapLedger(t, bad)
	view, err := s.Destroy(DestroyInput{
		SampleID: "C", Operator: "李四", Location: "实验室B",
		At: destroyGapAt, Reason: "r",
	})
	if err == nil {
		t.Fatalf("子样数量有缺口时必须拒绝销毁: %+v", view)
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	for _, want := range []string{"C", "4.000", "2.000", "1.000"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误说明应包含 %q: %v", want, err)
		}
	}
	// 父样与子样都保持原样，文件仍可重开。
	p, _ := s.GetSample("P")
	if p.Remaining != "6.000" || p.Destruction != nil {
		t.Fatalf("拒绝销毁子样不应改动父样: %+v", p)
	}
	assertStillOpenAndUndestroyed(t, path, "C", "2.000", []string{"G"})

	// 守恒分支：C 剩余 3.000 + G 1.000 = 自身初始 4.000，销毁 3.000。
	good := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChild(activeSample("P", "", 10000, 6000), "C"),
			"C": withChild(plainChild("C", "P", 4000, 3000), "G"),
			"G": plainChild("G", "C", 1000, 1000),
		},
	}
	s2, path2 := openGapLedger(t, good)
	cView, err := s2.Destroy(DestroyInput{
		SampleID: "C", Operator: "李四", Location: "实验室B",
		At: destroyGapAt, Reason: "r",
	})
	if err != nil {
		t.Fatalf("子样数量守恒应销毁成功: %v", err)
	}
	if cView.Destruction == nil || cView.Destruction.Qty != "3.000" || cView.Remaining != "0.000" {
		t.Fatalf("子样销毁视图错误: %+v", cView)
	}
	reopened, err := Open(path2)
	if err != nil {
		t.Fatalf("守恒销毁后的文件应正常重开: %v", err)
	}
	c3, _ := reopened.GetSample("C")
	if c3.Destruction == nil || c3.Destruction.Qty != "3.000" {
		t.Fatalf("重开后子样销毁记录应保留 3.000: %+v", c3)
	}
}

// TestDestroyConservationIndependentAcrossRoots 其他原样与兄弟子样不参与
// 核对：P 守恒时即使另一份原样 Q 有缺口，P 仍能销毁；Q 的销毁被拒绝。
func TestDestroyConservationIndependentAcrossRoots(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P":   withChild(activeSample("P", "", 10000, 6000), "P-C"),
			"P-C": plainChild("P-C", "P", 4000, 4000),
			"Q":   activeSample("Q", "", 5000, 1000),
		},
	}
	s, path := openGapLedger(t, l)

	// P 数量恰好守恒，正常销毁。
	pView, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "h", Location: "l", At: destroyGapAt, Reason: "r",
	})
	if err != nil {
		t.Fatalf("P 数量守恒不应受 Q 的缺口影响: %v", err)
	}
	if pView.Destruction == nil || pView.Destruction.Qty != "6.000" {
		t.Fatalf("P 应销毁 6.000: %+v", pView)
	}

	// Q 缺 4.000（无直接子样，剩余 1.000 != 初始 5.000），拒绝。
	qView, err := s.Destroy(DestroyInput{
		SampleID: "Q", Operator: "h", Location: "l",
		At: destroyGapAt.Add(time.Hour), Reason: "r",
	})
	if err == nil {
		t.Fatalf("Q 数量有缺口必须拒绝: %+v", qView)
	}
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "4.000") {
		t.Fatalf("Q 的缺口应为 4.000 且返回 ErrConflict: %v", err)
	}
	q, _ := s.GetSample("Q")
	if q.Destruction != nil || q.Remaining != "1.000" {
		t.Fatalf("Q 应保持未销毁、剩余 1.000: %+v", q)
	}

	// 文件仍可重开：P 带真实销毁量 6.000，Q 仍是未销毁旧记录。
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("部分销毁成功、部分旧记录保留的文件应正常重开: %v", err)
	}
	p2, _ := s2.GetSample("P")
	q2, _ := s2.GetSample("Q")
	if p2.Destruction == nil || p2.Destruction.Qty != "6.000" || p2.Remaining != "0.000" {
		t.Fatalf("P 销毁记录应保留: %+v", p2)
	}
	if q2.Destruction != nil || q2.Remaining != "1.000" {
		t.Fatalf("Q 应仍是可查询的未销毁旧记录: %+v", q2)
	}
}

// TestDestroyExactConservationViaPublicAPI 端到端合法流程：公开操作产生
// 的记录始终恰好守恒，初始 10.000、分出 4.000 后销毁剩余 6.000 成功，
// 重开后销毁记录可查询。
func TestDestroyExactConservationViaPublicAPI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exact.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return registerCheckAt }
	mustRegister(t, s, "P", "10.000", "张三", "A")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C", Qty: "4.000"},
	}}); err != nil {
		t.Fatal(err)
	}
	view, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "张三", Location: "A", At: destroyGapAt, Reason: "r",
	})
	if err != nil {
		t.Fatalf("公开操作产生的守恒记录应正常销毁: %v", err)
	}
	if view.Destruction.Qty != "6.000" || view.Remaining != "0.000" {
		t.Fatalf("应销毁真实剩余 6.000: %+v", view)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("销毁后的文件应正常重开: %v", err)
	}
	p, _ := s2.GetSample("P")
	if p.Destruction == nil || p.Destruction.Qty != "6.000" {
		t.Fatalf("重开后销毁量应为真实的 6.000: %+v", p)
	}
}

// TestDestroyRejectsInconsistentChildRelationship 销毁不仅核对数量，还要求
// 子样列表与来源编号关系一致：旧文件若漏列真正的直接子样、重复列入或列入
// 来源不符的样品，即使数量等式碰巧成立，销毁也必须拒绝——否则销毁后的
// 文件会在重新打开时因来源关系自相矛盾而失败。拒绝后记录与文件保持原样。
func TestDestroyRejectsInconsistentChildRelationship(t *testing.T) {
	// 漏列：C 的来源指向 P，P 的子样列表为空；剩余 6.000 + C 的 4.000
	// 数量守恒，但漏列必须拒绝。
	missing := &ledger{
		Samples: map[string]*sampleRecord{
			"P": activeSample("P", "", 10000, 6000),
			"C": plainChild("C", "P", 4000, 4000),
		},
	}
	s, path := openGapLedger(t, missing)
	if _, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "h", Location: "l", At: destroyGapAt, Reason: "r",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("漏列直接子样时销毁应 ErrConflict, got %v", err)
	}
	assertStillOpenAndUndestroyed(t, path, "P", "6.000", nil)

	// 重复列入：C 在列表中出现两次（JSON 数组允许重复）。
	dup := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChild(activeSample("P", "", 10000, 6000), "C", "C"),
			"C": plainChild("C", "P", 4000, 4000),
		},
	}
	s2, path2 := openGapLedger(t, dup)
	_, err := s2.Destroy(DestroyInput{
		SampleID: "P", Operator: "h", Location: "l", At: destroyGapAt, Reason: "r",
	})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("重复列入子样应 ErrConflict 并说明重复, got %v", err)
	}
	assertStillOpenAndUndestroyed(t, path2, "P", "6.000", []string{"C", "C"})

	// 来源不符：列表列入的 R 是没有来源的原样。
	wrong := &ledger{
		Samples: map[string]*sampleRecord{
			"P": withChild(activeSample("P", "", 10000, 6000), "R"),
			"R": plainChild("R", "", 4000, 4000),
		},
	}
	s3, path3 := openGapLedger(t, wrong)
	_, err = s3.Destroy(DestroyInput{
		SampleID: "P", Operator: "h", Location: "l", At: destroyGapAt, Reason: "r",
	})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "R") {
		t.Fatalf("列入来源不符的样品应 ErrConflict 并指出编号, got %v", err)
	}
	assertStillOpenAndUndestroyed(t, path3, "P", "6.000", []string{"R"})
}
