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

// registerSaveFixture 是“本地样品数据已打开并有有效记录，其中已有样品
// 正在等待指定接收人确认交接；拟登记的原样编号未被使用、数量和保管信息
// 都合法”的测试前置状态。
type registerSaveFixture struct {
	dir, path string
	s         *Store
	in        RegisterInput
	clock     time.Time

	// existingID 为已有样品，transferID 是它正在等待确认的交接编号；
	// newID 是尚未使用、拟登记的编号。失败登记的任何结果都不得混入
	// 已有样品与已有交接。
	existingID, transferID, newID string

	// 失败前已有样品的查询视图、待确认交接的查询视图与磁盘数据内容；
	// 任何保存失败之后，它们都应保持不变。
	existingBefore *Sample
	transferBefore *TransferView
	diskBefore     []byte
}

// setupPendingExisting 准备：登记 4.250 毫升原样 E → 发起交接 TR-E 给李四
// 在实验室B接收，交接保持待确认。拟登记的编号 R-100 尚未使用。
func setupPendingExisting(t *testing.T) *registerSaveFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	clock := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	const existingID, transferID, newID = "E", "TR-E", "R-100"
	mustRegister(t, s, existingID, "4.250", "张三", "实验室A")
	hAt := clock.Add(time.Hour)
	if _, err := s.Handover(HandoverInput{
		TransferID: transferID, SampleID: existingID,
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt,
	}); err != nil {
		t.Fatalf("前置交接发起: %v", err)
	}

	existingBefore, err := s.GetSample(existingID)
	if err != nil {
		t.Fatal(err)
	}
	transferBefore, err := s.GetTransfer(transferID)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted data: %v", err)
	}

	// 前置条件自检：已有样品数量、持有人、地点保持登记记录，交接处于
	// 待确认；拟登记编号尚未被使用。
	if existingBefore.InitialQty != "4.250" || existingBefore.Remaining != "4.250" ||
		existingBefore.Holder != "张三" || existingBefore.Location != "实验室A" ||
		len(existingBefore.History) != 2 {
		t.Fatalf("前置已有样品状态不正确: %+v", existingBefore)
	}
	if existingBefore.PendingTransfer == nil ||
		existingBefore.PendingTransfer.TransferID != transferID ||
		existingBefore.PendingTransfer.Confirmed {
		t.Fatalf("已有样品应正在等待交接 %s 确认: %+v", transferID, existingBefore.PendingTransfer)
	}
	if transferBefore.Confirmed || transferBefore.ReceivedAt != nil {
		t.Fatalf("前置交接应处于待确认: %+v", transferBefore)
	}
	if _, err := s.GetSample(newID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("拟登记编号 %q 应尚未使用", newID)
	}

	return &registerSaveFixture{
		dir: dir, path: path, s: s, clock: clock,
		existingID: existingID, transferID: transferID, newID: newID,
		// 入参带首尾空白、数量不带三位小数：成功结果应按现有规则去空白
		// 并统一显示三位小数。
		in: RegisterInput{
			ID: " R-100 ", Qty: "2.5", Holder: " 王五 ", Location: " 实验室C ",
		},
		existingBefore: existingBefore,
		transferBefore: transferBefore,
		diskBefore:     disk,
	}
}

// assertRegisterDidNotTakeEffect 断言一次保存失败的登记完全没有生效：
// 拟登记编号仍未被使用，已有样品的数量、持有人、地点、保管历史与待确认
// 状态保持原样，已有交接的交出信息和指定接收信息也没有被改写。
func assertRegisterDidNotTakeEffect(t *testing.T, f *registerSaveFixture) {
	t.Helper()

	// 拟登记编号不得出现在数据中：按编号查询必须得到样品不存在错误。
	if got, err := f.s.GetSample(f.newID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败的登记不应占用编号 %s, got view=%+v err=%v", f.newID, got, err)
	}

	// 已有样品逐项保持提交前状态。
	existing, err := f.s.GetSample(f.existingID)
	if err != nil {
		t.Fatalf("get existing: %v", err)
	}
	if !reflect.DeepEqual(existing, f.existingBefore) {
		t.Fatalf("保存失败后已有样品状态被改动:\n before=%+v\n after =%+v",
			f.existingBefore, existing)
	}
	// 关键业务字段再逐项点明：初始量、剩余量、持有人、地点。
	if existing.InitialQty != "4.250" || existing.Remaining != "4.250" {
		t.Fatalf("已有样品数量不应被失败的登记改动: init=%s remaining=%s",
			existing.InitialQty, existing.Remaining)
	}
	if existing.Holder != "张三" || existing.Location != "实验室A" {
		t.Fatalf("已有样品持有人和地点不应变化: %s @ %s", existing.Holder, existing.Location)
	}
	// 失败的登记不能解除已有样品的待确认状态。
	pt := existing.PendingTransfer
	if pt == nil || pt.TransferID != f.transferID || pt.Confirmed {
		t.Fatalf("已有样品的待确认状态应保持: %+v", existing.PendingTransfer)
	}

	// 已有交接的编号、交出信息和指定接收信息不能被改写，也不能变成已确认。
	tr, err := f.s.GetTransfer(f.transferID)
	if err != nil {
		t.Fatalf("get transfer: %v", err)
	}
	if !reflect.DeepEqual(tr, f.transferBefore) {
		t.Fatalf("已有交接不应被失败的登记改动:\n before=%+v\n after =%+v",
			f.transferBefore, tr)
	}
	if tr.Confirmed || tr.ReceivedAt != nil {
		t.Fatalf("失败的登记不能使原交接变成已确认: %+v", tr)
	}
}

// TestRegisterSaveFailureIsAtomic 覆盖编号未被使用、数量和保管信息都合法
// 的原样，在本地保存失败时的回归：Register 必须返回说明保存失败的错误
// （而不是成功的样品结果，也不能归为入参非法、编号冲突或样品不存在），
// 拟登记编号在保存成功前不得被使用，已有样品与已有待确认交接保持原样，
// 磁盘上不留下失败登记的编号或登记历史；保存未恢复时重提同一请求仍报
// 保存失败；恢复保存后继续用原来打开的数据以相同编号和登记信息重试，
// 应正常成功且只产生一次登记历史，此后该编号才算被使用。
func TestRegisterSaveFailureIsAtomic(t *testing.T) {
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
			f := setupPendingExisting(t)
			preservedPath, restore := tc.inject(t, f.dir, f.path)

			// 编号未使用、数量合法、持有人和地点非空——登记本身完全可做，
			// 仅保存失败。
			done, err := f.s.Register(f.in)
			if err == nil {
				t.Fatalf("保存失败时登记应返回错误，却得到成功结果: %+v", done)
			}
			if done != nil {
				t.Fatalf("保存失败不应返回表示登记成功的样品结果: %+v", done)
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

			// 失败后立即从原来打开的数据查询：拟登记编号未被使用，已有
			// 样品与已有待确认交接停留在提交前。
			assertRegisterDidNotTakeEffect(t, f)

			// 保存仍未恢复时再次提交同一份登记信息：必须再次报保存失败，
			// 既不能返回成功结果，也不能因编号被提前占用而按编号冲突拒绝。
			done2, err2 := f.s.Register(f.in)
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
			assertRegisterDidNotTakeEffect(t, f)

			// 此前保存的有效记录必须完整保留：不能混入失败登记的编号
			// 或登记历史。
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

			// 继续使用原来打开的样品数据，以失败时相同的编号和登记信息
			// 再次登记：应正常成功，不要求换编号或重新创建数据。
			got, err := f.s.Register(f.in)
			if err != nil {
				t.Fatalf("恢复后用相同编号和登记信息重新登记应成功: %v", err)
			}
			// 成功结果按现有规则去掉编号、持有人和地点的首尾空白，
			// 数量统一显示三位小数。
			if got.ID != f.newID || got.Holder != "王五" || got.Location != "实验室C" {
				t.Fatalf("登记结果应去首尾空白: %+v", got)
			}
			if got.InitialQty != "2.500" || got.Remaining != "2.500" {
				t.Fatalf("数量应统一显示三位小数, got init=%s rem=%s",
					got.InitialQty, got.Remaining)
			}
			// 这是一份没有来源样品、子样和待确认交接的原样。
			if got.ParentID != "" || len(got.Children) != 0 {
				t.Fatalf("新登记原样不应有来源或子样: %+v", got)
			}
			if got.PendingTransfer != nil || got.Destruction != nil {
				t.Fatalf("新登记原样不应有待确认交接或销毁信息: %+v", got)
			}
			// 只有一次登记历史：之前失败的尝试不能增加历史。
			if len(got.History) != 1 {
				t.Fatalf("新登记原样应只有一次登记历史: %+v", got.History)
			}
			h := got.History[0]
			if h.Kind != "register" || !h.Time.Equal(f.clock) ||
				h.Holder != "王五" || h.Location != "实验室C" {
				t.Fatalf("登记历史内容不正确: %+v", h)
			}

			// 按编号查询应得到与登记返回一致的结果。
			queried, err := f.s.GetSample(f.newID)
			if err != nil {
				t.Fatalf("登记成功后编号 %s 应可查询: %v", f.newID, err)
			}
			if !reflect.DeepEqual(queried, got) {
				t.Fatalf("查询结果应与登记返回一致:\n register=%+v\n query   =%+v", got, queried)
			}

			// 已有样品与已有待确认交接不受成功重试影响。
			assertExistingUnchangedAfterRetry(t, f)

			// 这次成功后该编号才算被使用：再提交同一编号（即使内容完全
			// 相同）仍应按现有登记规则返回 ErrConflict，不能照搬交接的
			// 重复提交规则返回成功。
			dup, err := f.s.Register(f.in)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("编号已使用后重复登记应返回 ErrConflict: view=%+v err=%v", dup, err)
			}
			if dup != nil {
				t.Fatalf("重复登记不应返回成功结果: %+v", dup)
			}
			after, err := f.s.GetSample(f.newID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, got) {
				t.Fatalf("被拒绝的重复登记不得改动已登记样品:\n before=%+v\n after =%+v",
					got, after)
			}

			// 成功的重试已正常落盘：重开后新原样只有一次登记历史，已有
			// 样品仍在等待原交接确认，已有交接内容不变。
			s2, err := Open(f.path)
			if err != nil {
				t.Fatalf("reopen after retry: %v", err)
			}
			reopened, err := s2.GetSample(f.newID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reopened, got) {
				t.Fatalf("重开后新登记原样应保持:\n before=%+v\n after =%+v", got, reopened)
			}
			reopenedExisting, err := s2.GetSample(f.existingID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reopenedExisting, f.existingBefore) {
				t.Fatalf("重开后已有样品应保持原样:\n before=%+v\n after =%+v",
					f.existingBefore, reopenedExisting)
			}
			reopenedTransfer, err := s2.GetTransfer(f.transferID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reopenedTransfer, f.transferBefore) {
				t.Fatalf("重开后已有交接应保持待确认:\n before=%+v\n after =%+v",
					f.transferBefore, reopenedTransfer)
			}
		})
	}
}

// assertExistingUnchangedAfterRetry 断言保存恢复后的成功重试只新增了
// 拟登记的原样，已有样品与已有待确认交接仍停留在提交前状态。
func assertExistingUnchangedAfterRetry(t *testing.T, f *registerSaveFixture) {
	t.Helper()

	existing, err := f.s.GetSample(f.existingID)
	if err != nil {
		t.Fatalf("get existing: %v", err)
	}
	if !reflect.DeepEqual(existing, f.existingBefore) {
		t.Fatalf("成功重试不应改动已有样品:\n before=%+v\n after =%+v",
			f.existingBefore, existing)
	}
	tr, err := f.s.GetTransfer(f.transferID)
	if err != nil {
		t.Fatalf("get transfer: %v", err)
	}
	if !reflect.DeepEqual(tr, f.transferBefore) {
		t.Fatalf("成功重试不应改动已有交接:\n before=%+v\n after =%+v",
			f.transferBefore, tr)
	}
}
