package custody

import (
	"math"
	"testing"
	"time"
)

// 重新打开数据时，每条已确认交接除了要有有效的确认人和接收时间，还必须
// 关联一份实际存在的样品，且交接量大于零、不超过该样品自身的初始量。
// 这些规则无法通过公开操作破坏，只能手工构造损坏文件来验证。

// confirmedTransferRecord 构造一条接收信息完整的已确认交接记录，
// 交接量（千分之一毫升）与样品编号由参数注入，便于构造零、负或超量。
func confirmedTransferRecord(id, sampleID string, qty int64) *transferRecord {
	hAt := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	rAt := hAt.Add(time.Hour)
	return &transferRecord{
		ID: id, SampleID: sampleID,
		FromHolder:   "张三",
		FromLocation: "实验室A",
		ToHolder:     "李四",
		ToLocation:   "实验室B",
		Qty:          qty,
		HandedOverAt: hAt,
		Confirmed:    true,
		ConfirmedBy:  "李四",
		ReceivedAt:   &rAt,
	}
}

// TestOpenRejectsConfirmedTransferWithoutSample 已确认交接引用的样品在
// 文件中根本不存在时，整份文件必须打开失败，不能让按交接编号查询看到
// 一次指向空样品的转手。
func TestOpenRejectsConfirmedTransferWithoutSample(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{},
		Transfers: map[string]*transferRecord{
			"T1": confirmedTransferRecord("T1", "S-ghost", 1000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "T1", "S-ghost", "不存在")
}

// TestOpenRejectsConfirmedTransferWithZeroQty 交接量为零不可能构成转手。
func TestOpenRejectsConfirmedTransferWithZeroQty(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S1": activeSample("S1", "", 10000, 10000),
		},
		Transfers: map[string]*transferRecord{
			"T1": confirmedTransferRecord("T1", "S1", 0),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "T1", "S1", "0.000", "大于零")
}

// TestOpenRejectsConfirmedTransferWithNegativeQty 交接量为负（含格式可表示
// 的极端负值）同样必须拒绝，错误数量按三位小数毫升展示。
func TestOpenRejectsConfirmedTransferWithNegativeQty(t *testing.T) {
	cases := []struct {
		name string
		qty  int64
		text string
	}{
		{"负一个最小刻度", -1, "-0.001"},
		{"负四毫升", -4000, "-4.000"},
		{"极端负数量", math.MinInt64, "-9223372036854775.808"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := &ledger{
				Samples: map[string]*sampleRecord{
					"S1": activeSample("S1", "", 10000, 10000),
				},
				Transfers: map[string]*transferRecord{
					"T1": confirmedTransferRecord("T1", "S1", c.qty),
				},
			}
			path := writeStructLedger(t, l)
			mustRejectOpen(t, path, "T1", "S1", c.text, "大于零")
		})
	}
}

// TestOpenRejectsConfirmedTransferOverOriginalInitial 原样初始 10.000，
// 已确认交接却记着 12.000，即使确认人与接收时间齐全也必须拒绝。
func TestOpenRejectsConfirmedTransferOverOriginalInitial(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S1": activeSample("S1", "", 10000, 0),
		},
		Transfers: map[string]*transferRecord{
			"T1": confirmedTransferRecord("T1", "S1", 12000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "T1", "S1", "12.000", "10.000", "超过样品自身初始量")
}

// TestOpenRejectsConfirmedTransferOverChildOwnInitial 任务示例：子样创建时
// 分得 3.250，却留下 4.000 的已确认交接；即使父样初始量为 10.000，
// 子样的上限也只能是它自己的 3.250，不能借用父样的量。
func TestOpenRejectsConfirmedTransferOverChildOwnInitial(t *testing.T) {
	parent := activeSample("P", "", 10000, 6750)
	parent.Children = []string{"C"}
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": parent,
			"C": plainChild("C", "P", 3250, 0),
		},
		Transfers: map[string]*transferRecord{
			"T-c": confirmedTransferRecord("T-c", "C", 4000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "T-c", "C", "4.000", "3.250", "超过样品自身初始量")
}

// TestOpenRejectsConfirmedTransferDespiteOtherValidRecords 文件里另有完全
// 正常的样品与交接时，也不能只恢复正常部分：整份文件打开失败、原文件
// 内容保持不变。
func TestOpenRejectsConfirmedTransferDespiteOtherValidRecords(t *testing.T) {
	parent := activeSample("P", "", 10000, 6750)
	parent.Children = []string{"C"}
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P":  parent,
			"C":  plainChild("C", "P", 3250, 0),
			"S2": activeSample("S2", "", 5000, 5000),
		},
		Transfers: map[string]*transferRecord{
			// 合法：子样恰以自身初始量 3.250 完成交接。
			"T-ok": confirmedTransferRecord("T-ok", "C", 3250),
			// 非法：原样只有 5.000 却记着 6.000 的转手。
			"T-bad": confirmedTransferRecord("T-bad", "S2", 6000),
			// 另一笔完全正常的交接。
			"T-good": confirmedTransferRecord("T-good", "P", 10000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "T-bad", "S2", "6.000", "5.000")
}

// TestOpenAcceptsConfirmedTransferAtOwnInitialBoundary 交接量恰好等于样品
// 自身初始量是合法边界：原样满量交接、子样恰以分得量交接都应正常恢复。
func TestOpenAcceptsConfirmedTransferAtOwnInitialBoundary(t *testing.T) {
	parent := activeSample("P", "", 10000, 6750)
	parent.Children = []string{"C"}
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": parent,
			"C": plainChild("C", "P", 3250, 0),
		},
		Transfers: map[string]*transferRecord{
			"T-p": confirmedTransferRecord("T-p", "P", 10000),
			"T-c": confirmedTransferRecord("T-c", "C", 3250),
		},
	}
	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("交接量恰好等于样品自身初始量应正常恢复: %v", err)
	}
	for _, tc := range []struct {
		id, sample, qty string
	}{
		{"T-p", "P", "10.000"},
		{"T-c", "C", "3.250"},
	} {
		tr, err := s.GetTransfer(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if !tr.Confirmed || tr.SampleID != tc.sample || tr.Qty != tc.qty {
			t.Fatalf("已确认交接 %s 恢复错误: %+v", tc.id, tr)
		}
	}
}

// TestOpenDoesNotSumRepeatedConfirmedQuantities 上限只按单笔交接量对照
// 样品自身初始量，不能把同一样品多次转手的交接量相加：原样先以 10.000
// 完成交接、之后又留下一笔 10.000 的旧交接，两笔各自都不超过初始量，
// 必须原样保留，不能按合计 20.000 拒绝，也不能拿当前剩余量（0.000）当
// 旧交接的上限。
func TestOpenDoesNotSumRepeatedConfirmedQuantities(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S1": activeSample("S1", "", 10000, 0),
		},
		Transfers: map[string]*transferRecord{
			"T1": confirmedTransferRecord("T1", "S1", 10000),
			"T2": confirmedTransferRecord("T2", "S1", 10000),
		},
	}
	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("多笔旧交接应逐笔按样品自身初始量核对，不能相加拒绝: %v", err)
	}
	for _, id := range []string{"T1", "T2"} {
		tr, err := s.GetTransfer(id)
		if err != nil {
			t.Fatal(err)
		}
		if !tr.Confirmed || tr.Qty != "10.000" {
			t.Fatalf("旧交接 %s 应保留当时的 10.000 毫升: %+v", id, tr)
		}
	}
}

// TestOpenKeepsConfirmedTransferAfterHolderMoved 样品后来更换了持有人和
// 地点，旧已确认交接保存的仍是当时的转手（交出人/地点与样品现状不同、
// 数量等于初始量），必须照常恢复并可按编号查询。
func TestOpenKeepsConfirmedTransferAfterHolderMoved(t *testing.T) {
	sample := activeSample("S1", "", 10000, 10000)
	sample.Holder = "王五"
	sample.Location = "实验室C"
	l := &ledger{
		Samples: map[string]*sampleRecord{"S1": sample},
		Transfers: map[string]*transferRecord{
			"T1": confirmedTransferRecord("T1", "S1", 10000),
		},
	}
	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("样品后来更换持有人/地点不应影响旧已确认交接恢复: %v", err)
	}
	tr, err := s.GetTransfer("T1")
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Confirmed || tr.Qty != "10.000" || tr.FromHolder != "张三" || tr.ToHolder != "李四" {
		t.Fatalf("旧交接的人员与数量应原样保留: %+v", tr)
	}
}
