package custody

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// splitSaveFailureFixture 是“来源样品已有一次成功分装，仍可继续分装”的
// 测试前置状态：P 初始 10.000 毫升，已分出子样 C0（3.250 毫升），
// 剩余 6.750 毫升，没有待确认交接，也未销毁。
type splitSaveFailureFixture struct {
	dir, path string
	s         *Store
	in        SplitInput

	// 失败分装提交前的来源与已有子样视图，以及磁盘上已保存的数据内容；
	// 任何保存失败之后，它们都应保持不变。
	parentBefore *Sample
	childBefore  *Sample
	diskBefore   []byte
}

// setupSplittableParent 准备：登记 P（10.000 毫升）→ 成功分出 C0（3.250 毫升）。
// 本次要尝试的分装（C1=2.125、C2=1.375，合计 3.500 毫升）入参全部合法，
// 编号互不重复且尚未登记，总量不超过剩余量。
func setupSplittableParent(t *testing.T) *splitSaveFailureFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	clock := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	mustRegister(t, s, "P", "10.000", "张三", "实验室A")

	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C0", Qty: "3.250"},
	}}); err != nil {
		t.Fatalf("pre-split: %v", err)
	}

	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted data: %v", err)
	}
	parentBefore, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	childBefore, err := s.GetSample("C0")
	if err != nil {
		t.Fatal(err)
	}
	// 前置条件自检：剩余 6.750 毫升，已有子样 C0，无待确认交接、未销毁。
	if parentBefore.InitialQty != "10.000" || parentBefore.Remaining != "6.750" ||
		parentBefore.Holder != "张三" || parentBefore.Location != "实验室A" ||
		len(parentBefore.Children) != 1 || parentBefore.Children[0] != "C0" ||
		parentBefore.PendingTransfer != nil || parentBefore.Destruction != nil {
		t.Fatalf("前置来源样品状态不正确: %+v", parentBefore)
	}
	if childBefore.ParentID != "P" || childBefore.InitialQty != "3.250" ||
		childBefore.Remaining != "3.250" {
		t.Fatalf("前置子样状态不正确: %+v", childBefore)
	}
	for _, id := range []string{"C1", "C2"} {
		if _, err := s.GetSample(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("拟创建的子样 %s 应尚未登记, got %v", id, err)
		}
	}

	return &splitSaveFailureFixture{
		dir: dir, path: path, s: s,
		in: SplitInput{ParentID: "P", Parts: []SplitPart{
			{ID: "C1", Qty: "2.125"},
			{ID: "C2", Qty: "1.375"},
		}},
		parentBefore: parentBefore,
		childBefore:  childBefore,
		diskBefore:   disk,
	}
}

// assertSplitNotApplied 断言整次分装没有生效：来源样品的初始量、剩余量、
// 持有人、地点、子样列表和保管历史都与提交前一致；已有子样 C0 不受影响；
// 拟创建的 C1、C2 仍不存在。
func assertSplitNotApplied(t *testing.T, s *Store, f *splitSaveFailureFixture) {
	t.Helper()

	parent, err := s.GetSample("P")
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if !reflect.DeepEqual(parent, f.parentBefore) {
		t.Fatalf("保存失败后来源样品状态被改动:\n before=%+v\n after =%+v",
			f.parentBefore, parent)
	}
	if parent.InitialQty != "10.000" || parent.Remaining != "6.750" {
		t.Fatalf("来源数量应保持不变, got init=%s rem=%s",
			parent.InitialQty, parent.Remaining)
	}

	child, err := s.GetSample("C0")
	if err != nil {
		t.Fatalf("get existing child: %v", err)
	}
	if !reflect.DeepEqual(child, f.childBefore) {
		t.Fatalf("保存失败后已有子样被改动:\n before=%+v\n after =%+v",
			f.childBefore, child)
	}

	for _, id := range []string{"C1", "C2"} {
		if _, err := s.GetSample(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("保存失败后子样 %s 仍应不存在（ErrNotFound）, got %v", id, err)
		}
	}
}

// TestSplitSaveFailureKeepsState 覆盖入参全部合法、来源也允许分装，但分装
// 结果无法保存到本地时的回归：分装必须返回保存错误而不是成功视图，也不能
// 归为入参非法、状态冲突或样品不存在；内存与磁盘上的状态都固定在提交前，
// 不出现只扣了来源数量或只保存了部分子样的状态。恢复访问后用原来打开的
// 数据、以相同的子样编号和数量重试，恰好成功一次。
func TestSplitSaveFailureKeepsState(t *testing.T) {
	cases := []struct {
		name string
		// wantErrFragment 是对应保存环节的错误文本，用于区分两类失败。
		wantErrFragment string
		// inject 制造一次保存失败，返回失败期间原数据所在的可读路径，
		// 以及恢复函数（幂等，同时注册到 t.Cleanup）。
		inject func(t *testing.T, dir, path string) (preservedPath string, restore func())
	}{
		{
			name:            "数据目录无法写入",
			wantErrFragment: "创建临时数据文件失败",
			inject: func(t *testing.T, dir, path string) (string, func()) {
				if err := os.Chmod(dir, 0o555); err != nil {
					t.Fatalf("chmod dir: %v", err)
				}
				restored := false
				restore := func() {
					if restored {
						return
					}
					restored = true
					_ = os.Chmod(dir, 0o755)
				}
				t.Cleanup(restore)
				return path, restore
			},
		},
		{
			name:            "既有数据文件无法被替换",
			wantErrFragment: "替换本地样品数据文件失败",
			inject: func(t *testing.T, dir, path string) (string, func()) {
				// 把原数据文件移开并在同一路径放置一个目录：临时文件可以
				// 正常写入同目录，但原子 rename 无法用文件替换一个目录。
				aside := path + ".preserved"
				if err := os.Rename(path, aside); err != nil {
					t.Fatalf("move data file aside: %v", err)
				}
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatalf("place directory at data path: %v", err)
				}
				restored := false
				restore := func() {
					if restored {
						return
					}
					restored = true
					if _, err := os.Lstat(path); err == nil {
						_ = os.RemoveAll(path)
					}
					_ = os.Rename(aside, path)
				}
				t.Cleanup(restore)
				return aside, restore
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setupSplittableParent(t)
			preservedPath, restore := tc.inject(t, f.dir, f.path)

			// 子样编号、数量都合法，来源也允许分装，但本次保存失败：
			// 必须返回错误，不能返回表示分装成功的样品结果。
			done, err := f.s.Split(f.in)
			if err == nil {
				t.Fatalf("保存失败时分装应返回错误，却得到成功结果: %+v", done)
			}
			if done != nil {
				t.Fatalf("保存失败不应返回成功视图: %+v", done)
			}
			for _, sentinel := range []error{ErrInvalid, ErrConflict, ErrNotFound} {
				if errors.Is(err, sentinel) {
					t.Fatalf("保存错误不应被包装为 %v，实际错误: %v", sentinel, err)
				}
			}
			if !strings.Contains(err.Error(), tc.wantErrFragment) {
				t.Fatalf("应返回 %q 对应的保存错误, got %v", tc.wantErrFragment, err)
			}

			// 保存失败后立即查询，来源与已有子样都停留在提交前，
			// 两个拟创建的子样仍不存在。
			assertSplitNotApplied(t, f.s, f)

			// 此前保存的样品文件保持原有业务内容，失败没有写入半截数据。
			preserved, err := os.ReadFile(preservedPath)
			if err != nil {
				t.Fatalf("读取原数据文件: %v", err)
			}
			if !bytes.Equal(preserved, f.diskBefore) {
				t.Fatalf("保存失败改动了磁盘上的原数据文件")
			}

			// 恢复正常文件访问：数据文件回到原路径，内容仍是提交前的状态。
			restore()
			disk, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatalf("恢复后读取数据文件: %v", err)
			}
			if !bytes.Equal(disk, f.diskBefore) {
				t.Fatalf("恢复后数据文件内容应与失败前一致")
			}

			// 仍通过原来打开的样品数据，用失败时相同的子样编号和数量
			// 重新分装：不能因那次失败占用了编号或提前扣减而被拒绝。
			done, err = f.s.Split(f.in)
			if err != nil {
				t.Fatalf("恢复后用相同编号和数量重试应成功: %v", err)
			}

			// 此次成功只扣减 2.125 + 1.375 = 3.500 毫升，来源剩余 3.250 毫升。
			if done.InitialQty != "10.000" || done.Remaining != "3.250" {
				t.Fatalf("重试后来源数量错误, got init=%s rem=%s",
					done.InitialQty, done.Remaining)
			}
			// 在原有子样列表后依请求顺序追加 C1、C2。
			wantChildren := []string{"C0", "C1", "C2"}
			if !reflect.DeepEqual(done.Children, wantChildren) {
				t.Fatalf("子样列表应为 %v, got %v", wantChildren, done.Children)
			}
			// 历史只增加本次分装应有的一条记录，原有记录保持不变。
			if len(done.History) != len(f.parentBefore.History)+1 {
				t.Fatalf("历史应只增加一条分装记录: before=%d after=%d",
					len(f.parentBefore.History), len(done.History))
			}
			if !reflect.DeepEqual(done.History[:len(f.parentBefore.History)],
				f.parentBefore.History) {
				t.Fatalf("原有保管历史应保持不变: %+v", done.History)
			}
			last := done.History[len(done.History)-1]
			if last.Kind != "split" || last.Holder != "张三" || last.Location != "实验室A" ||
				!strings.Contains(last.Detail, "C1") || !strings.Contains(last.Detail, "C2") {
				t.Fatalf("新增历史不是本次分装记录: %+v", last)
			}

			// 新子样各自指向来源 P，继承来源当前的持有人和地点，
			// 数量统一显示三位小数。
			for id, qty := range map[string]string{"C1": "2.125", "C2": "1.375"} {
				c, err := f.s.GetSample(id)
				if err != nil {
					t.Fatalf("重试后子样 %s 应存在: %v", id, err)
				}
				if c.ParentID != "P" || c.Holder != "张三" || c.Location != "实验室A" {
					t.Fatalf("子样 %s 未正确指向来源并继承保管位置: %+v", id, c)
				}
				if c.InitialQty != qty || c.Remaining != qty {
					t.Fatalf("子样 %s 数量应为 %s, got init=%s rem=%s",
						id, qty, c.InitialQty, c.Remaining)
				}
				if len(c.History) != 1 || c.History[0].Kind != "split" {
					t.Fatalf("子样 %s 应只有一条分装历史: %+v", id, c.History)
				}
			}

			// 原有子样 C0 的数量、来源和保管记录不被本次操作带动。
			child, err := f.s.GetSample("C0")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(child, f.childBefore) {
				t.Fatalf("已有子样不应被本次分装改动:\n before=%+v\n after =%+v",
					f.childBefore, child)
			}

			// 成功的重试已正常落盘：重新打开后状态保持。
			s2, err := Open(f.path)
			if err != nil {
				t.Fatalf("reopen after retry: %v", err)
			}
			reopened, err := s2.GetSample("P")
			if err != nil {
				t.Fatal(err)
			}
			if reopened.Remaining != "3.250" ||
				!reflect.DeepEqual(reopened.Children, wantChildren) ||
				len(reopened.History) != len(done.History) {
				t.Fatalf("重开后来源样品状态应保留: %+v", reopened)
			}
			for _, id := range []string{"C1", "C2"} {
				if _, err := s2.GetSample(id); err != nil {
					t.Fatalf("重开后子样 %s 应保留: %v", id, err)
				}
			}
		})
	}
}
