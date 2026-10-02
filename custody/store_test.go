package custody

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// fixedNow 让事件时间在测试中确定。
func fixedStore(t *testing.T) (*Store, time.Time) {
	t.Helper()
	dir := t.TempDir()
	clock := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	s, err := Open(filepath.Join(dir, "data.json"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.now = func() time.Time { return clock }
	return s, clock
}

func mustRegister(t *testing.T, s *Store, id, qty, holder, loc string) *Sample {
	t.Helper()
	got, err := s.Register(RegisterInput{ID: id, Qty: qty, Holder: holder, Location: loc})
	if err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
	return got
}

func TestRegisterAndQuery(t *testing.T) {
	s, now := fixedStore(t)

	got := mustRegister(t, s, " S-001 ", "10.5", " 张三 ", " 实验室A ")
	if got.ID != "S-001" || got.Holder != "张三" || got.Location != "实验室A" {
		t.Fatalf("登记后字段未去空白: %+v", got)
	}
	if got.InitialQty != "10.500" || got.Remaining != "10.500" {
		t.Fatalf("数量应统一显示三位小数, got init=%s rem=%s", got.InitialQty, got.Remaining)
	}
	if got.ParentID != "" || len(got.Children) != 0 {
		t.Fatalf("原样不应有来源或子样: %+v", got)
	}
	if len(got.History) != 1 || got.History[0].Kind != "register" ||
		!got.History[0].Time.Equal(now) || got.History[0].Holder != "张三" {
		t.Fatalf("登记历史不正确: %+v", got.History)
	}
	if got.PendingTransfer != nil {
		t.Fatalf("新登记样品不应有待确认交接")
	}
}

func TestRegisterValidation(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "S1", "1", "h", "l")

	cases := []struct {
		name string
		in   RegisterInput
		want error
	}{
		{"空白编号", RegisterInput{ID: "   ", Qty: "1", Holder: "h", Location: "l"}, ErrInvalid},
		{"空白持有人", RegisterInput{ID: "S2", Qty: "1", Holder: " ", Location: "l"}, ErrInvalid},
		{"空白地点", RegisterInput{ID: "S2", Qty: "1", Holder: "h", Location: ""}, ErrInvalid},
		{"零数量", RegisterInput{ID: "S2", Qty: "0", Holder: "h", Location: "l"}, ErrInvalid},
		{"负数量", RegisterInput{ID: "S2", Qty: "-1", Holder: "h", Location: "l"}, ErrInvalid},
		{"四位小数", RegisterInput{ID: "S2", Qty: "1.0001", Holder: "h", Location: "l"}, ErrInvalid},
		{"非数字", RegisterInput{ID: "S2", Qty: "abc", Holder: "h", Location: "l"}, ErrInvalid},
		{"科学计数", RegisterInput{ID: "S2", Qty: "1e3", Holder: "h", Location: "l"}, ErrInvalid},
		{"点开头", RegisterInput{ID: "S2", Qty: ".5", Holder: "h", Location: "l"}, ErrInvalid},
		{"重复编号", RegisterInput{ID: "S1", Qty: "1", Holder: "h", Location: "l"}, ErrConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s.Register(c.in); !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}

	// 被拒绝的登记不得顺带创建记录。
	if _, err := s.GetSample("S2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败的登记不应创建样品, got %v", err)
	}
}

func TestQuantityBoundary(t *testing.T) {
	s, _ := fixedStore(t)
	// 0.001 是最小合法数量；三位小数可精确分装不丢失。
	mustRegister(t, s, "T", "0.001", "h", "l")
	got, err := s.GetSample("T")
	if err != nil {
		t.Fatal(err)
	}
	if got.InitialQty != "0.001" {
		t.Fatalf("got %s", got.InitialQty)
	}
}

func TestSplitSuccessAndChaining(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "10.000", "张三", "实验室A")

	parent, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C1", Qty: "3.333"},
		{ID: "C2", Qty: "6.667"},
	}})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	// 整数刻度精确守恒：3.333 + 6.667 = 10.000，剩余正好为 0。
	if parent.Remaining != "0.000" {
		t.Fatalf("分装后剩余应为 0.000, got %s", parent.Remaining)
	}
	if len(parent.Children) != 2 || parent.Children[0] != "C1" || parent.Children[1] != "C2" {
		t.Fatalf("子样顺序不正确: %v", parent.Children)
	}

	for _, id := range []string{"C1", "C2"} {
		c, err := s.GetSample(id)
		if err != nil {
			t.Fatal(err)
		}
		if c.ParentID != "P" || c.Holder != "张三" || c.Location != "实验室A" {
			t.Fatalf("子样 %s 未正确继承: %+v", id, c)
		}
	}

	// 子样可以继续分装。
	c1, err := s.GetSample("C1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Split(SplitInput{ParentID: "C1", Parts: []SplitPart{
		{ID: "G1", Qty: "1.111"},
		{ID: "G2", Qty: "2.222"},
	}}); err != nil {
		t.Fatalf("子样继续分装: %v", err)
	}
	c1, _ = s.GetSample("C1")
	if c1.Remaining != "0.000" {
		t.Fatalf("C1 剩余应为 0.000, got %s", c1.Remaining)
	}
	g1, _ := s.GetSample("G1")
	if g1.ParentID != "C1" || g1.InitialQty != "1.111" {
		t.Fatalf("孙样关系错误: %+v", g1)
	}
}

func TestSplitRejectedIsAtomic(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "5.000", "h", "l")
	mustRegister(t, s, "EXIST", "1", "h", "l")

	before, _ := s.GetSample("P")

	// 超量：整次分装不生效。
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "A", Qty: "3.000"},
		{ID: "B", Qty: "2.001"}, // 合计 5.001 > 5.000
	}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("超量分装应返回 ErrConflict, got %v", err)
	}

	// 批次内编号重复。
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "A", Qty: "1"},
		{ID: "A", Qty: "1"},
	}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("批内重复编号应拒绝, got %v", err)
	}

	// 编号与既有样品冲突。
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "EXIST", Qty: "1"},
	}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("既有编号应拒绝, got %v", err)
	}

	// 某个子样数量非法（0.0001），即使总量不超也整批拒绝。
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "A", Qty: "1.0001"},
	}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("非法数量应拒绝, got %v", err)
	}

	// 空批次与不存在的来源。
	if _, err := s.Split(SplitInput{ParentID: "P"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("空批次应拒绝, got %v", err)
	}
	if _, err := s.Split(SplitInput{ParentID: "NOPE", Parts: []SplitPart{{ID: "A", Qty: "1"}}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在来源应 ErrNotFound, got %v", err)
	}

	after, _ := s.GetSample("P")
	if after.Remaining != before.Remaining || len(after.Children) != len(before.Children) ||
		len(after.History) != len(before.History) {
		t.Fatalf("被拒绝的分装改动了状态: before=%+v after=%+v", before, after)
	}
	for _, id := range []string{"A", "B"} {
		if _, err := s.GetSample(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("被拒绝分装不应创建子样 %s", id)
		}
	}
}

func TestZeroRemainingCannotFlow(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "1.000", "h", "l")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{{ID: "C", Qty: "1.000"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{{ID: "D", Qty: "0.001"}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("零剩余分装应拒绝, got %v", err)
	}
	if _, err := s.Handover(HandoverInput{
		TransferID: "T1", SampleID: "P",
		FromHolder: "h", FromLocation: "l",
		ToHolder: "x", ToLocation: "y",
		HandedOverAt: time.Now(),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("零剩余交接应拒绝, got %v", err)
	}
	// 零剩余样品仍可查询，历史保留。
	got, _ := s.GetSample("P")
	if got.Remaining != "0.000" || len(got.History) != 2 {
		t.Fatalf("零剩余样品历史应保留: %+v", got)
	}
}

func TestHandoverLifecycle(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "4.250", "张三", "实验室A")
	hAt := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	rAt := hAt.Add(time.Hour)

	in := HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt,
	}
	tr, err := s.Handover(in)
	if err != nil {
		t.Fatalf("handover: %v", err)
	}
	if tr.Confirmed || tr.Qty != "4.250" || tr.ToHolder != "李四" {
		t.Fatalf("交接初始视图错误: %+v", tr)
	}

	// 待确认期间持有人和地点不变。
	got, _ := s.GetSample("P")
	if got.Holder != "张三" || got.Location != "实验室A" {
		t.Fatalf("待确认期间不得改变持有人地点: %+v", got)
	}
	if got.PendingTransfer == nil || got.PendingTransfer.TransferID != "TR-1" {
		t.Fatalf("查询应能看到待确认交接详情: %+v", got.PendingTransfer)
	}

	// 待确认期间不能再次交接或分装。
	in2 := in
	in2.TransferID = "TR-2"
	if _, err := s.Handover(in2); !errors.Is(err, ErrConflict) {
		t.Fatalf("重复发起交接应拒绝, got %v", err)
	}
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{{ID: "C", Qty: "0.001"}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("待确认期间分装应拒绝, got %v", err)
	}

	// 错误的接收人/地点/时间均被拒绝。
	if _, err := s.Confirm(ConfirmInput{"TR-1", "王五", "实验室B", rAt}); !errors.Is(err, ErrConflict) {
		t.Fatalf("非指定接收人应拒绝, got %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-1", "李四", "实验室A", rAt}); !errors.Is(err, ErrConflict) {
		t.Fatalf("错误地点应拒绝, got %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-1", "李四", "实验室B", hAt.Add(-time.Second)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("接收早于交出应拒绝, got %v", err)
	}
	// 拒绝后仍处于待确认、位置不变。
	got, _ = s.GetSample("P")
	if got.Holder != "张三" || got.PendingTransfer == nil {
		t.Fatalf("被拒绝的接收改动了状态: %+v", got)
	}

	// 接收时间恰好等于交出时间应允许（不能早于）。
	done, err := s.Confirm(ConfirmInput{"TR-1", "李四", "实验室B", hAt})
	if err != nil {
		t.Fatalf("确认: %v", err)
	}
	if !done.Confirmed || done.ReceivedAt == nil || !done.ReceivedAt.Equal(hAt) {
		t.Fatalf("确认视图错误: %+v", done)
	}
	got, _ = s.GetSample("P")
	if got.Holder != "李四" || got.Location != "实验室B" || got.PendingTransfer != nil {
		t.Fatalf("确认后应改变持有人地点并结束待确认: %+v", got)
	}
	kinds := []string{}
	for _, h := range got.History {
		kinds = append(kinds, h.Kind)
	}
	wantKinds := []string{"register", "transfer-out", "transfer-in"}
	if len(kinds) != 3 {
		t.Fatalf("历史顺序错误: %v", kinds)
	}
	for i := range wantKinds {
		if kinds[i] != wantKinds[i] {
			t.Fatalf("历史顺序错误: %v", kinds)
		}
	}
}

func TestHandoverValidation(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "1.000", "张三", "实验室A")
	at := time.Now()

	base := func() HandoverInput {
		return HandoverInput{
			TransferID: "TR-1", SampleID: "P",
			FromHolder: "张三", FromLocation: "实验室A",
			ToHolder: "李四", ToLocation: "实验室B",
			HandedOverAt: at,
		}
	}
	cases := []struct {
		name string
		mut  func(HandoverInput) HandoverInput
		want error
	}{
		{"交出人不一致", func(i HandoverInput) HandoverInput { i.FromHolder = "王五"; return i }, ErrInvalid},
		{"交出地点不一致", func(i HandoverInput) HandoverInput { i.FromLocation = "X"; return i }, ErrInvalid},
		{"接收人相同", func(i HandoverInput) HandoverInput { i.ToHolder = "张三"; return i }, ErrInvalid},
		{"空白接收人", func(i HandoverInput) HandoverInput { i.ToHolder = "  "; return i }, ErrInvalid},
		{"零时间", func(i HandoverInput) HandoverInput { i.HandedOverAt = time.Time{}; return i }, ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s.Handover(c.mut(base())); !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
	// 样品不存在：不能创建交接。
	missing := base()
	missing.TransferID = "TR-X"
	missing.SampleID = "NOPE"
	if _, err := s.Handover(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在样品应 ErrNotFound, got %v", err)
	}
	if _, err := s.GetTransfer("TR-X"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败的交接不应被创建")
	}
	if _, err := s.Confirm(ConfirmInput{"NOPE", "李四", "实验室B", at}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在交接应 ErrNotFound, got %v", err)
	}
}

func TestTransferIdempotency(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "2.000", "张三", "A")
	mustRegister(t, s, "Q", "2.000", "张三", "A")
	at := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)

	in := HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "A",
		ToHolder: "李四", ToLocation: "B", HandedOverAt: at,
	}
	first, err := s.Handover(in)
	if err != nil {
		t.Fatal(err)
	}
	// 待确认状态下重复提交相同请求：返回原交接。
	again, err := s.Handover(in)
	if err != nil {
		t.Fatalf("重复提交不应报错: %v", err)
	}
	if again != first && again.TransferID != first.TransferID {
		t.Fatalf("重复提交应返回原交接")
	}
	if len(s.data.Transfers) != 1 {
		t.Fatalf("重复提交不得新增交接记录")
	}

	// 沿用编号改内容 → 拒绝（待确认期间）。
	mutated := in
	mutated.ToLocation = "C"
	if _, err := s.Handover(mutated); !errors.Is(err, ErrConflict) {
		t.Fatalf("同编号不同内容应拒绝, got %v", err)
	}
	mutated = in
	mutated.SampleID = "Q"
	if _, err := s.Handover(mutated); !errors.Is(err, ErrConflict) {
		t.Fatalf("同编号换样品应拒绝, got %v", err)
	}
	mutated = in
	mutated.HandedOverAt = at.Add(time.Minute)
	if _, err := s.Handover(mutated); !errors.Is(err, ErrConflict) {
		t.Fatalf("同编号改时间应拒绝, got %v", err)
	}

	// 确认后：重复相同接收信息返回原结果，不增加转手记录。
	if _, err := s.Confirm(ConfirmInput{"TR-1", "李四", "B", at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	sampleAfter, _ := s.GetSample("P")
	if _, err := s.Confirm(ConfirmInput{"TR-1", "李四", "B", at.Add(time.Hour)}); err != nil {
		t.Fatalf("已确认交接重复相同接收应返回原结果: %v", err)
	}
	if _, err := s.Handover(in); err != nil {
		t.Fatalf("已确认交接重复相同交出请求应返回原交接: %v", err)
	}
	sampleSame, _ := s.GetSample("P")
	if len(sampleSame.History) != len(sampleAfter.History) {
		t.Fatalf("幂等重复不得增加转手/历史记录: before=%d after=%d",
			len(sampleAfter.History), len(sampleSame.History))
	}

	// 已确认后接收信息不同 → 拒绝。
	if _, err := s.Confirm(ConfirmInput{"TR-1", "李四", "B", at.Add(2 * time.Hour)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("不同接收时间应拒绝, got %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-1", "张三", "B", at.Add(time.Hour)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("不同接收人应拒绝, got %v", err)
	}
}

func TestParentChildTransfersIndependent(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "10.000", "张三", "A")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C", Qty: "4.000"},
	}}); err != nil {
		t.Fatal(err)
	}
	// 父样剩余 6.000，子样 4.000，各自持有。
	at := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)

	if _, err := s.Handover(HandoverInput{
		TransferID: "TP", SampleID: "P",
		FromHolder: "张三", FromLocation: "A",
		ToHolder: "李四", ToLocation: "B", HandedOverAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Confirm(ConfirmInput{"TP", "李四", "B", at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	// 父样的交接不带动子样：子样仍由张三在 A 持有，且可以独立交接。
	c, _ := s.GetSample("C")
	if c.Holder != "张三" || c.Location != "A" {
		t.Fatalf("子样不应被父样交接带动: %+v", c)
	}
	if _, err := s.Handover(HandoverInput{
		TransferID: "TC", SampleID: "C",
		FromHolder: "张三", FromLocation: "A",
		ToHolder: "王五", ToLocation: "C", HandedOverAt: at.Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("子样应能独立交接: %v", err)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub.json")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustRegister(t, s, "P", "7.500", "张三", "A")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{{ID: "C", Qty: "2.500"}}}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "A",
		ToHolder: "李四", ToLocation: "B", HandedOverAt: at,
	}); err != nil {
		t.Fatal(err)
	}

	// 关闭后重新打开同一文件。
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s2.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if p.InitialQty != "7.500" || p.Remaining != "5.000" || p.Holder != "张三" {
		t.Fatalf("重开后父样状态错误: %+v", p)
	}
	if len(p.Children) != 1 || p.Children[0] != "C" {
		t.Fatalf("重开后子样关系丢失: %+v", p)
	}
	if p.PendingTransfer == nil || p.PendingTransfer.TransferID != "TR-1" || p.PendingTransfer.Confirmed {
		t.Fatalf("重开后待确认交接详情应保留: %+v", p.PendingTransfer)
	}

	// 交接编号的重复提交判断仍然有效：相同内容返回原交接，改内容拒绝。
	same := HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "A",
		ToHolder: "李四", ToLocation: "B", HandedOverAt: at,
	}
	if _, err := s2.Handover(same); err != nil {
		t.Fatalf("重开后相同请求应幂等返回: %v", err)
	}
	diff := same
	diff.ToLocation = "Z"
	if _, err := s2.Handover(diff); !errors.Is(err, ErrConflict) {
		t.Fatalf("重开后编号占用判断应继续生效, got %v", err)
	}

	// 确认后再次重开，已确认状态保留。
	if _, err := s2.Confirm(ConfirmInput{"TR-1", "李四", "B", at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	s3, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s3.GetTransfer("TR-1")
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Confirmed || tr.ConfirmedBy != "李四" {
		t.Fatalf("重开后确认状态丢失: %+v", tr)
	}
	p, _ = s3.GetSample("P")
	if p.Holder != "李四" || p.Location != "B" || p.PendingTransfer != nil {
		t.Fatalf("重开后持有人状态错误: %+v", p)
	}
}

func TestReopenMissingFileCreates(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "fresh.json"))
	if err != nil {
		t.Fatal(err)
	}
	mustRegister(t, s, "X", "1.000", "h", "l")
}

func TestNotFoundDoesNotCreate(t *testing.T) {
	s, _ := fixedStore(t)
	if _, err := s.GetSample("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.GetTransfer("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if len(s.data.Samples) != 0 || len(s.data.Transfers) != 0 {
		t.Fatalf("查询不存在的记录不得创建数据")
	}
}
