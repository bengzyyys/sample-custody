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

// destroySaveFixture 是“原样已分出过子样、仍有剩余量、没有待确认交接且
// 未销毁，当前持有人在记录地点提交的销毁请求本身完全合法”的测试前置状态。
type destroySaveFixture struct {
	dir, path string
	s         *Store
	in        DestroyInput

	// parentID 为待销毁原样；childID 是此前已成功分出、并已独立交接给
	// 别人在别处持有的原有子样，对原样的失败销毁不得带动它。
	parentID, childID string

	// 失败前原样、原有子样的查询视图与磁盘数据内容；任何保存失败
	// 之后，它们都应保持不变。
	parentBefore *Sample
	childBefore  *Sample
	diskBefore   []byte
}

// setupDestroyableParent 准备：登记 10.000 毫升原样 → 成功分出 3.250 毫升
// 子样（原样剩余 6.750）→ 子样经交接由别人在别处持有。原样未销毁、无
// 待确认交接，当前持有人仍在记录地点，销毁时间晚于全部已有历史。
func setupDestroyableParent(t *testing.T) *destroySaveFixture {
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

	// 原有子样交接给别人、在别处确认：它有独立于原样的数量、持有人、
	// 地点和保管历史（分出 → 交出 → 接收）。
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

	// 销毁时间晚于原样与子样的全部已有保管历史，请求本身满足销毁条件。
	destroyAt := clock.Add(4 * time.Hour)

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

	// 前置条件自检：原样初始 10.000、剩余 6.750、无待确认、未销毁，
	// 持有人和地点仍是登记记录；原有子样独立持有，数量 3.250。
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
		t.Fatalf("前置原有子样状态不正确: %+v", childBefore)
	}

	return &destroySaveFixture{
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
// 原样逐项保持失败前状态、销毁信息与销毁历史均不存在，原有子样不受带动。
func assertDestroyDidNotTakeEffect(t *testing.T, f *destroySaveFixture) {
	t.Helper()

	parent, err := f.s.GetSample(f.parentID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if !reflect.DeepEqual(parent, f.parentBefore) {
		t.Fatalf("保存失败后原样状态被改动:\n before=%+v\n after =%+v",
			f.parentBefore, parent)
	}
	// 关键业务字段再逐项点明：初始量、剩余量、持有人、地点、子样列表。
	if parent.InitialQty != "10.000" || parent.Remaining != "6.750" {
		t.Fatalf("原样剩余量不应被提前清零: init=%s remaining=%s",
			parent.InitialQty, parent.Remaining)
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
	if parent.Destruction != nil {
		t.Fatalf("失败的销毁不得留下销毁信息（不能让重试被误当成已完成的销毁）: %+v",
			parent.Destruction)
	}
	// 保管历史中不得出现销毁事件，长度与内容都停留在失败前。
	if len(parent.History) != len(f.parentBefore.History) {
		t.Fatalf("失败的销毁不得追加历史: before=%d after=%d",
			len(f.parentBefore.History), len(parent.History))
	}
	for _, h := range parent.History {
		if h.Kind == "destroy" {
			t.Fatalf("保管历史中不应出现销毁事件: %+v", h)
		}
	}

	child, err := f.s.GetSample(f.childID)
	if err != nil {
		t.Fatalf("get existing child: %v", err)
	}
	if !reflect.DeepEqual(child, f.childBefore) {
		t.Fatalf("原有子样不应被原样的失败销毁带动:\n before=%+v\n after =%+v",
			f.childBefore, child)
	}
	if child.InitialQty != "3.250" || child.Remaining != "3.250" ||
		child.Destruction != nil {
		t.Fatalf("原有子样的数量与保管记录不应变化: %+v", child)
	}
}

// TestDestroySaveFailureIsAtomic 覆盖销毁请求完全符合业务规则、但结果无法
// 保存到本地数据文件时的回归：Destroy 必须返回说明保存失败的错误（而不是
// 成功结果，也不能归为入参非法、状态冲突或样品不存在），原样不会提前进入
// 已销毁状态，磁盘上不留下数量归零、只剩销毁信息或只剩新增历史的部分结果；
// 恢复保存后继续用原来打开的数据以同一份销毁信息重试，应恰好销毁当时的
// 全部剩余量（6.750 毫升）一次，再次提交仍返回这次成功结果且不追加历史。
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
			name:            "数据目录无法创建临时文件",
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
			name:            "新数据已写出但无法替换原数据文件",
			wantErrFragment: "替换本地样品数据文件失败",
			inject: func(t *testing.T, dir, path string) (string, func()) {
				// 把原数据文件移开并在同一路径放置一个目录：临时文件可以
				// 正常写入同目录（新数据已完整写出），但原子 rename 无法
				// 用文件替换一个目录。
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

			// 操作人、地点、销毁时间和原因本身都满足销毁条件，原样仍有
			// 6.750 剩余、无待确认、未销毁——销毁本身完全可做，仅保存失败。
			done, err := f.s.Destroy(f.in)
			if err == nil {
				t.Fatalf("保存失败时销毁应返回错误，却得到成功结果: %+v", done)
			}
			if done != nil {
				t.Fatalf("保存失败不应返回表示销毁成功的样品结果: %+v", done)
			}
			for _, sentinel := range []error{ErrInvalid, ErrConflict, ErrNotFound} {
				if errors.Is(err, sentinel) {
					t.Fatalf("保存错误不应被包装为 %v，否则调用方会误判能否重试，实际错误: %v",
						sentinel, err)
				}
			}
			if !strings.Contains(err.Error(), tc.wantErrFragment) {
				t.Fatalf("应返回 %q 对应的保存错误, got %v", tc.wantErrFragment, err)
			}

			// 失败后立即从原来打开的数据查询：原样与原有子样停留在提交前。
			assertDestroyDidNotTakeEffect(t, f)

			// 保存仍未恢复时再次提交同一请求：必须再次报保存失败，而不是
			// 被当作“上一次销毁已完成”幂等返回成功结果。
			done2, err2 := f.s.Destroy(f.in)
			if err2 == nil || done2 != nil {
				t.Fatalf("保存未恢复时的重试也必须失败且不返回成功结果: view=%+v err=%v",
					done2, err2)
			}
			for _, sentinel := range []error{ErrInvalid, ErrConflict, ErrNotFound} {
				if errors.Is(err2, sentinel) {
					t.Fatalf("保存未恢复时的重试不应被归为业务错误 %v: %v", sentinel, err2)
				}
			}
			if !strings.Contains(err2.Error(), tc.wantErrFragment) {
				t.Fatalf("重试仍应返回 %q 对应的保存错误, got %v",
					tc.wantErrFragment, err2)
			}
			assertDestroyDidNotTakeEffect(t, f)

			// 此前保存的数据文件保持原有业务内容：不能留下数量归零、
			// 只有销毁信息或只有新增历史的部分结果。
			preserved, err := os.ReadFile(preservedPath)
			if err != nil {
				t.Fatalf("读取原数据文件: %v", err)
			}
			if !bytes.Equal(preserved, f.diskBefore) {
				t.Fatalf("保存失败改动了磁盘上的原数据文件")
			}

			// 恢复正常文件访问：数据文件回到原路径，内容仍是失败前的状态。
			restore()
			disk, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatalf("恢复后读取数据文件: %v", err)
			}
			if !bytes.Equal(disk, f.diskBefore) {
				t.Fatalf("恢复后数据文件内容应与失败前一致")
			}

			// 继续使用原来打开的数据，重新提交同一份销毁信息：应成功销毁
			// 当时的全部剩余量 6.750（初始 10.000 − 已分出 3.250）。
			destroyed, err := f.s.Destroy(f.in)
			if err != nil {
				t.Fatalf("恢复后用相同销毁信息重新提交应成功: %v", err)
			}
			if destroyed.Remaining != "0.000" {
				t.Fatalf("成功重试后原样剩余量应为 0.000, got %s", destroyed.Remaining)
			}
			if destroyed.InitialQty != "10.000" {
				t.Fatalf("初始量应保留为 10.000, got %s", destroyed.InitialQty)
			}
			if destroyed.Destruction == nil {
				t.Fatalf("成功重试后应带销毁记录")
			}
			d := destroyed.Destruction
			if d.Qty != "6.750" {
				t.Fatalf("成功重试记录的销毁量应为当时全部剩余量 6.750, got %s", d.Qty)
			}
			if d.Operator != "张三" || d.Location != "实验室A" ||
				d.Reason != "实验结束按规程销毁" || !d.At.Equal(f.in.At) {
				t.Fatalf("销毁记录应保留提交的操作人、地点、时间和原因: %+v", d)
			}
			if destroyed.Holder != "张三" || destroyed.Location != "实验室A" {
				t.Fatalf("销毁后持有人和地点保留为销毁前的最后记录: %s @ %s",
					destroyed.Holder, destroyed.Location)
			}
			if len(destroyed.Children) != 1 || destroyed.Children[0] != f.childID {
				t.Fatalf("销毁应保留子样列表: %v", destroyed.Children)
			}
			// 原样只增加一次销毁历史，原有历史原样保留。
			if len(destroyed.History) != len(f.parentBefore.History)+1 {
				t.Fatalf("原样历史应只增加一次销毁记录: before=%d after=%d",
					len(f.parentBefore.History), len(destroyed.History))
			}
			if !reflect.DeepEqual(destroyed.History[:len(f.parentBefore.History)],
				f.parentBefore.History) {
				t.Fatalf("原样原有保管历史应保持不变: %+v", destroyed.History)
			}
			last := destroyed.History[len(destroyed.History)-1]
			if last.Kind != "destroy" || !last.Time.Equal(f.in.At) ||
				last.Holder != "张三" || last.Location != "实验室A" ||
				!strings.Contains(last.Detail, "6.750") ||
				!strings.Contains(last.Detail, "实验结束按规程销毁") {
				t.Fatalf("新增历史不是本次销毁记录: %+v", last)
			}

			// 已分出的子样仍保持自己的 3.250 毫升与保管记录，不被原样的
			// 失败与随后的成功销毁带动。
			child, err := f.s.GetSample(f.childID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(child, f.childBefore) {
				t.Fatalf("原有子样不应被原样销毁带动:\n before=%+v\n after =%+v",
					f.childBefore, child)
			}

			// 再次提交相同信息：返回这次成功保存的结果，不再追加历史。
			again, err := f.s.Destroy(DestroyInput{
				SampleID: f.parentID, Operator: " 张三 ", Location: " 实验室A ",
				At:     f.in.At.In(time.FixedZone("UTC+8", 8*3600)),
				Reason: " 实验结束按规程销毁 ",
			})
			if err != nil {
				t.Fatalf("成功后相同信息的重复提交应幂等返回: %v", err)
			}
			if !reflect.DeepEqual(again, destroyed) {
				t.Fatalf("重复提交应返回与成功重试相同的结果:\n first=%+v\n again=%+v",
					destroyed, again)
			}
			parentAgain, _ := f.s.GetSample(f.parentID)
			if len(parentAgain.History) != len(destroyed.History) {
				t.Fatalf("重复提交不得再次追加历史: first=%d again=%d",
					len(destroyed.History), len(parentAgain.History))
			}

			// 成功的重试已正常落盘：重开后销毁信息、数量、子样列表与各自
			// 保管历史都保持，子样仍独立持有 3.250 毫升。
			s2, err := Open(f.path)
			if err != nil {
				t.Fatalf("reopen after retry: %v", err)
			}
			reopened, err := s2.GetSample(f.parentID)
			if err != nil {
				t.Fatal(err)
			}
			if reopened.InitialQty != "10.000" || reopened.Remaining != "0.000" ||
				reopened.Destruction == nil || reopened.Destruction.Qty != "6.750" ||
				len(reopened.Children) != 1 || reopened.Children[0] != f.childID ||
				len(reopened.History) != len(destroyed.History) ||
				reopened.History[len(reopened.History)-1].Kind != "destroy" {
				t.Fatalf("重开后原样销毁状态错误: %+v", reopened)
			}
			reopenedChild, err := s2.GetSample(f.childID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reopenedChild, f.childBefore) {
				t.Fatalf("重开后原有子样应保持独立状态:\n before=%+v\n after =%+v",
					f.childBefore, reopenedChild)
			}
			// 重开后相同销毁请求仍幂等，改变任一项仍按冲突拒绝。
			if _, err := s2.Destroy(f.in); err != nil {
				t.Fatalf("重开后相同销毁请求应幂等返回: %v", err)
			}
			mutated := f.in
			mutated.Reason = "别的原因"
			if _, err := s2.Destroy(mutated); !errors.Is(err, ErrConflict) {
				t.Fatalf("重开后销毁幂等判断应继续生效, got %v", err)
			}
		})
	}
}
