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

// splitFixture 是“来源样品已成功分出过一个子样、仍有剩余量”的测试前置状态。
type splitFixture struct {
	dir, path string
	s         *Store
	in        SplitInput

	// parentID 为来源样品，existingID 是操作前已成功分出并已独立交接出去
	// 的原有子样；c1ID/c2ID 是本次拟新建、失败时不应留下任何痕迹的子样。
	parentID, existingID, c1ID, c2ID string

	// 失败前来源样品、原有子样的查询视图与磁盘数据内容；任何保存失败
	// 之后，它们都应保持不变。
	parentBefore   *Sample
	existingBefore *Sample
	diskBefore     []byte
}

// setupPriorSplit 准备：登记 10.000 毫升原样 → 成功分出 3.250 毫升子样
// （来源剩余 6.750）→ 原有子样经交接由别人在别处持有，拥有独立的数量、
// 持有人、地点与保管历史，不应被后续对来源的操作带动。
func setupPriorSplit(t *testing.T) *splitFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	clock := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	const parentID, existingID = "P", "C0"
	mustRegister(t, s, parentID, "10.000", "张三", "实验室A")
	if _, err := s.Split(SplitInput{ParentID: parentID, Parts: []SplitPart{
		{ID: existingID, Qty: "3.250"},
	}}); err != nil {
		t.Fatalf("前置分装: %v", err)
	}

	// 原有子样交接给别人、在别处确认：它有独立于来源的持有人、地点和
	// 保管历史（分出 → 交出 → 接收）。
	hAt := clock.Add(time.Hour)
	rAt := hAt.Add(time.Hour)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-C0", SampleID: existingID,
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt,
	}); err != nil {
		t.Fatalf("前置交接发起: %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-C0", "李四", "实验室B", rAt}); err != nil {
		t.Fatalf("前置交接确认: %v", err)
	}

	// 后续分装事件使用确定且更晚的时间。
	splitAt := clock.Add(3 * time.Hour)
	s.now = func() time.Time { return splitAt }

	parentBefore, err := s.GetSample(parentID)
	if err != nil {
		t.Fatal(err)
	}
	existingBefore, err := s.GetSample(existingID)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted data: %v", err)
	}

	// 前置条件自检：来源仍有 6.750 剩余、无待确认、未销毁；原有子样
	// 独立持有，数量 3.250。
	if parentBefore.InitialQty != "10.000" || parentBefore.Remaining != "6.750" ||
		parentBefore.Holder != "张三" || parentBefore.Location != "实验室A" ||
		parentBefore.PendingTransfer != nil || parentBefore.Destruction != nil {
		t.Fatalf("前置来源样品状态不正确: %+v", parentBefore)
	}
	if len(parentBefore.Children) != 1 || parentBefore.Children[0] != existingID ||
		len(parentBefore.History) != 2 {
		t.Fatalf("前置来源的子样与历史不正确: %+v", parentBefore)
	}
	if existingBefore.InitialQty != "3.250" || existingBefore.Remaining != "3.250" ||
		existingBefore.ParentID != parentID ||
		existingBefore.Holder != "李四" || existingBefore.Location != "实验室B" ||
		len(existingBefore.History) != 3 {
		t.Fatalf("前置原有子样状态不正确: %+v", existingBefore)
	}

	const c1ID, c2ID = "C1", "C2"
	return &splitFixture{
		dir: dir, path: path, s: s,
		parentID: parentID, existingID: existingID, c1ID: c1ID, c2ID: c2ID,
		in: SplitInput{ParentID: parentID, Parts: []SplitPart{
			{ID: c1ID, Qty: "2.125"}, // 按请求顺序：先 2.125
			{ID: c2ID, Qty: "1.375"}, // 后 1.375，合计 3.500 <= 6.750
		}},
		parentBefore:   parentBefore,
		existingBefore: existingBefore,
		diskBefore:     disk,
	}
}

// assertSplitDidNotTakeEffect 断言一次保存失败的分装完全没有生效：
// 来源、原有子样逐项不变，两个拟创建子样仍不存在。
func assertSplitDidNotTakeEffect(t *testing.T, f *splitFixture) {
	t.Helper()

	parent, err := f.s.GetSample(f.parentID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if !reflect.DeepEqual(parent, f.parentBefore) {
		t.Fatalf("保存失败后来源样品状态被改动:\n before=%+v\n after =%+v",
			f.parentBefore, parent)
	}
	// 关键业务字段再逐项点明：初始量、剩余量、持有人、地点、子样顺序、历史。
	if parent.InitialQty != "10.000" || parent.Remaining != "6.750" {
		t.Fatalf("来源数量不应被扣减: init=%s remaining=%s",
			parent.InitialQty, parent.Remaining)
	}
	if parent.Holder != "张三" || parent.Location != "实验室A" {
		t.Fatalf("来源持有人和地点不应变化: %s @ %s", parent.Holder, parent.Location)
	}
	if len(parent.Children) != 1 || parent.Children[0] != f.existingID {
		t.Fatalf("来源子样列表不应变化: %v", parent.Children)
	}
	if parent.PendingTransfer != nil || parent.Destruction != nil {
		t.Fatalf("失败的分装不得引入待确认或销毁信息: %+v", parent)
	}

	existing, err := f.s.GetSample(f.existingID)
	if err != nil {
		t.Fatalf("get existing child: %v", err)
	}
	if !reflect.DeepEqual(existing, f.existingBefore) {
		t.Fatalf("原有子样不应被本次分装带动:\n before=%+v\n after =%+v",
			f.existingBefore, existing)
	}

	for _, id := range []string{f.c1ID, f.c2ID} {
		if got, err := f.s.GetSample(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("拟创建子样 %s 在失败后应仍不存在, got view=%+v err=%v", id, got, err)
		}
	}
}

// TestSplitSaveFailureIsAtomic 覆盖入参全部合法、来源也允许分装，但本地
// 保存失败时的回归：Split 必须返回说明保存失败的错误（而不是成功结果，
// 也不能归为入参非法、状态冲突或样品不存在），整次分装不生效；恢复
// 保存后用原来打开的样品数据以相同编号和数量重试，应恰好成功一次。
func TestSplitSaveFailureIsAtomic(t *testing.T) {
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
			f := setupPriorSplit(t)
			preservedPath, restore := tc.inject(t, f.dir, f.path)

			// 两个子样编号互不重复、尚未登记，数量合法且总量不超剩余，
			// 来源无待确认、未销毁——分装本身完全可做，仅保存失败。
			done, err := f.s.Split(f.in)
			if err == nil {
				t.Fatalf("保存失败时分装应返回错误，却得到成功结果: %+v", done)
			}
			if done != nil {
				t.Fatalf("保存失败不应返回表示分装成功的样品结果: %+v", done)
			}
			for _, sentinel := range []error{ErrInvalid, ErrConflict, ErrNotFound} {
				if errors.Is(err, sentinel) {
					t.Fatalf("保存错误不应被包装为 %v，实际错误: %v", sentinel, err)
				}
			}
			if !strings.Contains(err.Error(), tc.wantErrFragment) {
				t.Fatalf("应返回 %q 对应的保存错误, got %v", tc.wantErrFragment, err)
			}

			// 失败后立即查询：来源与原有子样停留在提交前，新子样不存在。
			assertSplitDidNotTakeEffect(t, f)

			// 此前保存的样品文件保持原有业务内容：不能只扣了来源数量，
			// 也不能只保存了部分子样。
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
			// 重新分装：失败既没有占用编号，也没有提前扣减，应成功。
			parent, err := f.s.Split(f.in)
			if err != nil {
				t.Fatalf("恢复后用相同编号和数量重新分装应成功: %v", err)
			}

			// 返回的来源视图：只扣减 3.500，剩余 3.250；持有人地点不变。
			if parent.InitialQty != "10.000" || parent.Remaining != "3.250" {
				t.Fatalf("成功分装后来源数量错误: init=%s remaining=%s",
					parent.InitialQty, parent.Remaining)
			}
			if parent.Holder != "张三" || parent.Location != "实验室A" {
				t.Fatalf("分装不改变来源持有人和地点: %s @ %s",
					parent.Holder, parent.Location)
			}
			wantChildren := []string{f.existingID, f.c1ID, f.c2ID}
			if !reflect.DeepEqual(parent.Children, wantChildren) {
				t.Fatalf("新子样应在原有子样后按请求顺序追加: got %v want %v",
					parent.Children, wantChildren)
			}
			// 来源只增加本次分装应有的一条历史，原有历史原样保留。
			if len(parent.History) != len(f.parentBefore.History)+1 {
				t.Fatalf("来源历史应只增加一条分装记录: before=%d after=%d",
					len(f.parentBefore.History), len(parent.History))
			}
			if !reflect.DeepEqual(parent.History[:len(f.parentBefore.History)],
				f.parentBefore.History) {
				t.Fatalf("来源原有保管历史应保持不变: %+v", parent.History)
			}
			last := parent.History[len(parent.History)-1]
			if last.Kind != "split" || last.Holder != "张三" || last.Location != "实验室A" ||
				!strings.Contains(last.Detail, f.c1ID) ||
				!strings.Contains(last.Detail, f.c2ID) {
				t.Fatalf("新增历史不是本次分装记录: %+v", last)
			}

			// 两个新子样各自指向该来源，数量即请求数量，继承来源当前的
			// 持有人和地点，各有一条分出历史；查询数量统一三位小数。
			wantParts := []struct {
				id, qty string
			}{{f.c1ID, "2.125"}, {f.c2ID, "1.375"}}
			for _, wp := range wantParts {
				c, err := f.s.GetSample(wp.id)
				if err != nil {
					t.Fatalf("新子样 %s 应可查询: %v", wp.id, err)
				}
				if c.ParentID != f.parentID {
					t.Fatalf("子样 %s 应指向来源 %s, got %q", wp.id, f.parentID, c.ParentID)
				}
				if c.InitialQty != wp.qty || c.Remaining != wp.qty {
					t.Fatalf("子样 %s 数量错误: init=%s remaining=%s want %s",
						wp.id, c.InitialQty, c.Remaining, wp.qty)
				}
				if c.Holder != "张三" || c.Location != "实验室A" {
					t.Fatalf("子样 %s 应继承来源当前持有人和地点: %s @ %s",
						wp.id, c.Holder, c.Location)
				}
				if len(c.History) != 1 || c.History[0].Kind != "split" ||
					c.History[0].Holder != "张三" || c.History[0].Location != "实验室A" ||
					!strings.Contains(c.History[0].Detail, f.parentID) {
					t.Fatalf("子样 %s 的保管历史不正确: %+v", wp.id, c.History)
				}
			}

			// 原有子样的数量、来源和保管记录不受本次成功分装带动。
			existing, err := f.s.GetSample(f.existingID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(existing, f.existingBefore) {
				t.Fatalf("原有子样不应被成功分装带动:\n before=%+v\n after =%+v",
					f.existingBefore, existing)
			}

			// 成功的重试已正常落盘：重开后数量、子样顺序、继承关系与
			// 各自保管历史都保持。
			s2, err := Open(f.path)
			if err != nil {
				t.Fatalf("reopen after retry: %v", err)
			}
			reopenedParent, err := s2.GetSample(f.parentID)
			if err != nil {
				t.Fatal(err)
			}
			if reopenedParent.InitialQty != "10.000" ||
				reopenedParent.Remaining != "3.250" ||
				!reflect.DeepEqual(reopenedParent.Children, wantChildren) ||
				len(reopenedParent.History) != len(parent.History) {
				t.Fatalf("重开后来源状态错误: %+v", reopenedParent)
			}
			for _, wp := range wantParts {
				c, err := s2.GetSample(wp.id)
				if err != nil {
					t.Fatalf("重开后子样 %s 应仍存在: %v", wp.id, err)
				}
				if c.ParentID != f.parentID || c.Remaining != wp.qty ||
					c.Holder != "张三" || c.Location != "实验室A" {
					t.Fatalf("重开后子样 %s 状态错误: %+v", wp.id, c)
				}
			}
			reopenedExisting, err := s2.GetSample(f.existingID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reopenedExisting, f.existingBefore) {
				t.Fatalf("重开后原有子样应保持独立状态:\n before=%+v\n after =%+v",
					f.existingBefore, reopenedExisting)
			}
		})
	}
}
