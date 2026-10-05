package custody

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// maxQty 是可接受的最大数量：math.MaxInt64 个千分之一毫升。
const maxQty = "9223372036854775.807"

// 接近数量上限的分装必须按整数刻度精确扣减，不能因数值很大丢失最后一位小数。
func TestSplitAtMaxQuantity(t *testing.T) {
	s, _ := fixedStore(t)

	got := mustRegister(t, s, "MAX", maxQty, "张三", "实验室A")
	if got.InitialQty != maxQty || got.Remaining != maxQty {
		t.Fatalf("最大数量登记后显示错误: %+v", got)
	}

	// 分出仅比上限少 0.001 的子样：来源恰好剩余 0.001。
	parent, err := s.Split(SplitInput{ParentID: "MAX", Parts: []SplitPart{
		{ID: "C1", Qty: "9223372036854775.806"},
	}})
	if err != nil {
		t.Fatalf("接近上限的分装应成功: %v", err)
	}
	if parent.Remaining != "0.001" {
		t.Fatalf("来源应恰好剩余 0.001, got %s", parent.Remaining)
	}
	if parent.InitialQty != maxQty {
		t.Fatalf("来源初始量应保留登记值 %s, got %s", maxQty, parent.InitialQty)
	}

	// 子样的初始量和剩余量都与请求一致，最后一位小数不丢失。
	c1, err := s.GetSample("C1")
	if err != nil {
		t.Fatal(err)
	}
	if c1.InitialQty != "9223372036854775.806" || c1.Remaining != "9223372036854775.806" {
		t.Fatalf("大数量子样丢失精度: init=%s rem=%s", c1.InitialQty, c1.Remaining)
	}
	if c1.ParentID != "MAX" || c1.Holder != "张三" || c1.Location != "实验室A" {
		t.Fatalf("子样来源关系/持有人/地点应与分装时一致: %+v", c1)
	}

	// 接着分出最后 0.001：剩余量准确归零，初始量仍保留登记值。
	parent, err = s.Split(SplitInput{ParentID: "MAX", Parts: []SplitPart{
		{ID: "C2", Qty: "0.001"},
	}})
	if err != nil {
		t.Fatalf("分出最后 0.001 应成功: %v", err)
	}
	if parent.Remaining != "0.000" {
		t.Fatalf("来源剩余量应准确变为 0.000, got %s", parent.Remaining)
	}
	if parent.InitialQty != maxQty {
		t.Fatalf("用尽后初始量仍应保留登记值 %s, got %s", maxQty, parent.InitialQty)
	}

	// 子样列表按分装顺序记录编号。
	if len(parent.Children) != 2 || parent.Children[0] != "C1" || parent.Children[1] != "C2" {
		t.Fatalf("子样列表应按分装顺序记录: %v", parent.Children)
	}

	// 两个子样各自保留实际分出量，来源关系、持有人和地点一致。
	c2, err := s.GetSample("C2")
	if err != nil {
		t.Fatal(err)
	}
	if c2.InitialQty != "0.001" || c2.Remaining != "0.001" {
		t.Fatalf("C2 数量错误: %+v", c2)
	}
	if c2.ParentID != "MAX" || c2.Holder != "张三" || c2.Location != "实验室A" {
		t.Fatalf("C2 来源关系/持有人/地点错误: %+v", c2)
	}
	c1, _ = s.GetSample("C1")
	if c1.InitialQty != "9223372036854775.806" || c1.Remaining != "9223372036854775.806" {
		t.Fatalf("C1 实际分出量应保留: %+v", c1)
	}

	// 来源历史依次记录登记与两次分装。
	kinds := []string{}
	for _, h := range parent.History {
		kinds = append(kinds, h.Kind)
	}
	want := []string{"register", "split", "split"}
	if len(kinds) != len(want) {
		t.Fatalf("来源历史顺序错误: %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("来源历史顺序错误: %v", kinds)
		}
	}
}

// 超过最大可表示数量的登记应被拒绝，且不创建任何记录。
func TestRegisterAboveMaxQuantityRejected(t *testing.T) {
	s, _ := fixedStore(t)
	if _, err := s.Register(RegisterInput{
		ID: "OVER", Qty: "9223372036854775.808", Holder: "h", Location: "l",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("超出上限的登记应返回 ErrInvalid, got %v", err)
	}
	if _, err := s.GetSample("OVER"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("被拒绝的登记不应创建样品, got %v", err)
	}
}

// 各子样数量分别合法，但合计超过 9223372036854775.807 毫升时，
// 整次分装必须返回 ErrInvalid，不能把合计回绕误判为一个更小的合法数量。
func TestSplitTotalOverflowRejected(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", maxQty, "h", "l")

	cases := []struct {
		name  string
		parts []SplitPart
	}{
		{"上限加最小量", []SplitPart{{ID: "A", Qty: maxQty}, {ID: "B", Qty: "0.001"}}},
		{"两份上限", []SplitPart{{ID: "A", Qty: maxQty}, {ID: "B", Qty: maxQty}}},
		// 三份上限直接相加会回绕成正数，最容易被误判为合法合计。
		{"三份上限回绕", []SplitPart{{ID: "A", Qty: maxQty}, {ID: "B", Qty: maxQty}, {ID: "C", Qty: maxQty}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.Split(SplitInput{ParentID: "P", Parts: c.parts})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("合计超出支持范围应返回 ErrInvalid, got %v", err)
			}
			if !strings.Contains(err.Error(), "超出可表示范围") {
				t.Fatalf("错误内容应说明合计超出可表示范围, got %v", err)
			}
		})
	}

	// 被拒绝后：来源数量、子样列表、历史均未改动，拟创建的子样全部查不到。
	p, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if p.Remaining != maxQty || p.InitialQty != maxQty {
		t.Fatalf("被拒绝的分装不得扣减来源数量: %+v", p)
	}
	if len(p.Children) != 0 || len(p.History) != 1 {
		t.Fatalf("被拒绝的分装不得追加子样编号或历史: %+v", p)
	}
	for _, id := range []string{"A", "B", "C"} {
		if _, err := s.GetSample(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("被拒绝分装的子样 %s 应查不到, got %v", id, err)
		}
	}
}

// 合计仍在支持范围内、却比来源剩余量多 0.001 毫升时，应返回 ErrConflict。
func TestSplitTotalExceedsRemainingByOneMilli(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", maxQty, "h", "l")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C1", Qty: "9223372036854775.806"},
	}}); err != nil {
		t.Fatal(err)
	}
	// 此时来源剩余 0.001；两个子样合计 0.002，在支持范围内但超出剩余量 0.001。
	_, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "D1", Qty: "0.001"},
		{ID: "D2", Qty: "0.001"},
	}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("合计超出剩余量 0.001 应返回 ErrConflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "剩余量") {
		t.Fatalf("错误内容应说明超过剩余量, got %v", err)
	}

	// 被拒绝后来源保持剩余 0.001，既有子样保留，新子样未创建。
	p, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if p.Remaining != "0.001" {
		t.Fatalf("被拒绝的分装不得扣减来源数量, got %s", p.Remaining)
	}
	if len(p.Children) != 1 || p.Children[0] != "C1" {
		t.Fatalf("拒绝前已存在的子样应保留: %v", p.Children)
	}
	if len(p.History) != 2 {
		t.Fatalf("拒绝前已存在的历史应保留: %+v", p.History)
	}
	for _, id := range []string{"D1", "D2"} {
		if _, err := s.GetSample(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("被拒绝分装的子样 %s 应查不到, got %v", id, err)
		}
	}
}

// 被拒绝的分装不得改变来源的持有人、地点及待确认状态。
func TestRejectedSplitKeepsHolderLocationAndPending(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "5.000", "张三", "实验室A")
	at := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}

	// 待确认期间尝试合计溢出的分装：整批拒绝。
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "O1", Qty: maxQty},
		{ID: "O2", Qty: maxQty},
	}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("合计溢出应返回 ErrInvalid, got %v", err)
	}
	// 待确认期间普通分装同样因冲突被拒绝。
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "O3", Qty: "0.001"},
	}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("待确认期间分装应返回 ErrConflict, got %v", err)
	}

	after, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if after.Holder != before.Holder || after.Location != before.Location {
		t.Fatalf("被拒绝的分装不得改变持有人或地点: before=%+v after=%+v", before, after)
	}
	if after.Remaining != before.Remaining ||
		len(after.Children) != len(before.Children) ||
		len(after.History) != len(before.History) {
		t.Fatalf("被拒绝的分装不得扣减数量或追加记录: before=%+v after=%+v", before, after)
	}
	if after.PendingTransfer == nil || after.PendingTransfer.TransferID != "TR-1" ||
		after.PendingTransfer.Confirmed {
		t.Fatalf("被拒绝的分装不得改变待确认状态: %+v", after.PendingTransfer)
	}
	for _, id := range []string{"O1", "O2", "O3"} {
		if _, err := s.GetSample(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("被拒绝分装的子样 %s 应查不到, got %v", id, err)
		}
	}
}
