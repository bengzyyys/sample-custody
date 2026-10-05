package custody

import (
	"errors"
	"testing"
	"time"
)

// 销毁时间边界回归的公共前置：
//
//	原样 P 初始 10.000，由张三在实验室A交给李四并在实验室B确认接收，
//	随后李四分出 4.000 子样 C，原样剩余 6.000。
//
// 关键在于历史的“追加次序”和“时间先后”不一致：
//
//	历史追加次序：register -> transfer-out -> transfer-in -> split
//	实际时间先后：register -> transfer-out -> split(sAt) -> transfer-in(rAt)
//
// 分装记录虽最后追加，时间却早于此前的接收记录（分装没有外部时间，
// 沿用创建时刻，保持既有时间处理行为）。因此最晚历史时刻是接收时间
// rAt，而不是历史末尾的分装时间 sAt。
//
// 返回登记、交出、接收、分装四个实际时刻。
func setupReceivedThenSplit(t *testing.T) (s *Store, regAt, hAt, rAt, sAt time.Time) {
	t.Helper()
	s, _ = fixedStore(t)

	regAt = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	hAt = time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	rAt = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	sAt = time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC) // 晚于交出、早于接收

	s.now = func() time.Time { return regAt }
	mustRegister(t, s, "P", "10.000", "张三", "实验室A")

	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt,
	}); err != nil {
		t.Fatalf("发起交接: %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-1", "李四", "实验室B", rAt}); err != nil {
		t.Fatalf("确认接收: %v", err)
	}

	// 分装在接收之后才追加，时钟却拨到接收之前：历史末尾不是最晚时刻。
	s.now = func() time.Time { return sAt }
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C", Qty: "4.000"},
	}}); err != nil {
		t.Fatalf("分装: %v", err)
	}

	p, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	// 前置条件自检：确保场景确实是“末尾更早、接收最晚”，避免测试空转。
	if len(p.History) != 4 {
		t.Fatalf("前置条件：原样应有 4 条历史, got %+v", p.History)
	}
	wantKinds := []string{"register", "transfer-out", "transfer-in", "split"}
	for i, k := range wantKinds {
		if p.History[i].Kind != k {
			t.Fatalf("前置条件：历史追加次序错误: %+v", p.History)
		}
	}
	if !p.History[len(p.History)-1].Time.Equal(sAt) {
		t.Fatalf("前置条件：历史末尾应为分装时刻 %s, got %s",
			sAt, p.History[len(p.History)-1].Time)
	}
	if !sAt.Before(rAt) {
		t.Fatalf("前置条件：分装时间应早于接收时间")
	}
	if p.Remaining != "6.000" || p.Holder != "李四" || p.Location != "实验室B" {
		t.Fatalf("前置条件：原样应由李四在实验室B持有剩余 6.000: %+v", p)
	}
	if p.PendingTransfer != nil {
		t.Fatalf("前置条件：接收完成后不应有待确认交接")
	}
	return s, regAt, hAt, rAt, sAt
}

// sameHistory 逐条比较保管历史的内容与次序（含时间实际时刻与详情文本）。
func sameHistory(a, b []History) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || !a[i].Time.Equal(b[i].Time) ||
			a[i].Holder != b[i].Holder || a[i].Location != b[i].Location ||
			a[i].Detail != b[i].Detail {
			return false
		}
	}
	return true
}

// TestDestroyTimeBoundChecksAllHistoryNotLastEntry 销毁时间晚于历史末尾的
// 分装时间、却早于更早追加的接收时间时，必须返回 ErrInvalid；不能因为
// 比较对象只是最后一条历史就接受。
func TestDestroyTimeBoundChecksAllHistoryNotLastEntry(t *testing.T) {
	s, _, _, rAt, sAt := setupReceivedThenSplit(t)

	before, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	childBefore, err := s.GetSample("C")
	if err != nil {
		t.Fatal(err)
	}

	// dMid 晚于末尾的分装（10:00），早于接收（12:00）。
	dMid := time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC)
	in := DestroyInput{
		SampleID: "P", Operator: "李四", Location: "实验室B",
		At: dMid, Reason: "实验结束按规程销毁",
	}
	if _, err := s.Destroy(in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("晚于分装但早于接收的销毁时间应返回 ErrInvalid, got %v", err)
	}

	// 同一实际时刻换 UTC+8 表示（当地钟点 19:00）仍须拒绝：
	// 判断只看实际时刻，不能按显示钟点或时区名称决定先后。
	tokyo := time.FixedZone("UTC+8", 8*60*60)
	inZone := in
	inZone.At = dMid.In(tokyo)
	if _, err := s.Destroy(inZone); !errors.Is(err, ErrInvalid) {
		t.Fatalf("换时区表示同一过早时刻仍应返回 ErrInvalid, got %v", err)
	}

	// 再早于最晚历史 1 秒也拒绝（边界的严格一侧）。
	tooEarly := in
	tooEarly.At = rAt.Add(-time.Second)
	if _, err := s.Destroy(tooEarly); !errors.Is(err, ErrInvalid) {
		t.Fatalf("早于最晚历史 1 秒应返回 ErrInvalid, got %v", err)
	}

	// 拒绝后原样状态完整保留。
	after, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if after.Remaining != "6.000" || after.InitialQty != "10.000" {
		t.Fatalf("拒绝后数量应不变: init=%s rem=%s", after.InitialQty, after.Remaining)
	}
	if after.Holder != "李四" || after.Location != "实验室B" {
		t.Fatalf("拒绝后持有人/地点应不变: %+v", after)
	}
	if after.ParentID != "" || len(after.Children) != 1 || after.Children[0] != "C" {
		t.Fatalf("拒绝后来源关系与子样列表应不变: parent=%q children=%v",
			after.ParentID, after.Children)
	}
	if after.PendingTransfer != nil {
		t.Fatalf("拒绝不应产生待确认交接: %+v", after.PendingTransfer)
	}
	if after.Destruction != nil {
		t.Fatalf("拒绝后不应出现销毁信息: %+v", after.Destruction)
	}
	if !sameHistory(after.History, before.History) {
		t.Fatalf("拒绝后已有历史内容与次序应保持原样:\nbefore=%+v\nafter=%+v",
			before.History, after.History)
	}
	// 历史末尾没有被追加销毁事件，仍是时间更早的分装事件。
	if last := after.History[len(after.History)-1]; last.Kind == "destroy" ||
		!last.Time.Equal(sAt) {
		t.Fatalf("历史末尾应仍是原分装事件: %+v", last)
	}

	// 子样同样不受失败销毁影响。
	childAfter, err := s.GetSample("C")
	if err != nil {
		t.Fatal(err)
	}
	if childAfter.ParentID != "P" || childAfter.Remaining != "4.000" ||
		childAfter.InitialQty != "4.000" || childAfter.Holder != "李四" ||
		childAfter.Location != "实验室B" || childAfter.Destruction != nil {
		t.Fatalf("拒绝销毁原样不应改动子样: %+v", childAfter)
	}
	if !sameHistory(childAfter.History, childBefore.History) {
		t.Fatalf("子样历史不应变化: before=%+v after=%+v",
			childBefore.History, childAfter.History)
	}
}

// TestDestroyAtLatestHistoryInstantSucceedsAcrossZones 销毁时间恰好等于
// 自身最晚历史时刻（接收时刻）时允许销毁，即使该时刻用另一时区表示；
// 此前被拒绝的请求不影响后续合法销毁。
func TestDestroyAtLatestHistoryInstantSucceedsAcrossZones(t *testing.T) {
	s, regAt, hAt, rAt, sAt := setupReceivedThenSplit(t)
	tokyo := time.FixedZone("UTC+8", 8*60*60)

	// 先制造一次过早请求并被拒绝，确认失败不留下任何痕迹。
	tooEarly := DestroyInput{
		SampleID: "P", Operator: "李四", Location: "实验室B",
		At: rAt.Add(-time.Minute), Reason: "实验结束按规程销毁",
	}
	if _, err := s.Destroy(tooEarly); !errors.Is(err, ErrInvalid) {
		t.Fatalf("前置：过早销毁应 ErrInvalid, got %v", err)
	}

	// 恰好等于最晚历史时刻，用 UTC+8 表示（当地 20:00 == UTC 12:00）。
	atBoundary := tooEarly
	atBoundary.At = rAt.In(tokyo)
	destroyed, err := s.Destroy(atBoundary)
	if err != nil {
		t.Fatalf("销毁时间等于最晚历史时刻应允许（跨时区表示）: %v", err)
	}
	if destroyed.Remaining != "0.000" {
		t.Fatalf("销毁后剩余量应为 0.000, got %s", destroyed.Remaining)
	}
	if destroyed.InitialQty != "10.000" {
		t.Fatalf("初始量应保留, got %s", destroyed.InitialQty)
	}
	d := destroyed.Destruction
	if d == nil {
		t.Fatalf("成功销毁应返回销毁信息")
	}
	if d.Operator != "李四" || d.Location != "实验室B" ||
		d.Reason != "实验结束按规程销毁" || d.Qty != "6.000" || !d.At.Equal(rAt) {
		t.Fatalf("销毁信息（操作人/地点/原因/时间/实际销毁量）错误: %+v", d)
	}
	if d.At.Location() != time.UTC {
		// 内部统一按实际时刻保存，不依赖入参时区。
		t.Fatalf("销毁时间应按实际时刻规范化保存, got location %s", d.At.Location())
	}

	// 原有历史保留，销毁事件追加在末尾；时间按实际时刻相等。
	wantKinds := []string{"register", "transfer-out", "transfer-in", "split", "destroy"}
	if len(destroyed.History) != len(wantKinds) {
		t.Fatalf("历史条数错误: %+v", destroyed.History)
	}
	for i, k := range wantKinds {
		if destroyed.History[i].Kind != k {
			t.Fatalf("历史内容或次序错误: %+v", destroyed.History)
		}
	}
	wantTimes := []time.Time{regAt, hAt, rAt, sAt, rAt}
	for i, want := range wantTimes {
		if !destroyed.History[i].Time.Equal(want) {
			t.Fatalf("第 %d 条历史时间错误: got %s want %s",
				i, destroyed.History[i].Time, want)
		}
	}
	last := destroyed.History[len(destroyed.History)-1]
	if last.Holder != "李四" || last.Location != "实验室B" {
		t.Fatalf("销毁事件的持有人/地点错误: %+v", last)
	}
	if destroyed.Holder != "李四" || destroyed.Location != "实验室B" {
		t.Fatalf("销毁后持有人/地点应保留为销毁前记录: %+v", destroyed)
	}

	// 再次查询结果一致：仍为 0.000 且销毁信息完整。
	again, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if again.Remaining != "0.000" || again.Destruction == nil ||
		again.Destruction.Qty != "6.000" || !again.Destruction.At.Equal(rAt) {
		t.Fatalf("重新查询销毁结果错误: %+v", again)
	}
	if !sameHistory(again.History, destroyed.History) {
		t.Fatalf("重新查询历史应与销毁返回一致")
	}
}

// TestDestroyTimeBoundIgnoresChildHistory 销毁时间只看被销毁样品自身的
// 历史：已分出的子样另有更晚的交出/接收记录，不推迟原样的可销毁时间，
// 实际销毁量也只含原样剩余量，子样一切不变。
func TestDestroyTimeBoundIgnoresChildHistory(t *testing.T) {
	s, _, _, rAt, sAt := setupReceivedThenSplit(t)

	// 子样 C 此后独立交接并完成接收，时间远晚于原样自身的最晚历史。
	cHandAt := rAt.Add(2 * time.Hour) // 14:00
	cRecvAt := rAt.Add(3 * time.Hour) // 15:00
	if _, err := s.Handover(HandoverInput{
		TransferID: "TC", SampleID: "C",
		FromHolder: "李四", FromLocation: "实验室B",
		ToHolder: "王五", ToLocation: "实验室C",
		HandedOverAt: cHandAt,
	}); err != nil {
		t.Fatalf("子样交出: %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TC", "王五", "实验室C", cRecvAt}); err != nil {
		t.Fatalf("子样接收: %v", err)
	}

	// 原样销毁时间满足自身边界（晚于 rAt），但严格早于子样的更晚记录。
	destroyPAt := rAt.Add(30 * time.Minute) // 12:30，早于子样交出 14:00
	p, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "李四", Location: "实验室B",
		At: destroyPAt, Reason: "实验结束按规程销毁",
	})
	if err != nil {
		t.Fatalf("原样满足自身时间条件即可销毁，不受子样更晚历史影响: %v", err)
	}
	if p.Remaining != "0.000" || p.Destruction == nil || p.Destruction.Qty != "6.000" {
		t.Fatalf("原样实际销毁量应只含自身剩余量 6.000: %+v", p)
	}
	if !p.Destruction.At.Equal(destroyPAt) {
		t.Fatalf("原样销毁时间错误: got %s want %s", p.Destruction.At, destroyPAt)
	}
	if len(p.Children) != 1 || p.Children[0] != "C" || p.ParentID != "" {
		t.Fatalf("销毁原样应保留父子关系: parent=%q children=%v", p.ParentID, p.Children)
	}
	// 原样历史只追加自身销毁事件，不混入子样事件。
	wantKinds := []string{"register", "transfer-out", "transfer-in", "split", "destroy"}
	if len(p.History) != len(wantKinds) {
		t.Fatalf("原样历史条数错误: %+v", p.History)
	}
	for i, k := range wantKinds {
		if p.History[i].Kind != k {
			t.Fatalf("原样历史内容/次序错误: %+v", p.History)
		}
	}

	// 子样数量、持有人、地点、接收状态与自身保管历史全部不变。
	c, err := s.GetSample("C")
	if err != nil {
		t.Fatal(err)
	}
	if c.ParentID != "P" || c.InitialQty != "4.000" || c.Remaining != "4.000" {
		t.Fatalf("子样数量与来源关系应不变: %+v", c)
	}
	if c.Holder != "王五" || c.Location != "实验室C" {
		t.Fatalf("子样持有人/地点应不变: %+v", c)
	}
	if c.Destruction != nil || c.PendingTransfer != nil {
		t.Fatalf("子样不应出现销毁信息或待确认交接: destruction=%+v pending=%+v",
			c.Destruction, c.PendingTransfer)
	}
	cWantKinds := []string{"split", "transfer-out", "transfer-in"}
	cWantTimes := []time.Time{sAt, cHandAt, cRecvAt}
	if len(c.History) != len(cWantKinds) {
		t.Fatalf("子样历史条数应不变: %+v", c.History)
	}
	for i, k := range cWantKinds {
		if c.History[i].Kind != k || !c.History[i].Time.Equal(cWantTimes[i]) {
			t.Fatalf("子样历史内容/次序/时间应不变: %+v", c.History)
		}
	}
	tr, err := s.GetTransfer("TC")
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Confirmed || tr.ConfirmedBy != "王五" || tr.Qty != "4.000" ||
		tr.ReceivedAt == nil || !tr.ReceivedAt.Equal(cRecvAt) {
		t.Fatalf("子样交接的已确认状态应不变: %+v", tr)
	}
}
