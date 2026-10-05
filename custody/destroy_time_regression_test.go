package custody

import (
	"errors"
	"testing"
	"time"
)

// 销毁时间回归的公共前置：原样 P 登记后交接给李四并确认接收，
// 随后（时钟回拨）又分出子样 C。历史按追加次序为
// register(09:00) → transfer-out(10:00) → transfer-in(12:00) → split(11:00)，
// 其中接收记录的时间晚于最后追加的分装记录。P 剩余 6.000，
// 由李四在实验室B 持有，无待确认交接。
//
// 返回接收时刻（即该样品最晚的历史时刻）与分装时刻。
func setupOutOfOrderHistory(t *testing.T) (*Store, time.Time, time.Time) {
	t.Helper()
	s, now := fixedStore(t) // 09:00 UTC
	mustRegister(t, s, "P", "10.000", "张三", "实验室A")

	handedAt := now.Add(time.Hour) // 10:00
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B", HandedOverAt: handedAt,
	}); err != nil {
		t.Fatalf("handover: %v", err)
	}
	receivedAt := now.Add(3 * time.Hour) // 12:00，最晚的历史时刻
	if _, err := s.Confirm(ConfirmInput{"TR-1", "李四", "实验室B", receivedAt}); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// 时钟回拨到接收之前再分装：分装记录追加在历史末尾，但时间更早。
	splitAt := now.Add(2 * time.Hour) // 11:00
	s.now = func() time.Time { return splitAt }
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C", Qty: "4.000"},
	}}); err != nil {
		t.Fatalf("split: %v", err)
	}
	return s, receivedAt, splitAt
}

// requireHistory 校验历史的内容与次序和预期完全一致。
func requireHistory(t *testing.T, got *Sample, want []History) {
	t.Helper()
	if len(got.History) != len(want) {
		t.Fatalf("历史条数错误: want %d, got %d (%+v)", len(want), len(got.History), got.History)
	}
	for i, w := range want {
		h := got.History[i]
		if h.Kind != w.Kind || !h.Time.Equal(w.Time) ||
			h.Holder != w.Holder || h.Location != w.Location || h.Detail != w.Detail {
			t.Fatalf("历史第 %d 条被改动: want %+v, got %+v", i, w, h)
		}
	}
}

// TestDestroyTimeComparedAgainstEveryHistoryEntry 销毁时间与该样品每一条
// 已有保管历史按实际时刻比较：历史末尾追加的记录未必时间最晚，
// 晚于末尾记录但早于更早接收记录的销毁时间必须被拒绝（ErrInvalid）。
func TestDestroyTimeComparedAgainstEveryHistoryEntry(t *testing.T) {
	s, receivedAt, splitAt := setupOutOfOrderHistory(t)

	before, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if before.Remaining != "6.000" || before.Holder != "李四" || before.Location != "实验室B" {
		t.Fatalf("前置状态错误: %+v", before)
	}
	wantHistory := append([]History(nil), before.History...)
	// 前置确认：最晚的历史时刻是接收记录，而不是末尾的分装记录。
	if !receivedAt.After(splitAt) || !before.History[2].Time.Equal(receivedAt) ||
		!before.History[3].Time.Equal(splitAt) {
		t.Fatalf("前置历史时间构造错误: %+v", before.History)
	}

	// 晚于分装时间（历史末尾）却早于接收时间的销毁请求必须被拒绝。
	tooEarly := splitAt.Add(30 * time.Minute) // 11:30，晚于 split、早于 receive
	if _, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "李四", Location: "实验室B",
		At: tooEarly, Reason: "实验结束按规程销毁",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("早于接收记录的销毁时间应返回 ErrInvalid, got %v", err)
	}

	// 显示钟点更晚、实际时刻更早（另一时区）同样必须被拒绝：
	// 13:00+08:00 实际是 05:00 UTC，早于接收时刻 12:00 UTC。
	shallower := time.Date(2026, 10, 2, 13, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	if !shallower.Before(receivedAt) {
		t.Fatalf("前置时间构造错误：13:00+08:00 应早于 12:00 UTC")
	}
	if _, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "李四", Location: "实验室B",
		At: shallower, Reason: "实验结束按规程销毁",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("按实际时刻早于历史的销毁时间应返回 ErrInvalid, got %v", err)
	}

	// 两次拒绝后：剩余量、持有人、地点、来源关系、子样列表不变，
	// 历史内容与次序保持原样，无销毁信息、无销毁事件。
	after, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if after.Remaining != "6.000" || after.Holder != "李四" || after.Location != "实验室B" {
		t.Fatalf("被拒绝的销毁改动了数量/持有人/地点: %+v", after)
	}
	if after.ParentID != "" || len(after.Children) != 1 || after.Children[0] != "C" {
		t.Fatalf("被拒绝的销毁改动了来源关系或子样列表: %+v", after)
	}
	if after.Destruction != nil {
		t.Fatalf("被拒绝的销毁留下了销毁信息: %+v", after.Destruction)
	}
	if after.PendingTransfer != nil {
		t.Fatalf("被拒绝的销毁改动了交接状态: %+v", after.PendingTransfer)
	}
	requireHistory(t, after, wantHistory)

	// 被拒绝后样品仍按未销毁处理：恰好等于自身最晚历史时刻（接收时刻）
	// 的销毁时间应正常销毁全部剩余量；同一时刻用另一时区表示，
	// 允许与否的判断一致（20:00+08:00 即 12:00 UTC）。
	atPlus8 := receivedAt.In(time.FixedZone("UTC+8", 8*3600))
	destroyed, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "李四", Location: "实验室B",
		At: atPlus8, Reason: "实验结束按规程销毁",
	})
	if err != nil {
		t.Fatalf("等于最晚历史时刻的销毁应成功: %v", err)
	}
	if destroyed.Remaining != "0.000" || destroyed.InitialQty != "10.000" {
		t.Fatalf("销毁后数量错误: init=%s rem=%s", destroyed.InitialQty, destroyed.Remaining)
	}
	d := destroyed.Destruction
	if d == nil {
		t.Fatalf("销毁后应带销毁信息")
	}
	if d.Operator != "李四" || d.Location != "实验室B" ||
		d.Reason != "实验结束按规程销毁" || d.Qty != "6.000" || !d.At.Equal(receivedAt) {
		t.Fatalf("销毁信息错误: %+v", d)
	}
	// 初始量与原有历史保留，销毁事件追加在末尾。
	requireHistory(t, destroyed, append(wantHistory, History{
		Kind: "destroy", Time: receivedAt, Holder: "李四", Location: "实验室B",
		Detail: "销毁全部剩余量 6.000 毫升，原因：实验结束按规程销毁",
	}))

	// 再次查询结果一致。
	again, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if again.Remaining != "0.000" || again.Destruction == nil ||
		again.Destruction.Qty != "6.000" || !again.Destruction.At.Equal(receivedAt) {
		t.Fatalf("再次查询销毁结果错误: %+v", again)
	}
	requireHistory(t, again, destroyed.History)
}

// TestDestroyTimeLimitIgnoresChildHistory 销毁时间限制只看被销毁样品自己
// 的历史：子样更晚的交出/接收记录不推迟原样可销毁的时间；原样满足自身
// 时间条件即可销毁，实际销毁量只含原样剩余量，子样完全不受影响。
func TestDestroyTimeLimitIgnoresChildHistory(t *testing.T) {
	s, now := fixedStore(t) // 09:00 UTC
	mustRegister(t, s, "P", "10.000", "张三", "实验室A")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C", Qty: "4.000"},
	}}); err != nil {
		t.Fatalf("split: %v", err)
	}
	// P 自己的历史最晚时刻为 09:00（register 与 split）。

	// 子样随后交接并确认，历史里出现更晚的时刻（10:00、11:00）。
	childHandedAt := now.Add(time.Hour)
	childReceivedAt := now.Add(2 * time.Hour)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-C", SampleID: "C",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B", HandedOverAt: childHandedAt,
	}); err != nil {
		t.Fatalf("handover child: %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-C", "李四", "实验室B", childReceivedAt}); err != nil {
		t.Fatalf("confirm child: %v", err)
	}
	childBefore, err := s.GetSample("C")
	if err != nil {
		t.Fatal(err)
	}

	// 原样在自身最晚历史时刻之后、子样更晚记录之前销毁：应成功。
	destroyAt := now.Add(30 * time.Minute) // 09:30，早于子样的 10:00/11:00
	destroyed, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "张三", Location: "实验室A",
		At: destroyAt, Reason: "剩余部分废弃",
	})
	if err != nil {
		t.Fatalf("子样的更晚历史不应推迟原样销毁: %v", err)
	}
	// 实际销毁量只包含原样剩余量 6.000，不含子样的 4.000。
	if destroyed.Remaining != "0.000" || destroyed.Destruction == nil ||
		destroyed.Destruction.Qty != "6.000" {
		t.Fatalf("原样销毁量错误: %+v", destroyed)
	}
	if len(destroyed.Children) != 1 || destroyed.Children[0] != "C" {
		t.Fatalf("销毁后父子关系应保留: %+v", destroyed.Children)
	}

	// 子样的数量、持有人、地点、保管历史与接收状态均保持不变。
	childAfter, err := s.GetSample("C")
	if err != nil {
		t.Fatal(err)
	}
	if childAfter.Remaining != "4.000" || childAfter.Holder != "李四" ||
		childAfter.Location != "实验室B" || childAfter.Destruction != nil {
		t.Fatalf("销毁原样不得改动子样: %+v", childAfter)
	}
	if childAfter.ParentID != "P" {
		t.Fatalf("子样来源关系应保留: %+v", childAfter)
	}
	requireHistory(t, childAfter, childBefore.History)
	tr, err := s.GetTransfer("TR-C")
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Confirmed || tr.ConfirmedBy != "李四" ||
		tr.ReceivedAt == nil || !tr.ReceivedAt.Equal(childReceivedAt) {
		t.Fatalf("子样接收状态应保持不变: %+v", tr)
	}
}
