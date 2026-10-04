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

// destroyFixture 是“原样已分出过一个子样、仍有剩余量，无待确认交接且
// 未销毁，销毁请求本身完全合法”的测试前置状态。
type destroyFixture struct {
	dir, path string
	s         *Store
	in        DestroyInput

	// parentID 为拟销毁的原样，childID 是操作前已成功分出的子样；
	// 子样有独立的数量和保管记录，不应被来源样品的一次失败销毁带动。
	parentID, childID string

	// 失败前原样、子样的查询视图与磁盘数据内容；任何保存失败之后，
	// 它们都应保持不变。
	parentBefore *Sample
	childBefore  *Sample
	diskBefore   []byte
}

// setupDestroyableParent 准备：登记 10.000 毫升原样 → 成功分出 3.250 毫升
// 子样（原样剩余 6.750）→ 子样经交接由别人在别处持有。原样仍由登记时的
// 持有人在记录地点持有，无待确认交接、未销毁，销毁入参完全满足业务规则。
func setupDestroyableParent(t *testing.T) *destroyFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	clock := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	const parentID, childID = "P", "C0"
	mustRegister(t, s, parentID, "10.000", "张三", "实验室A")
	if _, err := s.Split(SplitInput{ParentID: parentID, Parts: []SplitPart{
		{ID: childID, Qty: "3.250"},
	}}); err != nil {
		t.Fatalf("前置分装: %v", err)
	}

	// 子样交接给别人、在别处确认：它有独立于来源的持有人、地点和保管历史。
	hAt := clock.Add(time.Hour)
	rAt := hAt.Add(time.Hour)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-C0", SampleID: childID,
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt,
	}); err != nil {
		t.Fatalf("前置交接发起: %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-C0", "李四", "实验室B", rAt}); err != nil {
		t.Fatalf("前置交接确认: %v", err)
	}

	// 销毁时间晚于原样全部已有历史，操作人、地点与当前记录一致，原因非空：
	// 请求本身满足全部销毁条件，唯一的失败来源是本地保存。
	destroyAt := clock.Add(3 * time.Hour)

	parentBefore, err := s.GetSample(parentID)
	if err != nil {
		t.Fatal(err)
	}
	childBefore, err := s.GetSample(childID)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted data: %v", err)
	}

	// 前置条件自检：原样剩余 6.750、无待确认、未销毁；子样独立持有 3.250。
	if parentBefore.InitialQty != "10.000" || parentBefore.Remaining != "6.750" ||
		parentBefore.Holder != "张三" || parentBefore.Location != "实验室A" ||
		parentBefore.PendingTransfer != nil || parentBefore.Destruction != nil {
		t.Fatalf("前置原样状态不正确: %+v", parentBefore)
	}
	if len(parentBefore.Children) != 1 || parentBefore.Children[0] != childID ||
		len(parentBefore.History) != 2 {
		t.Fatalf("前置原样的子样与历史不正确: %+v", parentBefore)
	}
	if childBefore.InitialQty != "3.250" || childBefore.Remaining != "3.250" ||
		childBefore.ParentID != parentID ||
		childBefore.Holder != "李四" || childBefore.Location != "实验室B" ||
		len(childBefore.History) != 3 {
		t.Fatalf("前置子样状态不正确: %+v", childBefore)
	}

	return &destroyFixture{
		dir: dir, path: path, s: s,
		parentID: parentID, childID: childID,
		in: DestroyInput{
			SampleID: parentID, Operator: "张三", Location: "实验室A",
			At: destroyAt, Reason: "实验结束按规程销毁",
		},
		parentBefore: parentBefore,
		childBefore:  childBefore,
		diskBefore:   disk,
	}
}

// assertDestroyDidNotTakeEffect 断言一次保存失败的销毁完全没有生效：
// 原样仍保有失败前的剩余量、无销毁信息、无新增历史，初始量、持有人、
// 地点与子样列表不变；已分出的子样逐项不变。
func assertDestroyDidNotTakeEffect(t *testing.T, f *destroyFixture) {
	t.Helper()

	parent, err := f.s.GetSample(f.parentID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if !reflect.DeepEqual(parent, f.parentBefore) {
		t.Fatalf("保存失败后原样状态被改动:\n before=%+v\n after =%+v",
			f.parentBefore, parent)
	}
	// 关键业务字段再逐项点明：剩余量、销毁信息、历史、初始量、持有人、
	// 地点、子样列表。
	if parent.InitialQty != "10.000" || parent.Remaining != "6.750" {
		t.Fatalf("原样数量不应被扣减: init=%s remaining=%s",
			parent.InitialQty, parent.Remaining)
	}
	if parent.Destruction != nil {
		t.Fatalf("失败的销毁不得留下销毁信息: %+v", parent.Destruction)
	}
	if parent.Holder != "张三" || parent.Location != "实验室A" {
		t.Fatalf("原样持有人和地点不应变化: %s @ %s", parent.Holder, parent.Location)
	}
	if len(parent.Children) != 1 || parent.Children[0] != f.childID {
		t.Fatalf("原样子样列表不应变化: %v", parent.Children)
	}
	if parent.PendingTransfer != nil {
		t.Fatalf("失败的销毁不得引入待确认交接: %+v", parent.PendingTransfer)
	}
	for _, h := range parent.History {
		if h.Kind == "destroy" {
			t.Fatalf("保管历史中不应新增销毁事件: %+v", parent.History)
		}
	}

	child, err := f.s.GetSample(f.childID)
	if err != nil {
		t.Fatalf("get child: %v", err)
	}
	if !reflect.DeepEqual(child, f.childBefore) {
		t.Fatalf("已分出的子样不应被来源的失败销毁带动:\n before=%+v\n after =%+v",
			f.childBefore, child)
	}
}

// TestDestroySaveFailureIsAtomic 覆盖销毁请求完全符合业务规则、但结果无法
// 保存到本地数据文件时的回归：Destroy 必须返回说明保存失败的错误（而不是
// 成功结果，也不能归为入参非法、状态冲突或样品不存在，否则调用方会误判
// 是否可以重试），销毁完全不生效；恢复保存后用原来打开的数据以同一份
// 销毁信息重试，应恰好成功一次，重复提交仍幂等。
func TestDestroySaveFailureIsAtomic(t *testing.T) {
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
			f := setupDestroyableParent(t)
			preservedPath, restore := tc.inject(t, f.dir, f.path)

			// 操作人、地点与当前记录一致，销毁时间晚于全部已有历史，原因
			// 非空，无待确认、剩余量充足——销毁本身完全可做，仅保存失败。
			done, err := f.s.Destroy(f.in)
			if err == nil {
				t.Fatalf("保存失败时销毁应返回错误，却得到成功结果: %+v", done)
			}
			if done != nil {
				t.Fatalf("保存失败不应返回表示销毁成功的样品结果: %+v", done)
			}
			for _, sentinel := range []error{ErrInvalid, ErrConflict, ErrNotFound} {
				if errors.Is(err, sentinel) {
					t.Fatalf("保存错误不应被包装为 %v，实际错误: %v", sentinel, err)
				}
			}
			if !strings.Contains(err.Error(), tc.wantErrFragment) {
				t.Fatalf("应返回 %q 对应的保存错误, got %v", tc.wantErrFragment, err)
			}

			// 失败后立即查询：原样停留在提交前，子样不受影响。
			assertDestroyDidNotTakeEffect(t, f)

			// 此前保存的样品文件保持原有业务内容：不能留下数量归零、
			// 只有销毁信息或只有新增历史的部分结果。
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

			// 仍通过原来打开的数据，用失败时相同的一份销毁信息重新提交：
			// 失败既没有提前销毁，也没有消耗这次请求，应成功销毁当时的
			// 全部剩余量 6.750 毫升。
			parent, err := f.s.Destroy(f.in)
			if err != nil {
				t.Fatalf("恢复后用相同销毁信息重试应成功: %v", err)
			}
			if parent.InitialQty != "10.000" || parent.Remaining != "0.000" {
				t.Fatalf("成功销毁后原样数量错误: init=%s remaining=%s",
					parent.InitialQty, parent.Remaining)
			}
			if parent.Destruction == nil {
				t.Fatalf("成功销毁后应带销毁记录: %+v", parent)
			}
			d := parent.Destruction
			if d.Operator != "张三" || d.Location != "实验室A" ||
				d.Reason != "实验结束按规程销毁" || d.Qty != "6.750" ||
				!d.At.Equal(f.in.At) {
				t.Fatalf("销毁记录内容错误: %+v", d)
			}
			// 持有人和地点保留为销毁前的最后记录，子样列表保留。
			if parent.Holder != "张三" || parent.Location != "实验室A" {
				t.Fatalf("销毁不应改变持有人和地点: %s @ %s",
					parent.Holder, parent.Location)
			}
			if len(parent.Children) != 1 || parent.Children[0] != f.childID {
				t.Fatalf("销毁应保留子样列表: %v", parent.Children)
			}
			// 只增加一次销毁历史，原有历史原样保留。
			if len(parent.History) != len(f.parentBefore.History)+1 {
				t.Fatalf("原样历史应只增加一条销毁记录: before=%d after=%d",
					len(f.parentBefore.History), len(parent.History))
			}
			if !reflect.DeepEqual(parent.History[:len(f.parentBefore.History)],
				f.parentBefore.History) {
				t.Fatalf("原样原有保管历史应保持不变: %+v", parent.History)
			}
			last := parent.History[len(parent.History)-1]
			if last.Kind != "destroy" || !last.Time.Equal(f.in.At) ||
				last.Holder != "张三" || last.Location != "实验室A" ||
				!strings.Contains(last.Detail, "6.750") {
				t.Fatalf("新增历史不是本次销毁记录: %+v", last)
			}

			// 已分出的子样仍保持 3.250 毫升和自己的保管记录。
			child, err := f.s.GetSample(f.childID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(child, f.childBefore) {
				t.Fatalf("子样不应被来源的成功销毁带动:\n before=%+v\n after =%+v",
					f.childBefore, child)
			}

			// 再次提交相同信息仍返回这次成功保存的结果，不再追加历史。
			again, err := f.s.Destroy(f.in)
			if err != nil {
				t.Fatalf("重复提交相同销毁信息应幂等返回原结果: %v", err)
			}
			if !reflect.DeepEqual(again, parent) {
				t.Fatalf("重复提交应返回与首次成功相同的结果:\n first=%+v\n again=%+v",
					parent, again)
			}
			if len(again.History) != len(parent.History) {
				t.Fatalf("重复提交不得再次追加历史: first=%d again=%d",
					len(parent.History), len(again.History))
			}

			// 成功的重试已正常落盘：重开后销毁信息、剩余量与历史保持，
			// 子样状态也保持独立。
			s2, err := Open(f.path)
			if err != nil {
				t.Fatalf("reopen after retry: %v", err)
			}
			reopenedParent, err := s2.GetSample(f.parentID)
			if err != nil {
				t.Fatal(err)
			}
			if reopenedParent.Remaining != "0.000" ||
				reopenedParent.Destruction == nil ||
				reopenedParent.Destruction.Qty != "6.750" ||
				len(reopenedParent.History) != len(parent.History) {
				t.Fatalf("重开后原样销毁状态错误: %+v", reopenedParent)
			}
			reopenedChild, err := s2.GetSample(f.childID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reopenedChild, f.childBefore) {
				t.Fatalf("重开后子样应保持独立状态:\n before=%+v\n after =%+v",
					f.childBefore, reopenedChild)
			}
		})
	}
}
