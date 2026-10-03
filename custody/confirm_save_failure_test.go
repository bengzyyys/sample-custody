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

// pendingConfirmFixture 是“一条已成功保存、仍待确认的交接”的测试前置状态。
type pendingConfirmFixture struct {
	dir, path string
	s         *Store
	hAt, rAt  time.Time
	in        ConfirmInput

	// 确认发起前的样品与交接视图，以及磁盘上已保存的数据内容；
	// 任何保存失败之后，它们都应保持不变。
	sampleBefore   *Sample
	transferBefore *TransferView
	diskBefore     []byte
}

// setupPendingConfirm 准备：登记样品 → 发起一条已落盘、仍待确认的交接。
// 样品仍在交出地点、由交出人持有；接收信息本身完全合法。
func setupPendingConfirm(t *testing.T) *pendingConfirmFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	clock := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	mustRegister(t, s, "P", "6.750", "张三", "实验室A")

	hAt := clock.Add(time.Hour)
	rAt := hAt.Add(time.Hour) // 接收时间晚于交出时间，满足确认要求
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt,
	}); err != nil {
		t.Fatalf("handover: %v", err)
	}

	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted data: %v", err)
	}
	sampleBefore, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	transferBefore, err := s.GetTransfer("TR-1")
	if err != nil {
		t.Fatal(err)
	}
	// 前置条件自检：交接已保存且处于待确认，样品仍由交出人在交出地点持有。
	if sampleBefore.Holder != "张三" || sampleBefore.Location != "实验室A" ||
		sampleBefore.Remaining != "6.750" || sampleBefore.PendingTransfer == nil ||
		sampleBefore.PendingTransfer.TransferID != "TR-1" {
		t.Fatalf("前置样品状态不正确: %+v", sampleBefore)
	}
	if transferBefore.Confirmed || transferBefore.ReceivedAt != nil ||
		transferBefore.ConfirmedBy != "" {
		t.Fatalf("前置交接不应已确认: %+v", transferBefore)
	}
	if !bytes.Contains(disk, []byte("TR-1")) {
		t.Fatalf("前置交接应已落盘")
	}

	return &pendingConfirmFixture{
		dir: dir, path: path, s: s, hAt: hAt, rAt: rAt,
		in:             ConfirmInput{"TR-1", "李四", "实验室B", rAt},
		sampleBefore:   sampleBefore,
		transferBefore: transferBefore,
		diskBefore:     disk,
	}
}

// assertStillPending 断言样品与交接都停留在确认前的待确认状态：
// 持有人、地点、剩余量、历史、待确认详情以及交接记录逐项不变。
func assertStillPending(t *testing.T, s *Store, f *pendingConfirmFixture) {
	t.Helper()

	sample, err := s.GetSample("P")
	if err != nil {
		t.Fatalf("get sample: %v", err)
	}
	if !reflect.DeepEqual(sample, f.sampleBefore) {
		t.Fatalf("保存失败后样品状态被改动:\n before=%+v\n after =%+v",
			f.sampleBefore, sample)
	}
	if sample.Holder != "张三" || sample.Location != "实验室A" {
		t.Fatalf("保管位置应保持为交出人和交出地点, got %s @ %s",
			sample.Holder, sample.Location)
	}
	if sample.PendingTransfer == nil ||
		sample.PendingTransfer.TransferID != "TR-1" {
		t.Fatalf("待确认详情仍应指向同一条交接 TR-1: %+v", sample.PendingTransfer)
	}

	transfer, err := s.GetTransfer("TR-1")
	if err != nil {
		t.Fatalf("get transfer: %v", err)
	}
	if !reflect.DeepEqual(transfer, f.transferBefore) {
		t.Fatalf("保存失败后交接记录被改动:\n before=%+v\n after =%+v",
			f.transferBefore, transfer)
	}
	if transfer.Confirmed {
		t.Fatalf("交接不应被标记为已确认: %+v", transfer)
	}
	if transfer.ReceivedAt != nil || transfer.ConfirmedBy != "" {
		t.Fatalf("未确认交接不应有接收时间和确认人: %+v", transfer)
	}
}

// TestConfirmSaveFailureKeepsPendingState 覆盖接收信息完全合法、但确认结果
// 无法保存到本地时的回归：确认必须返回保存错误而不是成功，内存与磁盘上的
// 保管状态都固定在确认前；恢复访问后用同一份接收信息重开重试，恰好成功
// 一次，重复确认仍幂等。
func TestConfirmSaveFailureKeepsPendingState(t *testing.T) {
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
			f := setupPendingConfirm(t)
			preservedPath, restore := tc.inject(t, f.dir, f.path)

			// 接收人、地点、接收时间本身都满足确认要求，但本次保存失败：
			// 必须返回错误，不能返回表示接收成功的结果。
			done, err := f.s.Confirm(f.in)
			if err == nil {
				t.Fatalf("保存失败时确认应返回错误，却得到成功结果: %+v", done)
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

			// 保存失败后立即查询，样品与交接都停留在确认前。
			assertStillPending(t, f.s, f)

			// 在这次失败与下一次成功确认之间，待确认限制继续生效：
			// 同一样品既不能分装，也不能另发起一条交接。这些冲突在校验
			// 阶段即被拒绝，不会走到落盘，也不得影响原交接。
			if _, err := f.s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
				{ID: "C-1", Qty: "0.001"},
			}}); !errors.Is(err, ErrConflict) {
				t.Fatalf("待确认期间分装应返回 ErrConflict, got %v", err)
			}
			if _, err := f.s.Handover(HandoverInput{
				TransferID: "TR-2", SampleID: "P",
				FromHolder: "张三", FromLocation: "实验室A",
				ToHolder: "王五", ToLocation: "实验室C",
				HandedOverAt: f.hAt,
			}); !errors.Is(err, ErrConflict) {
				t.Fatalf("待确认期间另发起交接应返回 ErrConflict, got %v", err)
			}
			assertStillPending(t, f.s, f)

			// 此前保存的样品文件保持原有业务内容，失败没有写入半截数据。
			preserved, err := os.ReadFile(preservedPath)
			if err != nil {
				t.Fatalf("读取原数据文件: %v", err)
			}
			if !bytes.Equal(preserved, f.diskBefore) {
				t.Fatalf("保存失败改动了磁盘上的原数据文件")
			}

			// 恢复正常文件访问：数据文件回到原路径，内容仍是确认前的状态。
			restore()
			disk, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatalf("恢复后读取数据文件: %v", err)
			}
			if !bytes.Equal(disk, f.diskBefore) {
				t.Fatalf("恢复后数据文件内容应与失败前一致")
			}

			// 重新打开，应仍能查询到同一条待确认交接和原来的保管位置。
			s2, err := Open(f.path)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			assertStillPending(t, s2, f)

			// 用失败时的同一份接收信息再次确认：成功完成一次接收。
			done, err = s2.Confirm(f.in)
			if err != nil {
				t.Fatalf("恢复后用相同接收信息确认应成功: %v", err)
			}
			if !done.Confirmed || done.ConfirmedBy != "李四" ||
				done.ReceivedAt == nil || !done.ReceivedAt.Equal(f.rAt) {
				t.Fatalf("成功确认视图错误: %+v", done)
			}

			sample, _ := s2.GetSample("P")
			if sample.Holder != "李四" || sample.Location != "实验室B" {
				t.Fatalf("确认后持有人和地点应改为接收人和目的地点: %+v", sample)
			}
			if sample.PendingTransfer != nil {
				t.Fatalf("确认后待确认详情应消失: %+v", sample.PendingTransfer)
			}
			if sample.Remaining != "6.750" {
				t.Fatalf("交接不改变剩余量, got %s", sample.Remaining)
			}
			// 历史只在原有记录后增加这一条接收记录。
			if len(sample.History) != len(f.sampleBefore.History)+1 {
				t.Fatalf("历史应只增加一条接收记录: before=%d after=%d",
					len(f.sampleBefore.History), len(sample.History))
			}
			if !reflect.DeepEqual(sample.History[:len(f.sampleBefore.History)],
				f.sampleBefore.History) {
				t.Fatalf("原有保管历史应保持不变: %+v", sample.History)
			}
			last := sample.History[len(sample.History)-1]
			if last.Kind != "transfer-in" || !last.Time.Equal(f.rAt) ||
				last.Holder != "李四" || last.Location != "实验室B" ||
				!strings.Contains(last.Detail, "TR-1") {
				t.Fatalf("新增历史不是本次接收记录: %+v", last)
			}
			transfer, _ := s2.GetTransfer("TR-1")
			if !transfer.Confirmed || transfer.ConfirmedBy != "李四" ||
				transfer.ReceivedAt == nil || !transfer.ReceivedAt.Equal(f.rAt) {
				t.Fatalf("交接查询应显示已确认并保留提交的接收时间: %+v", transfer)
			}

			// 相同信息的重复确认仍返回原结果，不再次追加记录。
			again, err := s2.Confirm(f.in)
			if err != nil {
				t.Fatalf("重复确认应幂等返回原结果: %v", err)
			}
			if !reflect.DeepEqual(again, done) {
				t.Fatalf("重复确认应返回与首次成功相同的结果:\n first=%+v\n again=%+v",
					done, again)
			}
			sampleAgain, _ := s2.GetSample("P")
			if len(sampleAgain.History) != len(sample.History) {
				t.Fatalf("重复确认不得再次追加历史: first=%d again=%d",
					len(sample.History), len(sampleAgain.History))
			}

			// 成功的重试已正常落盘：再次重开后状态保持。
			s3, err := Open(f.path)
			if err != nil {
				t.Fatalf("reopen after retry: %v", err)
			}
			reopened, err := s3.GetTransfer("TR-1")
			if err != nil {
				t.Fatal(err)
			}
			if !reopened.Confirmed || reopened.ConfirmedBy != "李四" ||
				reopened.ReceivedAt == nil || !reopened.ReceivedAt.Equal(f.rAt) {
				t.Fatalf("重开后确认状态应保留: %+v", reopened)
			}
			reopenedSample, _ := s3.GetSample("P")
			if reopenedSample.Holder != "李四" || reopenedSample.Location != "实验室B" ||
				reopenedSample.PendingTransfer != nil ||
				len(reopenedSample.History) != len(sample.History) {
				t.Fatalf("重开后样品保管状态错误: %+v", reopenedSample)
			}
		})
	}
}
