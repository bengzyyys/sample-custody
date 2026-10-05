package custody

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeLedger 把内部账本序列化写入一个新文件并返回路径。
func writeLedger(t *testing.T, l *ledger) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.json")
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func sampleRec(id string, pendingID string) *sampleRecord {
	return &sampleRecord{
		ID:        id,
		Initial:   1000,
		Remaining: 1000,
		Holder:    "张三",
		Location:  "A",
		Children:  []string{},
		History: []historyRecord{{
			Kind: "register", Time: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
			Holder: "张三", Location: "A",
		}},
		PendingID: pendingID,
	}
}

func pendingTransfer(id, sampleID string) *transferRecord {
	return &transferRecord{
		ID:           id,
		SampleID:     sampleID,
		FromHolder:   "张三",
		FromLocation: "A",
		ToHolder:     "李四",
		ToLocation:   "B",
		Qty:          1000,
		HandedOverAt: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC),
	}
}

func confirmedTransfer(id, sampleID string) *transferRecord {
	t := pendingTransfer(id, sampleID)
	rx := t.HandedOverAt.Add(time.Hour)
	t.Confirmed = true
	t.ConfirmedBy = "李四"
	t.ReceivedAt = &rx
	return t
}

// TestReopenRejectsUnrecoverablePending 待确认关联无法恢复时，Open 必须
// 返回 nil Store 和包装 ErrInvalid 的错误，且错误文字指出相关编号与原因。
func TestReopenRejectsUnrecoverablePending(t *testing.T) {
	cases := []struct {
		name   string
		build  func() *ledger
		wantID string // 错误信息中必须出现的相关编号
	}{
		{
			name: "样品指向不存在的交接",
			build: func() *ledger {
				return &ledger{
					Version:   1,
					Samples:   map[string]*sampleRecord{"S1": sampleRec("S1", "GHOST")},
					Transfers: map[string]*transferRecord{},
				}
			},
			wantID: "GHOST",
		},
		{
			name: "样品指向已经确认的交接",
			build: func() *ledger {
				return &ledger{
					Version: 1,
					Samples: map[string]*sampleRecord{"S1": sampleRec("S1", "T1")},
					Transfers: map[string]*transferRecord{
						"T1": confirmedTransfer("T1", "S1"),
					},
				}
			},
			wantID: "T1",
		},
		{
			name: "样品指向属于其他样品的交接",
			build: func() *ledger {
				return &ledger{
					Version: 1,
					Samples: map[string]*sampleRecord{
						"S1": sampleRec("S1", "T1"),
						"S2": sampleRec("S2", ""),
					},
					Transfers: map[string]*transferRecord{
						"T1": pendingTransfer("T1", "S2"),
					},
				}
			},
			wantID: "T1",
		},
		{
			name: "子样借用父样的待确认交接",
			build: func() *ledger {
				child := sampleRec("C", "TP")
				child.ParentID = "P"
				return &ledger{
					Version: 1,
					Samples: map[string]*sampleRecord{
						"P": sampleRec("P", ""),
						"C": child,
					},
					Transfers: map[string]*transferRecord{
						"TP": pendingTransfer("TP", "P"),
					},
				}
			},
			wantID: "TP",
		},
		{
			name: "两份样品指向同一条待确认交接",
			build: func() *ledger {
				return &ledger{
					Version: 1,
					Samples: map[string]*sampleRecord{
						"S1": sampleRec("S1", "T1"),
						"S2": sampleRec("S2", "T1"),
					},
					Transfers: map[string]*transferRecord{
						"T1": pendingTransfer("T1", "S1"),
					},
				}
			},
			wantID: "T1",
		},
		{
			name: "待确认交接对应的样品不存在",
			build: func() *ledger {
				return &ledger{
					Version:   1,
					Samples:   map[string]*sampleRecord{},
					Transfers: map[string]*transferRecord{"T1": pendingTransfer("T1", "GHOST")},
				}
			},
			wantID: "T1",
		},
		{
			name: "交接仍待确认但样品已清除编号",
			build: func() *ledger {
				return &ledger{
					Version: 1,
					Samples: map[string]*sampleRecord{
						"S1": sampleRec("S1", ""),
					},
					Transfers: map[string]*transferRecord{
						"T1": pendingTransfer("T1", "S1"),
					},
				}
			},
			wantID: "T1",
		},
		{
			name: "样品的待确认编号指向了另一条交接",
			build: func() *ledger {
				return &ledger{
					Version: 1,
					Samples: map[string]*sampleRecord{
						"S1": sampleRec("S1", "T2"),
					},
					Transfers: map[string]*transferRecord{
						"T1": pendingTransfer("T1", "S1"),
						"T2": pendingTransfer("T2", "S1"),
					},
				}
			},
			wantID: "T1",
		},
		{
			name: "样品集合中占用编号的条目为 null",
			build: func() *ledger {
				return &ledger{
					Version:   1,
					Samples:   map[string]*sampleRecord{"S1": nil},
					Transfers: map[string]*transferRecord{},
				}
			},
			wantID: "S1",
		},
		{
			name: "交接集合中占用编号的条目为 null",
			build: func() *ledger {
				return &ledger{
					Version: 1,
					Samples: map[string]*sampleRecord{
						"S1": sampleRec("S1", "T1"),
					},
					Transfers: map[string]*transferRecord{"T1": nil},
				}
			},
			wantID: "T1",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeLedger(t, c.build())
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			s, err := Open(path)
			if s != nil {
				t.Fatalf("无法恢复的数据必须返回空的 Store, got %+v", s)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("应返回包装 ErrInvalid 的错误, got %v", err)
			}
			if !strings.Contains(err.Error(), c.wantID) {
				t.Fatalf("错误文字应指出相关编号 %q, got %v", c.wantID, err)
			}

			// 拒绝打开不得改写原文件。
			after, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if string(after) != string(before) {
				t.Fatalf("拒绝打开不得改写原文件")
			}
		})
	}
}

// TestReopenNullJSONEntriesRejected 通过原始 JSON 再确认 "值为 null 的
// 占用条目" 不会被当成记录不存在而忽略或自动删除。
func TestReopenNullJSONEntriesRejected(t *testing.T) {
	cases := map[string]string{
		"样品为 null": `{"version":1,"samples":{"S1":null},"transfers":{}}`,
		"交接为 null": `{
  "version": 1,
  "samples": {"S1": {"id":"S1","initial":1000,"remaining":1000,"holder":"张三","location":"A","children":[],"history":[{"kind":"register","time":"2026-10-01T09:00:00Z","holder":"张三","location":"A"}],"pendingId":"T1"}},
  "transfers": {"T1": null}
}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data.json")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := Open(path)
			if s != nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("null 条目必须拒绝打开, got s=%v err=%v", s, err)
			}
			// 原文件保持原样，未被“自动删除 null 后继续打开”。
			got, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if string(got) != raw {
				t.Fatalf("拒绝打开不得改写原文件")
			}
		})
	}
}

// TestReopenEmptyCollectionsAllowed 集合整体缺省、为 null 或为空对象时，
// 沿用现有空集合含义，仍可正常打开。
func TestReopenEmptyCollectionsAllowed(t *testing.T) {
	raws := []string{
		`{"version":1}`,
		`{"version":1,"samples":null,"transfers":null}`,
		`{"version":1,"samples":{},"transfers":{}}`,
	}
	for i, raw := range raws {
		path := filepath.Join(t.TempDir(), "data.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Open(path)
		if err != nil {
			t.Fatalf("case %d: 空集合应允许打开, got %v", i, err)
		}
		if len(s.data.Samples) != 0 || len(s.data.Transfers) != 0 {
			t.Fatalf("case %d: 应为空集合", i)
		}
		mustRegister(t, s, "X", "1.000", "h", "l")
	}
}

// TestReopenValidPendingAndHistoryAllowed 合法数据必须原样恢复：
// 待确认交接双向对应、已确认历史不要求样品继续指向、父样子样各自独立；
// 样品完成旧交接后分装再发起新交接，旧交接与当前持有人/地点/剩余量
// 不同也完全正常。
func TestReopenValidPendingAndHistoryAllowed(t *testing.T) {
	regAt := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	oldHandAt := regAt.Add(time.Hour)
	oldRecvAt := oldHandAt.Add(time.Hour)
	newHandAt := oldRecvAt.Add(time.Hour)

	l := &ledger{Version: 1}

	// 父样 P：初始 10；旧交接 T1（张三/A → 李四/B，10）已确认；
	// 确认后分装出子样 C（4），P 剩余 6、现由王五在 C 地点持有，
	// 又发起新的待确认交接 T2（王五/C → 赵六/D，6）。
	parent := sampleRec("P", "T2")
	parent.Initial = 10000
	parent.Remaining = 6000
	parent.Holder = "王五"
	parent.Location = "C"
	parent.Children = []string{"C"}
	parent.History = []historyRecord{
		{Kind: "register", Time: regAt, Holder: "张三", Location: "A"},
		{Kind: "transfer-out", Time: oldHandAt, Holder: "张三", Location: "A"},
		{Kind: "transfer-in", Time: oldRecvAt, Holder: "李四", Location: "B"},
		{Kind: "split", Time: oldRecvAt.Add(time.Minute), Holder: "李四", Location: "B"},
		{Kind: "transfer-out", Time: newHandAt, Holder: "王五", Location: "C"},
	}

	child := sampleRec("C", "")
	child.ParentID = "P"
	child.Initial = 4000
	child.Remaining = 4000
	child.Holder = "李四"
	child.Location = "B"

	t1 := &transferRecord{
		ID: "T1", SampleID: "P",
		FromHolder: "张三", FromLocation: "A",
		ToHolder: "李四", ToLocation: "B",
		Qty: 10000, HandedOverAt: oldHandAt,
		Confirmed: true, ConfirmedBy: "李四", ReceivedAt: &oldRecvAt,
	}
	t2 := &transferRecord{
		ID: "T2", SampleID: "P",
		FromHolder: "王五", FromLocation: "C",
		ToHolder: "赵六", ToLocation: "D",
		Qty: 6000, HandedOverAt: newHandAt,
	}

	l.Samples = map[string]*sampleRecord{"P": parent, "C": child}
	l.Transfers = map[string]*transferRecord{"T1": t1, "T2": t2}

	s, err := Open(writeLedger(t, l))
	if err != nil {
		t.Fatalf("合法的历史+待确认数据应正常打开: %v", err)
	}

	p, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	// 旧交接已成为历史，样品当前状态与旧交接不同不应被拒绝。
	if p.Holder != "王五" || p.Location != "C" || p.Remaining != "6.000" {
		t.Fatalf("样品当前状态应原样恢复: %+v", p)
	}
	if p.PendingTransfer == nil || p.PendingTransfer.TransferID != "T2" || p.PendingTransfer.Confirmed {
		t.Fatalf("待确认交接 T2 应原样恢复: %+v", p.PendingTransfer)
	}

	// 旧交接仍可按已确认历史查询，重复提交行为保留。
	old, err := s.GetTransfer("T1")
	if err != nil {
		t.Fatal(err)
	}
	if !old.Confirmed {
		t.Fatalf("T1 应为已确认历史: %+v", old)
	}
	if _, err := s.Handover(HandoverInput{
		TransferID: "T1", SampleID: "P",
		FromHolder: "张三", FromLocation: "A",
		ToHolder: "李四", ToLocation: "B", HandedOverAt: oldHandAt,
	}); err != nil {
		t.Fatalf("已确认交接的相同重复提交应继续保留: %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"T1", "李四", "B", oldRecvAt}); err != nil {
		t.Fatalf("已确认交接相同接收重复提交应继续保留: %v", err)
	}

	// 待确认接收行为在重开后继续有效。
	done, err := s.Confirm(ConfirmInput{"T2", "赵六", "D", newHandAt})
	if err != nil {
		t.Fatalf("待确认交接应能正常接收: %v", err)
	}
	if !done.Confirmed {
		t.Fatalf("接收后应已确认: %+v", done)
	}
}

// TestReopenParentChildPendingIndependent 父样与子样的待确认交接互不
// 混同：各自指向属于自己的交接时应正常恢复。
func TestReopenParentChildPendingIndependent(t *testing.T) {
	parent := sampleRec("P", "TP")
	child := sampleRec("C", "TC")
	child.ParentID = "P"
	l := &ledger{
		Version: 1,
		Samples: map[string]*sampleRecord{"P": parent, "C": child},
		Transfers: map[string]*transferRecord{
			"TP": pendingTransfer("TP", "P"),
			"TC": pendingTransfer("TC", "C"),
		},
	}
	s, err := Open(writeLedger(t, l))
	if err != nil {
		t.Fatalf("父样子样各自的待确认交接应正常恢复: %v", err)
	}
	p, _ := s.GetSample("P")
	c, _ := s.GetSample("C")
	if p.PendingTransfer == nil || p.PendingTransfer.TransferID != "TP" {
		t.Fatalf("父样待确认交接错误: %+v", p.PendingTransfer)
	}
	if c.PendingTransfer == nil || c.PendingTransfer.TransferID != "TC" {
		t.Fatalf("子样待确认交接错误: %+v", c.PendingTransfer)
	}
}
