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

// handoverSaveFixture 是“原样已分出过子样、仍有剩余量、没有待确认交接，
// 当前持有人在记录地点提交的交出请求本身完全合法”的测试前置状态。
type handoverSaveFixture struct {
	dir, path string
	s         *Store
	in        HandoverInput
	hAt       time.Time

	// parentID 为待交出原样；transferID 是本次拟发起、失败时不应被占用的
	// 交接编号；childID 是此前已成功分出、并已独立交接给别人在别处持有的
	// 原有子样，对原样的失败交接不得带动它。
	parentID, childID, transferID string

	// 失败前原样、原有子样的查询视图与磁盘数据内容；任何保存失败
	// 之后，它们都应保持不变。
	parentBefore *Sample
	childBefore  *Sample
	diskBefore   []byte
}

// setupHandoverableParent 准备：登记 10.000 毫升原样 → 成功分出 3.250 毫升
// 子样（原样剩余 6.750）→ 子样经交接由别人在别处确认持有。原样无待确认
// 交接、未销毁，当前持有人仍在记录地点；拟发起的交接接收人不同、目的地
// 点不同，交出时间晚于全部已有历史，交出信息本身完全合法。
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

	const parentID, childID, transferID = "P", "C0", "TR-NEW"
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

	// 本次交出时间晚于原样与子样的全部已有保管历史，请求本身满足交接条件。
	handAt := clock.Add(4 * time.Hour)

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

	return &handoverSaveFixture{
		dir: dir, path: path, s: s, hAt: handAt,
		parentID: parentID, childID: childID, transferID: transferID,
		in: HandoverInput{
			TransferID: transferID, SampleID: parentID,
			FromHolder: "张三", FromLocation: "实验室A",
			ToHolder: "王五", ToLocation: "实验室C",
			HandedOverAt: handAt,
		},
		parentBefore: parentBefore,
		childBefore:  childBefore,
		diskBefore:   disk,
	}
}

// assertHandoverDidNotTakeEffect 断言一次保存失败的交接完全没有生效：
// 原样逐项保持提交前状态、待确认详情与本次交出历史均不存在，按本次编号
// 查询得到记录不存在，原有子样不受带动。
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
	// 关键业务字段再逐项点明：初始量、剩余量、持有人、地点、子样列表。
	if parent.InitialQty != "10.000" || parent.Remaining != "6.750" {
		t.Fatalf("原样数量不应变化: init=%s remaining=%s",
			parent.InitialQty, parent.Remaining)
	}
	if parent.Holder != "张三" || parent.Location != "实验室A" {
		t.Fatalf("原样持有人和地点不应变化: %s @ %s", parent.Holder, parent.Location)
	}
	if len(parent.Children) != 1 || parent.Children[0] != f.childID {
		t.Fatalf("原样子样关系不应变化: %v", parent.Children)
	}
	if parent.Destruction != nil {
		t.Fatalf("失败的交接不得引入销毁信息: %+v", parent.Destruction)
	}
	if parent.PendingTransfer != nil {
		t.Fatalf("失败的交接不得让原样进入待确认状态（不能让重试被误当成已发起的交接）: %+v",
			parent.PendingTransfer)
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

	// 交接编号只有保存成功后才能被占用：失败后按编号查询必须得到记录不存在。
	if got, err := f.s.GetTransfer(f.transferID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败的交接编号 %s 应仍查询不到, got view=%+v err=%v",
			f.transferID, got, err)
	}

	child, err := f.s.GetSample(f.childID)
	if err != nil {
		t.Fatalf("get existing child: %v", err)
	}
	if !reflect.DeepEqual(child, f.childBefore) {
		t.Fatalf("原有子样不应被原样的失败交接带动:\n before=%+v\n after =%+v",
			f.childBefore, child)
	}
	if child.InitialQty != "3.250" || child.Remaining != "3.250" ||
		child.PendingTransfer != nil {
		t.Fatalf("原有子样的数量与保管状态不应变化，也不得随原样进入待确认: %+v", child)
	}
}

// TestHandoverSaveFailureIsAtomic 覆盖交出请求完全符合业务规则、但交接结果
// 无法保存到本地数据文件时的回归：Handover 必须返回说明保存失败的错误
// （而不是成功交接视图，也不能归为入参非法、记录不存在或状态冲突），交接
// 编号不被提前占用，原样不出现待确认详情和本次交出历史，磁盘上不留下交接
// 记录、历史或待确认详情中任何一部分；保存未恢复时再次提交同一请求仍报
// 保存失败；恢复保存后继续用原来打开的数据以相同编号和交出信息重试，应
// 恰好生成一条待确认交接，交接量为原样当时全部剩余的 6.750 毫升。
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
			name:            "新数据无法替换原数据文件",
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

			// 交出人、交出地点与当前记录一致，接收人不同、目的地点不同，
			// 原样仍有 6.750 剩余、无待确认、未销毁——交接本身完全可做，
			// 仅保存失败。
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
			// 本次交接编号仍不存在。
			assertHandoverDidNotTakeEffect(t, f)

			// 保存仍未恢复时再次提交同一份交出信息：必须再次报保存失败，
			// 不能被当作“上一次交接已发起”的重复提交幂等返回，也不能因为
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
				t.Fatalf("成功交接视图的编号或样品错误: %+v", tr)
			}
			if tr.Qty != "6.750" {
				t.Fatalf("交接量应为原样当时全部剩余量 6.750, got %s", tr.Qty)
			}
			if tr.FromHolder != "张三" || tr.FromLocation != "实验室A" ||
				tr.ToHolder != "王五" || tr.ToLocation != "实验室C" ||
				!tr.HandedOverAt.Equal(f.hAt) {
				t.Fatalf("成功交接视图应保留提交的人员、地点和时间: %+v", tr)
			}
			if tr.Confirmed || tr.ReceivedAt != nil || tr.ConfirmedBy != "" {
				t.Fatalf("发起后应处于待确认，不能带接收信息: %+v", tr)
			}

			parent, err := f.s.GetSample(f.parentID)
			if err != nil {
				t.Fatal(err)
			}
			// 待确认期间原样持有人、地点和剩余量都保持原值。
			if parent.InitialQty != "10.000" || parent.Remaining != "6.750" {
				t.Fatalf("发起交接不改变原样数量: init=%s remaining=%s",
					parent.InitialQty, parent.Remaining)
			}
			if parent.Holder != "张三" || parent.Location != "实验室A" {
				t.Fatalf("待确认期间原样持有人和地点应保持原值: %s @ %s",
					parent.Holder, parent.Location)
			}
			if len(parent.Children) != 1 || parent.Children[0] != f.childID {
				t.Fatalf("发起交接不应改变子样关系: %v", parent.Children)
			}
			pt := parent.PendingTransfer
			if pt == nil {
				t.Fatalf("查询中的待确认详情应指向本次交接 %s", f.transferID)
			}
			if !reflect.DeepEqual(pt, tr) {
				t.Fatalf("待确认详情应与成功交接视图一致:\n view =%+v\n pending=%+v", tr, pt)
			}
			// 原有历史后只追加一次交出事件，此前的失败尝试不留额外历史。
			if len(parent.History) != len(f.parentBefore.History)+1 {
				t.Fatalf("原样历史应只增加一条交出记录: before=%d after=%d",
					len(f.parentBefore.History), len(parent.History))
			}
			if !reflect.DeepEqual(parent.History[:len(f.parentBefore.History)],
				f.parentBefore.History) {
				t.Fatalf("原样原有保管历史应保持不变: %+v", parent.History)
			}
			last := parent.History[len(parent.History)-1]
			if last.Kind != "transfer-out" || !last.Time.Equal(f.hAt) ||
				last.Holder != "张三" || last.Location != "实验室A" ||
				!strings.Contains(last.Detail, f.transferID) {
				t.Fatalf("新增历史不是本次交出记录: %+v", last)
			}

			// 按编号查询得到的就是这条待确认交接。
			gotTR, err := f.s.GetTransfer(f.transferID)
			if err != nil {
				t.Fatalf("成功后交接 %s 应可查询: %v", f.transferID, err)
			}
			if !reflect.DeepEqual(gotTR, tr) {
				t.Fatalf("交接查询结果应与发起时视图一致:\n发起=%+v\n查询=%+v", tr, gotTR)
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

			// 相同交出信息的重复提交仍幂等返回这条待确认交接，不再追加历史。
			again, err := f.s.Handover(HandoverInput{
				TransferID: " TR-NEW ", SampleID: " P ",
				FromHolder: " 张三 ", FromLocation: " 实验室A ",
				ToHolder: " 王五 ", ToLocation: " 实验室C ",
				HandedOverAt: f.hAt.In(time.FixedZone("UTC+8", 8*3600)),
			})
			if err != nil {
				t.Fatalf("成功后相同交出信息的重复提交应幂等返回: %v", err)
			}
			if !reflect.DeepEqual(again, tr) {
				t.Fatalf("重复提交应返回与首次成功相同的结果:\n first=%+v\n again=%+v",
					tr, again)
			}
			parentAgain, _ := f.s.GetSample(f.parentID)
			if len(parentAgain.History) != len(parent.History) ||
				parentAgain.PendingTransfer == nil ||
				parentAgain.PendingTransfer.TransferID != f.transferID {
				t.Fatalf("重复提交不得再次追加历史或改动待确认状态: %+v", parentAgain)
			}

			// 成功的重试已正常落盘：重开后待确认详情、交接记录、数量与各自
			// 保管历史都保持，子样仍独立持有 3.250 毫升。
			s2, err := Open(f.path)
			if err != nil {
				t.Fatalf("reopen after retry: %v", err)
			}
			reopenedTR, err := s2.GetTransfer(f.transferID)
			if err != nil {
				t.Fatal(err)
			}
			if reopenedTR.Confirmed || reopenedTR.Qty != "6.750" ||
				reopenedTR.ToHolder != "王五" || reopenedTR.ToLocation != "实验室C" ||
				!reopenedTR.HandedOverAt.Equal(f.hAt) {
				t.Fatalf("重开后交接应保持待确认状态: %+v", reopenedTR)
			}
			reopenedParent, err := s2.GetSample(f.parentID)
			if err != nil {
				t.Fatal(err)
			}
			if reopenedParent.InitialQty != "10.000" ||
				reopenedParent.Remaining != "6.750" ||
				reopenedParent.Holder != "张三" || reopenedParent.Location != "实验室A" ||
				reopenedParent.PendingTransfer == nil ||
				reopenedParent.PendingTransfer.TransferID != f.transferID ||
				len(reopenedParent.History) != len(parent.History) {
				t.Fatalf("重开后原样保管状态错误: %+v", reopenedParent)
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
