package custody

import (
	"errors"
	"testing"
	"time"
)

// 重新打开数据时，每条已确认交接都必须关联一份实际存在的样品记录，且
// 交接量本身成立：大于零、不超过该样品自身的初始量（原样取登记量，分装
// 子样取创建时分得的量，不能借用父样或其他样品的量）。这些测试构造公开
// 操作无法产生的损坏记录，验证 Open 一律拒绝、整份文件不加载、原文件
// 不改写，且错误可用 errors.Is 判定为 ErrInvalid。

// confirmedRestoredAt 为构造已确认交接使用的固定时间。
var (
	confirmedHandedAt   = time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	confirmedReceivedAt = time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC)
)

// confirmedTransferRecord 构造一条接收信息完整的已确认交接记录，数量单位
// 为千分之一毫升；交出人张三/A、接收人李四/B 与常见构造样品搭配使用。
func confirmedTransferRecord(id, sampleID string, qty int64) *transferRecord {
	hAt := confirmedHandedAt
	rAt := confirmedReceivedAt
	return &transferRecord{
		ID:           id,
		SampleID:     sampleID,
		FromHolder:   "张三",
		FromLocation: "A",
		ToHolder:     "李四",
		ToLocation:   "B",
		Qty:          qty,
		HandedOverAt: hAt,
		Confirmed:    true,
		ReceivedAt:   &rAt,
		ConfirmedBy:  "李四",
	}
}

// TestOpenRejectsConfirmedTransferWithoutSample 已确认交接引用的样品在
// 文件中不存在时，整份文件必须打开失败，不能让一次无样品的转手可被查询。
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

// TestOpenRejectsConfirmedTransferWithNullSample 关联样品编号被 null 条目
// 占用同样按样品不存在处理，不能当作记录缺失而放行。
func TestOpenRejectsConfirmedTransferWithNullSample(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S1": nil,
		},
		Transfers: map[string]*transferRecord{
			"T1": confirmedTransferRecord("T1", "S1", 1000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "T1", "S1")
}

// TestOpenRejectsConfirmedTransferWithZeroQty 接收信息齐全但交接量为零，
// 这样的转手不可能发生，必须拒绝。
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

// TestOpenRejectsConfirmedTransferWithNegativeQty 交接量为负也必须拒绝，
// 错误信息以三位小数毫升展示负交接量。
func TestOpenRejectsConfirmedTransferWithNegativeQty(t *testing.T) {
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S1": activeSample("S1", "", 10000, 10000),
		},
		Transfers: map[string]*transferRecord{
			"T1": confirmedTransferRecord("T1", "S1", -4000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "T1", "S1", "-4.000", "大于零")
}

// TestOpenRejectsConfirmedTransferAboveOriginalInitial 原样上限是它自身的
// 初始量：初始 10.000 的样品留着 12.000 的已确认交接必须拒绝。
func TestOpenRejectsConfirmedTransferAboveOriginalInitial(t *testing.T) {
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

// TestOpenRejectsChildConfirmedTransferAboveOwnInitial 任务示例：子样创建
// 时只分得 3.250 毫升，却留着 4.000 毫升的已确认交接；即使父样初始量为
// 10.000 毫升，也不能借用父样、兄弟子样的量，必须拒绝。
func TestOpenRejectsChildConfirmedTransferAboveOwnInitial(t *testing.T) {
	parent := activeSample("P", "", 10000, 6750)
	parent.Children = []string{"C"}
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": parent,
			"C": plainChild("C", "P", 3250, 3250),
		},
		Transfers: map[string]*transferRecord{
			"TC": confirmedTransferRecord("TC", "C", 4000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "TC", "C", "4.000", "3.250", "超过样品自身初始量")
}

// TestOpenConfirmedTransferCheckIsWholeFileFailure 文件里另有完全正常的
// 样品与交接时，也不能只恢复正常部分；原文件保持不变（由
// mustRejectOpen 统一核对）。
func TestOpenConfirmedTransferCheckIsWholeFileFailure(t *testing.T) {
	parent := activeSample("P", "", 10000, 6750)
	parent.Children = []string{"C"}
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P":  parent,
			"C":  plainChild("C", "P", 3250, 3250),
			"S2": activeSample("S2", "", 5000, 5000),
		},
		Transfers: map[string]*transferRecord{
			// 越界的子样交接。
			"TC": confirmedTransferRecord("TC", "C", 4000),
			// 完全正常的另一份原样交接。
			"T2": confirmedTransferRecord("T2", "S2", 5000),
		},
	}
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "TC", "C", "4.000", "3.250")
}

// TestOpenAcceptsConfirmedTransferQuantityBoundaries 合法边界必须正常恢复：
// 交接量恰好等于样品自身初始量（含子样等于自己分得的量）合法；样品后来
// 剩余量被分装用尽也不否定当时的满量转手。
func TestOpenAcceptsConfirmedTransferQuantityBoundaries(t *testing.T) {
	parent := activeSample("P", "", 10000, 6750)
	parent.Children = []string{"C"}
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"P": parent,
			// 子样当前已无剩余（旧数据分装用尽约定），但曾以自己创建时
			// 分得的全部 3.250 毫升完成过一次交接。
			"C": plainChild("C", "P", 3250, 0),
		},
		Transfers: map[string]*transferRecord{
			"TP": confirmedTransferRecord("TP", "P", 10000),
			"TC": confirmedTransferRecord("TC", "C", 3250),
		},
	}
	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("交接量等于样品自身初始量属于合法边界，应正常恢复: %v", err)
	}
	for _, id := range []string{"TP", "TC"} {
		tr, err := s.GetTransfer(id)
		if err != nil {
			t.Fatal(err)
		}
		if !tr.Confirmed {
			t.Fatalf("交接 %s 应保持已确认状态", id)
		}
	}
	if tc, _ := s.GetTransfer("TC"); tc.Qty != "3.250" {
		t.Fatalf("子样交接量应原样恢复为 3.250, got %s", tc.Qty)
	}
}

// TestOpenKeepsHistoricalConfirmedQuantitiesRegardlessOfLaterChanges 旧交接
// 的上限不是样品当前剩余量，多次转手量也不能相加后与初始量比较：原样先
// 以 10.000 完成交接，之后分出 3.250 子样、再以剩余的 6.750 完成第二次
// 交接；两条已确认交接（合计 16.750 > 初始 10.000）都必须按当时的数量
// 原样保留。样品后来换了持有人、地点也同样不影响恢复与查询。
func TestOpenKeepsHistoricalConfirmedQuantitiesRegardlessOfLaterChanges(t *testing.T) {
	holder := "王五"
	location := "实验室C"
	parent := activeSample("S-001", "", 10000, 6750)
	parent.Holder = holder
	parent.Location = location
	parent.Children = []string{"S-001-A"}
	child := plainChild("S-001-A", "S-001", 3250, 3250)
	child.Holder = holder
	child.Location = location

	hAt2 := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	rAt2 := hAt2.Add(time.Hour)
	second := confirmedTransferRecord("TR-002", "S-001", 6750)
	second.FromHolder = "李四"
	second.FromLocation = "实验室B"
	second.ToHolder = holder
	second.ToLocation = location
	second.HandedOverAt = hAt2
	second.ReceivedAt = &rAt2
	second.ConfirmedBy = holder

	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":   parent,
			"S-001-A": child,
		},
		Transfers: map[string]*transferRecord{
			"TR-001": confirmedTransferRecord("TR-001", "S-001", 10000),
			"TR-002": second,
		},
	}
	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("两条历史已确认交接都不超过各自发生时样品自身初始量，应正常恢复: %v", err)
	}
	first, err := s.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Confirmed || first.Qty != "10.000" {
		t.Fatalf("第一次旧交接必须保留当时的 10.000 毫升: %+v", first)
	}
	next, err := s.GetTransfer("TR-002")
	if err != nil {
		t.Fatal(err)
	}
	if !next.Confirmed || next.Qty != "6.750" || next.ConfirmedBy != "王五" {
		t.Fatalf("第二次交接必须保留当时的 6.750 毫升与接收事实: %+v", next)
	}
	p, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	if p.Holder != holder || p.Location != location {
		t.Fatalf("样品后来更换的持有人和地点应原样恢复: %+v", p)
	}

	// 恢复后既有接收信息校验与重复提交行为继续有效：相同接收请求幂等
	// 返回原记录，不同接收信息被拒绝。
	again, err := s.Confirm(ConfirmInput{"TR-001", "李四", "B", confirmedReceivedAt})
	if err != nil {
		t.Fatalf("已确认交接重复提交相同接收信息应幂等返回: %v", err)
	}
	if again.Qty != "10.000" {
		t.Fatalf("幂等返回的旧交接数量被改写: %s", again.Qty)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-001", "李四", "B",
		confirmedReceivedAt.Add(time.Hour)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("改变接收信息的重复提交应继续返回 ErrConflict, got %v", err)
	}
}
