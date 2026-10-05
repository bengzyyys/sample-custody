package custody

import (
	"errors"
	"testing"
	"time"
)

// 销毁信息隔离的公共前置：一份已分出子样、并已合法销毁全部剩余量的原样。
// 原样 P：初始 10.000，分出 C1 4.000 后剩余 6.000，再由张三在实验室A
// 以原因“实验结束按规程销毁”销毁剩余 6.000。返回销毁发生的时刻。
func setupDestroyedParent(t *testing.T) (*Store, time.Time) {
	t.Helper()
	s, now := fixedStore(t)
	mustRegister(t, s, "P", "10.000", "张三", "实验室A")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C1", Qty: "4.000"},
	}}); err != nil {
		t.Fatalf("split: %v", err)
	}
	dAt := now.Add(2 * time.Hour)
	if _, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "张三", Location: "实验室A",
		At: dAt, Reason: "实验结束按规程销毁",
	}); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	return s, dAt
}

// 校验 P 的查询结果仍是真实记录：剩余 0.000、初始 10.000、子样 [C1]、
// 三条历史（register、split、destroy），销毁信息与首次记录一致。
func requirePristineDestroyed(t *testing.T, got *Sample, dAt time.Time) {
	t.Helper()
	if got.Remaining != "0.000" || got.InitialQty != "10.000" {
		t.Fatalf("数量被改写: init=%s rem=%s", got.InitialQty, got.Remaining)
	}
	if got.Holder != "张三" || got.Location != "实验室A" {
		t.Fatalf("销毁后持有人/地点应保留销毁前记录: %+v", got)
	}
	if len(got.Children) != 1 || got.Children[0] != "C1" {
		t.Fatalf("子样列表被改写: %v", got.Children)
	}
	if len(got.History) != 3 {
		t.Fatalf("历史条数被改写: %d", len(got.History))
	}
	wantKinds := []string{"register", "split", "destroy"}
	for i, k := range wantKinds {
		if got.History[i].Kind != k {
			t.Fatalf("历史内容被改写: %+v", got.History)
		}
	}
	d := got.Destruction
	if d == nil {
		t.Fatalf("销毁信息丢失")
	}
	if d.Operator != "张三" || d.Location != "实验室A" ||
		!d.At.Equal(dAt) || d.Reason != "实验结束按规程销毁" || d.Qty != "6.000" {
		t.Fatalf("销毁信息被改写: %+v", d)
	}
}

// 销毁操作返回的详情与查询取得的详情都是隔离副本：改写其中的销毁操作人、
// 地点、时间、原因、实际销毁量，甚至直接移除销毁信息，都不影响真实记录。
func TestDestructionViewIsIsolatedCopy(t *testing.T) {
	s, dAt := setupDestroyedParent(t)

	got, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	requirePristineDestroyed(t, got, dAt)

	// 调用方改写查询结果中的销毁信息各字段，并直接移除销毁信息。
	got.Destruction.Operator = "王五"
	got.Destruction.Location = "仓库Z"
	got.Destruction.At = dAt.Add(5 * time.Hour)
	got.Destruction.Reason = "篡改"
	got.Destruction.Qty = "0.001"
	got.Destruction = nil
	got.Remaining = "9.999"

	// 再次查询仍应看到首次记录的真实销毁信息。
	again, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	requirePristineDestroyed(t, again, dAt)

	// 已分出的子样仍保留原来的数量和来源关系。
	c1, err := s.GetSample("C1")
	if err != nil {
		t.Fatal(err)
	}
	if c1.ParentID != "P" || c1.InitialQty != "4.000" || c1.Remaining != "4.000" ||
		c1.Destruction != nil {
		t.Fatalf("子样记录被改写: %+v", c1)
	}
}

// 先后取得的两份详情互不影响：深改一份，另一份仍是首次记录的内容。
func TestDestructionViewsAreIndependent(t *testing.T) {
	s, dAt := setupDestroyedParent(t)

	first, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}

	// 深改第一份结果的销毁信息，第二份已经取得的结果应保持原样。
	first.Destruction.Operator = "王五"
	first.Destruction.Reason = "篡改"
	first.Destruction.Qty = "0.001"
	first.Destruction.At = dAt.Add(5 * time.Hour)
	first.Remaining = "9.999"

	requirePristineDestroyed(t, second, dAt)
}

// 副本修改不能影响后续销毁请求的幂等判断：按首次真实信息重复提交返回
// 原结果；改用副本中改过但仍合法的信息则返回 ErrConflict，不覆盖原记录。
func TestDestructionMutationDoesNotAffectResubmission(t *testing.T) {
	s, dAt := setupDestroyedParent(t)

	// 调用方在查询副本上改出另一套仍合法的销毁信息。
	got, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	got.Destruction.Reason = "过期"
	got.Destruction.At = dAt.Add(time.Hour)

	// 按首次真实的操作人、地点、时间和原因重复提交：返回原销毁结果，不增加历史。
	orig := DestroyInput{
		SampleID: "P", Operator: "张三", Location: "实验室A",
		At: dAt, Reason: "实验结束按规程销毁",
	}
	again, err := s.Destroy(orig)
	if err != nil {
		t.Fatalf("完全相同的重复提交应返回原结果, got %v", err)
	}
	requirePristineDestroyed(t, again, dAt)

	// 改用副本中改过但仍合法的原因或时间：返回 ErrConflict，不覆盖首次记录。
	byReason := orig
	byReason.Reason = "过期"
	if _, err := s.Destroy(byReason); !errors.Is(err, ErrConflict) {
		t.Fatalf("按篡改原因重复销毁应 ErrConflict, got %v", err)
	}
	byAt := orig
	byAt.At = dAt.Add(time.Hour)
	if _, err := s.Destroy(byAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("按篡改时间重复销毁应 ErrConflict, got %v", err)
	}
	cur, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	requirePristineDestroyed(t, cur, dAt)

	// 相同请求返回的详情同样是隔离副本：清空这份结果的销毁信息，
	// 不能把样品恢复为未销毁；下一次查询与重复提交仍以首次销毁信息为准。
	again.Destruction = nil
	again.Remaining = "6.000"
	after, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	requirePristineDestroyed(t, after, dAt)
	onceMore, err := s.Destroy(orig)
	if err != nil {
		t.Fatalf("清空副本后相同请求仍应幂等返回原结果, got %v", err)
	}
	requirePristineDestroyed(t, onceMore, dAt)
}

// 销毁信息原本为空的情况：剩余量因全部分装归零的样品没有销毁记录，
// 调用方在返回详情中自行填入销毁信息，不能把它变成已销毁样品。
func TestEmptyDestructionStaysIsolated(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "Q", "2.000", "张三", "实验室A")
	if _, err := s.Split(SplitInput{ParentID: "Q", Parts: []SplitPart{
		{ID: "Q-C1", Qty: "2.000"},
	}}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetSample("Q")
	if err != nil {
		t.Fatal(err)
	}
	if got.Remaining != "0.000" || got.Destruction != nil {
		t.Fatalf("前置条件：分装用尽且没有销毁记录: %+v", got)
	}
	historyLen := len(got.History)

	// 调用方在返回详情中自行填入一份销毁信息。
	got.Destruction = &Destruction{
		Operator: "张三", Location: "实验室A",
		At: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
		Reason:   "自行填入", Qty: "2.000",
	}

	// 再次查询仍应没有销毁信息。
	again, err := s.GetSample("Q")
	if err != nil {
		t.Fatal(err)
	}
	if again.Destruction != nil {
		t.Fatalf("自行填入的销毁信息不应进入真实记录: %+v", again.Destruction)
	}
	if len(again.History) != historyLen {
		t.Fatalf("历史不应变化: before=%d after=%d", historyLen, len(again.History))
	}

	// 尝试销毁仍按零剩余量规则返回 ErrConflict，不追加销毁历史。
	if _, err := s.Destroy(DestroyInput{
		SampleID: "Q", Operator: "张三", Location: "实验室A",
		At: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC), Reason: "自行填入",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("零剩余量样品销毁应 ErrConflict, got %v", err)
	}
	final, err := s.GetSample("Q")
	if err != nil {
		t.Fatal(err)
	}
	if final.Destruction != nil || len(final.History) != historyLen {
		t.Fatalf("被拒绝的销毁改动了状态: %+v", final)
	}
}
