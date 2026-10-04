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

// handoverSaveFixture 是“原样已成功分出过子样、仍有剩余量、没有待确认
// 交接，当前持有人在记录地点提交的交出请求本身完全合法”的测试前置状态。
type handoverSaveFixture struct {
	dir, path string
	s         *Store
	in        HandoverInput
	hAt       time.Time

	// parentID 为待交出原样，transferID 是尚未占用的本次交接编号；childID
	// 是此前已成功分出、并已独立交接给别人在别处持有的原有子样，原样交接
	// 的失败与重试都不得带动它。
	parentID, childID, transferID string

	// 失败前原样、原有子样的查询视图与磁盘数据内容；任何保存失败
	// 之后，它们都应保持不变。
	parentBefore *Sample
	childBefore  *Sample
	diskBefore   []byte
}

// setupHandoverableParent 准备：登记 10.000 毫升原样 → 成功分出 3.250 毫升
// 子样（原样剩余 6.750）→ 子样经交接由别人在别处确认持有。原样无待确认
// 交接、未销毁，当前持有人仍在记录地点，拟用的交接编号尚未占用。
func setupHandoverableParent(t *testing.T) *handoverSaveFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	clock := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	const parentID, childID, transferID = "P", "C0", "TR-P"
	mustRegister(t, s, parentID, "10.000", "张三", "实验室A")
	if _, err := s.Split(SplitInput{ParentID: parentID, Parts: []SplitPart{
		{ID: childID, Qty: "3.250"},
	}}); err != nil {
		t.Fatalf("前置分装: %v", err)
	}

	// 原有子样交接给别人、在别处确认：它有独立于原样的数量、持有人、
	// 地点和保管历史（分出 → 交出 → 接收）。
	cHAt := clock.Add(time.Hour)
	cRAt := cHAt.Add(time.Hour)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-C0", SampleID: childID,
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: cHAt,
	}); err != nil {
		t.Fatalf("前置交接发起: %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-C0", "李四", "实验室B", cRAt}); err != nil {
		t.Fatalf("前置交接确认: %v", err)
	}

	// 本次交出时间晚于全部已有历史，请求本身完全合法。
	hAt := clock.Add(4 * time.Hour)

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
	// 持有人和地点仍是登记记录；拟用交接编号尚未占用；原有子样独立持有。
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
	if _, err := s.GetTransfer(transferID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("前置交接编号 %q 应尚未占用", transferID)
	}

	return &handoverSaveFixture{
		dir: dir, path: path, s: s, hAt: hAt,
		parentID: parentID, childID: childID, transferID: transferID,
		in: HandoverInput{
			TransferID: transferID, SampleID: parentID,
			FromHolder: "张三", FromLocation: "实验室A",
			ToHolder: "王五", ToLocation: "实验室C",
			HandedOverAt: hAt,
		},
		parentBefore: parentBefore,
		childBefore:  childBefore,
		diskBefore:   disk,
	}
}

// assertHandoverDidNotTakeEffect 断言一次保存失败的交接完全没有生效：
// 原样逐项保持提交前状态、没有待确认详情与本次交出历史，交接编号仍未
// 占用，原有子样保持自己的数量与保管状态。
func assertHandoverDidNotTakeEffect(t *testing.T, f *handoverSaveFixture) {
	t.Helper()

	parent, err := f.s.GetSample(f.parentID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if !reflect.DeepEqual(parent, f.parentBefore) {
		t.Fatalf("保存失败后原样状态被改动:\n before=%+v\n after =%+v",
			f.parentBefore, parent)
	}
	// 关键业务字段再逐项点明：初始量、剩余量、持有人、地点、子样关系。
	if parent.InitialQty != "10.000" || parent.Remaining != "6.750" {
		t.Fatalf("原样数量不应被失败的交接改动: init=%s remaining=%s",
			parent.InitialQty, parent.Remaining)
	}
	if parent.Holder != "张三" || parent.Location != "实验室A" {
		t.Fatalf("原样持有人和地点不应变化: %s @ %s", parent.Holder, parent.Location)
	}
	if len(parent.Children) != 1 || parent.Children[0] != f.childID {
		t.Fatalf("原样子样关系不应变化: %v", parent.Children)
	}
	if parent.PendingTransfer != nil {
		t.Fatalf("失败的交接不得留下待确认详情（不能让重试被误当成已发起）: %+v",
			parent.PendingTransfer)
	}
	if parent.Destruction != nil {
		t.Fatalf("失败的交接不得引入销毁信息: %+v", parent.Destruction)
	}
	// 保管历史中不得出现本次交出事件，长度与内容都停留在提交前。
	if len(parent.History) != len(f.parentBefore.History) {
		t.Fatalf("失败的交接不得追加历史: before=%d after=%d",
			len(f.parentBefore.History), len(parent.History))
	}
	for _, h := range parent.History {
		if h.Kind == "transfer-out" && strings.Contains(h.Detail, f.transferID) {
			t.Fatalf("保管历史中不应出现本次交出事件: %+v", h)
		}
	}

	// 交接记录完全不存在：按编号查询必须得到记录不存在错误。
	if tr, err := f.s.GetTransfer(f.transferID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败的交接不应占用编号 %s, got view=%+v err=%v",
			f.transferID, tr, err)
	}

	// 已分出的子样仍保留自己的数量与保管状态，不随原样进入待确认。
	child, err := f.s.GetSample(f.childID)
	if err != nil {
		t.Fatalf("get existing child: %v", err)
	}
	if !reflect.DeepEqual(child, f.childBefore) {
		t.Fatalf("原有子样不应被原样的失败交接带动:\n before=%+v\n after =%+v",
			f.childBefore, child)
	}
	if child.PendingTransfer != nil {
		t.Fatalf("子样不应随原样进入待确认状态: %+v", child.PendingTransfer)
	}
}

// TestHandoverSaveFailureIsAtomic 覆盖交出信息全部合法、样品也允许交接，
// 但本地保存失败时的回归：Handover 必须返回说明保存失败的错误（而不是
// 成功交接视图，也不能归为入参非法、记录不存在或状态冲突），交接编号在
// 保存成功前不得被占用，原样不出现待确认详情、不追加交出历史，磁盘上不
// 留下交接记录、历史或待确认详情的部分结果；保存未恢复时重提同一请求仍
// 报保存失败；恢复保存后继续用原来打开的数据以相同编号和交出信息重试，
// 应恰好生成一条待确认交接，交接量为原样当时全部剩余量 6.750 毫升。
func TestHandoverSaveFailureIsAtomic(t *testing.T) {
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
			f := setupHandoverableParent(t)
			preservedPath, restore := tc.inject(t, f.dir, f.path)

			// 交出人、交出地点与当前记录一致，接收人不同，样品仍有 6.750
			// 剩余、无待确认、未销毁——交接本身完全可做，仅保存失败。
			done, err := f.s.Handover(f.in)
			if err == nil {
				t.Fatalf("保存失败时发起交接应返回错误，却得到成功结果: %+v", done)
			}
			if done != nil {
				t.Fatalf("保存失败不应返回表示交接成功的视图: %+v", done)
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

			// 失败后立即从原来打开的数据查询：原样与原有子样停留在提交前，
			// 交接编号未被占用，按编号查询得到记录不存在错误。
			assertHandoverDidNotTakeEffect(t, f)

			// 保存仍未恢复时再次提交同一份交出信息：必须再次报保存失败，
			// 既不能被当作“上一次交接已发起”的成功重复提交返回，也不能因
			// 编号被提前占用而按状态冲突拒绝。
			done2, err2 := f.s.Handover(f.in)
			if err2 == nil || done2 != nil {
				t.Fatalf("保存未恢复时的重试也必须失败且不返回成功视图: view=%+v err=%v",
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
			assertHandoverDidNotTakeEffect(t, f)

			// 此前保存的数据文件保持原有业务内容：交接记录、交出历史与
			// 待确认详情不能只保存其中一部分。
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

			// 继续使用原来打开的样品数据，以相同编号和交出信息再次发起：
			// 应成功生成一条待确认交接，交接量是原样当时全部剩余的 6.750。
			tr, err := f.s.Handover(f.in)
			if err != nil {
				t.Fatalf("恢复后用相同编号和交出信息重新发起应成功: %v", err)
			}
			if tr.TransferID != f.transferID || tr.SampleID != f.parentID {
				t.Fatalf("成功交接视图编号/样品错误: %+v", tr)
			}
			if tr.Qty != "6.750" {
				t.Fatalf("交接量应为原样当时全部剩余量 6.750, got %s", tr.Qty)
			}
			if tr.FromHolder != "张三" || tr.FromLocation != "实验室A" ||
				tr.ToHolder != "王五" || tr.ToLocation != "实验室C" ||
				!tr.HandedOverAt.Equal(f.hAt) {
				t.Fatalf("成功交接视图应保留提交的交出信息: %+v", tr)
			}
			if tr.Confirmed || tr.ReceivedAt != nil || tr.ConfirmedBy != "" {
				t.Fatalf("发起后应处于待确认，不能显示已确认: %+v", tr)
			}

			// 原样的持有人、地点和剩余量仍保持原值，查询中的待确认详情
			// 指向这次交接。
			parent, err := f.s.GetSample(f.parentID)
			if err != nil {
				t.Fatal(err)
			}
			if parent.InitialQty != "10.000" || parent.Remaining != "6.750" {
				t.Fatalf("发起交接不改变原样数量: init=%s remaining=%s",
					parent.InitialQty, parent.Remaining)
			}
			if parent.Holder != "张三" || parent.Location != "实验室A" {
				t.Fatalf("待确认期间原样持有人和地点保持原值: %s @ %s",
					parent.Holder, parent.Location)
			}
			if len(parent.Children) != 1 || parent.Children[0] != f.childID {
				t.Fatalf("原样子样关系不应变化: %v", parent.Children)
			}
			pt := parent.PendingTransfer
			if pt == nil {
				t.Fatalf("发起后查询应能看到指向 %s 的待确认详情", f.transferID)
			}
			if pt.TransferID != f.transferID || pt.Confirmed ||
				pt.FromHolder != "张三" || pt.FromLocation != "实验室A" ||
				pt.ToHolder != "王五" || pt.ToLocation != "实验室C" ||
				pt.Qty != "6.750" || !pt.HandedOverAt.Equal(f.hAt) {
				t.Fatalf("待确认详情应指向本次交接且内容与提交一致: %+v", pt)
			}

			// 原有历史后只追加一次交出事件；此前的失败尝试不留下额外历史。
			if len(parent.History) != len(f.parentBefore.History)+1 {
				t.Fatalf("原样历史应只增加一次交出记录: before=%d after=%d",
					len(f.parentBefore.History), len(parent.History))
			}
			if !reflect.DeepEqual(parent.History[:len(f.parentBefore.History)],
				f.parentBefore.History) {
				t.Fatalf("原样原有保管历史应保持不变: %+v", parent.History)
			}
			last := parent.History[len(parent.History)-1]
			if last.Kind != "transfer-out" || !last.Time.Equal(f.hAt) ||
				last.Holder != "张三" || last.Location != "实验室A" ||
				!strings.Contains(last.Detail, f.transferID) ||
				!strings.Contains(last.Detail, "王五") ||
				!strings.Contains(last.Detail, "实验室C") {
				t.Fatalf("新增历史不是本次交出记录: %+v", last)
			}

			// 交接记录可按编号查询到，且就是待确认的这一条。
			got, err := f.s.GetTransfer(f.transferID)
			if err != nil {
				t.Fatalf("成功发起后交接 %s 应可查询: %v", f.transferID, err)
			}
			if !reflect.DeepEqual(got, tr) {
				t.Fatalf("交接查询结果应与发起返回一致:\n handover=%+v\n query   =%+v", tr, got)
			}

			// 已分出的子样仍保留自己的数量和保管状态，不随原样进入待确认。
			child, err := f.s.GetSample(f.childID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(child, f.childBefore) {
				t.Fatalf("原有子样不应被原样交接带动:\n before=%+v\n after =%+v",
					f.childBefore, child)
			}

			// 相同交出信息立即重复提交：幂等返回这条待确认交接，不另建
			// 记录、不再次追加历史。
			again, err := f.s.Handover(HandoverInput{
				TransferID: " TR-P ", SampleID: " P ",
				FromHolder: " 张三 ", FromLocation: " 实验室A ",
				ToHolder: " 王五 ", ToLocation: " 实验室C ",
				HandedOverAt: f.hAt.In(time.FixedZone("UTC+9", 9*60*60)),
			})
			if err != nil {
				t.Fatalf("相同交出信息的重复提交应幂等返回: %v", err)
			}
			if !reflect.DeepEqual(again, tr) {
				t.Fatalf("重复提交应返回与首次成功相同的结果:\n first=%+v\n again=%+v",
					tr, again)
			}
			parentAgain, _ := f.s.GetSample(f.parentID)
			if len(parentAgain.History) != len(parent.History) {
				t.Fatalf("重复提交不得再次追加历史: first=%d again=%d",
					len(parent.History), len(parentAgain.History))
			}

			// 成功的重试已正常落盘：重开后待确认详情、交接记录、数量与
			// 各自保管历史都保持，子样仍独立持有 3.250 毫升。
			s2, err := Open(f.path)
			if err != nil {
				t.Fatalf("reopen after retry: %v", err)
			}
			reopened, err := s2.GetSample(f.parentID)
			if err != nil {
				t.Fatal(err)
			}
			if reopened.InitialQty != "10.000" || reopened.Remaining != "6.750" ||
				reopened.Holder != "张三" || reopened.Location != "实验室A" ||
				len(reopened.Children) != 1 || reopened.Children[0] != f.childID ||
				len(reopened.History) != len(parent.History) ||
				reopened.PendingTransfer == nil ||
				reopened.PendingTransfer.TransferID != f.transferID ||
				reopened.PendingTransfer.Qty != "6.750" ||
				reopened.PendingTransfer.Confirmed {
				t.Fatalf("重开后原样待确认状态错误: %+v", reopened)
			}
			reopenedTransfer, err := s2.GetTransfer(f.transferID)
			if err != nil {
				t.Fatal(err)
			}
			if reopenedTransfer.Confirmed || reopenedTransfer.Qty != "6.750" ||
				reopenedTransfer.ToHolder != "王五" {
				t.Fatalf("重开后交接记录应仍是待确认的本次交接: %+v", reopenedTransfer)
			}
			reopenedChild, err := s2.GetSample(f.childID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reopenedChild, f.childBefore) {
				t.Fatalf("重开后原有子样应保持独立状态:\n before=%+v\n after =%+v",
					f.childBefore, reopenedChild)
			}
		})
	}
}
