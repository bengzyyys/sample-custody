package custody

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestConcurrentSplitNoOvershoot 并发对同一样品发起分装，成功子样的总量
// 绝不能超过来源样品的初始量；失败的调用必须收到错误。
func TestConcurrentSplitNoOvershoot(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "1.000", "h", "l")

	const n = 50
	var wg sync.WaitGroup
	var okCount int64
	var mu sync.Mutex
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
				{ID: fmt.Sprintf("CC-%d", i), Qty: "0.025"},
			}})
			if err == nil {
				mu.Lock()
				okCount++
				mu.Unlock()
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("goroutine %d: 期望 ErrConflict, got %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// 每次分装扣 0.025，50 次并发合计 1.250 > 1.000，应恰好成功 40 次。
	if okCount != 40 {
		t.Fatalf("1.000 毫升按 0.025 分装应恰好成功 40 次, 实际 %d", okCount)
	}
	p, _ := s.GetSample("P")
	if p.Remaining != "0.000" {
		t.Fatalf("并发分装后剩余应为 0.000, got %s", p.Remaining)
	}
	if len(p.Children) != 40 {
		t.Fatalf("应只创建 40 个子样, got %d", len(p.Children))
	}
}

// TestConcurrentHandoverSinglePending 并发发起交接，至多一条成功并成为
// 唯一的待确认交接。
func TestConcurrentHandoverSinglePending(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "1.000", "h", "l")
	at := time.Now()

	const n = 50
	var wg sync.WaitGroup
	var ok int64
	var mu sync.Mutex
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := s.Handover(HandoverInput{
				TransferID: fmt.Sprintf("TR-%02d", i), SampleID: "P",
				FromHolder: "h", FromLocation: "l",
				ToHolder: "x", ToLocation: "y", HandedOverAt: at,
			})
			if err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("goroutine %d: 期望 ErrConflict, got %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if ok != 1 {
		t.Fatalf("应恰好有 1 条待确认交接, 实际 %d", ok)
	}
	p, _ := s.GetSample("P")
	if p.PendingTransfer == nil {
		t.Fatalf("应存在 1 条待确认交接")
	}
	if len(s.data.Transfers) != 1 {
		t.Fatalf("应只落盘 1 条交接, got %d", len(s.data.Transfers))
	}
}
