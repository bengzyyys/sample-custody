package custody

import (
	"errors"
	"testing"
	"time"
)

// 销毁信息隔离的公共前置：原样 P 初始 10.000，分出子样 C1 4.000 后
// 剩余 6.000，再由持有人张三在实验室A 合法销毁全部剩余量。
// 返回销毁入参（首次真实记录）与销毁结果。
func setupDestroyedParent(t *testing.T) (*Store, DestroyInput, *Sample) {
	t.Helper()
	s, now := fixedStore(t)
	mustRegister(t, s, "P", "10.000", "张三", "实验室A")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C1", Qty: "4.000"},
	}}); err != nil {
		t.Fatalf("split: %v", err)
	}
	in := DestroyInput{
		SampleID: "P", Operator: "张三", Location: "实验室A",
		At: now.Add(2 * time.Hour), Reason: "实验结束按规程销毁",
	}
	destroyed, err := s.Destroy(in)
	if err != nil {
		t.Fatalf("destroy: %v", err)
	}
	return s, in, destroyed
}

// 校验 P 的查询结果仍是首次销毁的真实记录：剩余 0.000、初始 10.000、
// 销毁操作人/地点/时间/原因与首次一致、实际销毁量 6.000，
// 子样列表与既有保管历史（登记、分装、销毁）保留。
func requirePristineDestruction(t *testing.T, got *Sample, in DestroyInput) {
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
	wantKinds := []string{"register", "split", "destroy"}
	if len(got.History) != len(wantKinds) {
		t.Fatalf("历史条数被改写: %d", len(got.History))
	}
	for i, k := range wantKinds {
		if got.History[i].Kind != k {
			t.Fatalf("历史内容被改写: %+v", got.History)
		}
	}
	d := got.Destruction
	if d == nil {
		t.Fatalf("销毁信息丢失")
	}
	if d.Operator != in.Operator || d.Location != in.Location ||
		!d.At.Equal(in.At) || d.Reason != in.Reason || d.Qty != "6.000" {
		t.Fatalf("销毁信息被改写: %+v", d)
	}
}

// 销毁操作返回的详情是隔离副本：改写其中的销毁操作人、地点、时间、
// 原因、实际销毁量，甚至直接移除销毁信息，都不影响真实记录。
func TestDestroyResultIsIsolatedCopy(t *testing.T) {
	s, in, destroyed := setupDestroyedParent(t)
	requirePristineDestruction(t, destroyed, in)

	// 调用方改写销毁返回结果中的销毁信息，并直接移除销毁信息。
	destroyed.Destruction.Operator = "王五"
	destroyed.Destruction.Location = "仓库Z"
	destroyed.Destruction.At = in.At.Add(24 * time.Hour)
	destroyed.Destruction.Reason = "篡改"
	destroyed.Destruction.Qty = "0.001"
	destroyed.Destruction = nil
	destroyed.Remaining = "6.000"

	// 再次查询仍应看到首次销毁的真实记录。
	again, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	requirePristineDestruction(t, again, in)

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

// 查询取得的详情同样是隔离副本：改写销毁信息或移除它不影响真实记录，
// 修改一份详情也不改变此前取得的另一份详情。
func TestDestroyQueryResultsAreIndependentCopies(t *testing.T) {
	s, in, _ := setupDestroyedParent(t)

	first, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}

	// 深改第一份查询结果的销毁信息。
	first.Destruction.Operator = "王五"
	first.Destruction.Location = "仓库Z"
	first.Destruction.At = in.At.Add(24 * time.Hour)
	first.Destruction.Reason = "篡改"
	first.Destruction.Qty = "0.001"

	// 此前取得的另一份详情保持原样。
	requirePristineDestruction(t, second, in)

	// 连销毁信息整体移除也不影响真实记录。
	second.Destruction = nil
	again, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	requirePristineDestruction(t, again, in)
}

// 副本修改不影响后续销毁请求的幂等判断：仍按首次真实信息重复提交
// 返回原销毁结果且不增加历史；改用副本中改过但仍合法的原因或时间
// 返回 ErrConflict，不覆盖首次销毁记录。相同请求返回的详情同样是
// 隔离副本，清空它不会把样品恢复为未销毁。
func TestDestructionCopiesDoNotAffectIdempotency(t *testing.T) {
	s, in, destroyed := setupDestroyedParent(t)
	historyLen := len(destroyed.History)

	// 先篡改一份查询副本，再按首次真实的操作人、地点、时间、原因重复提交。
	queried, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	queried.Destruction.Reason = "过期"
	queried.Destruction.At = in.At.Add(time.Hour)

	replay, err := s.Destroy(in)
	if err != nil {
		t.Fatalf("按首次真实信息重复提交应返回原销毁结果, got %v", err)
	}
	requirePristineDestruction(t, replay, in)
	if len(replay.History) != historyLen {
		t.Fatalf("重复提交不得追加历史: before=%d after=%d", historyLen, len(replay.History))
	}

	// 改用副本中改过但仍合法的原因或时间：ErrConflict，不覆盖首次记录。
	for name, mut := range map[string]func(DestroyInput) DestroyInput{
		"篡改后的原因": func(i DestroyInput) DestroyInput { i.Reason = "过期"; return i },
		"篡改后的时间": func(i DestroyInput) DestroyInput { i.At = in.At.Add(time.Hour); return i },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Destroy(mut(in)); !errors.Is(err, ErrConflict) {
				t.Fatalf("want ErrConflict, got %v", err)
			}
		})
	}
	cur, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	requirePristineDestruction(t, cur, in)

	// 相同请求返回的详情也是隔离副本：清空它不能恢复为未销毁。
	replay.Destruction = nil
	replay.Remaining = "6.000"
	again, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	requirePristineDestruction(t, again, in)

	// 下一次重复提交仍以首次销毁信息为准：相同请求幂等返回，不追加历史。
	final, err := s.Destroy(in)
	if err != nil {
		t.Fatalf("副本被清空后相同请求仍应幂等返回, got %v", err)
	}
	requirePristineDestruction(t, final, in)
	if len(final.History) != historyLen {
		t.Fatalf("重复提交不得追加历史: before=%d after=%d", historyLen, len(final.History))
	}
}

// 销毁信息原本为空的样品：调用方在返回详情中自行填入销毁信息，
// 不能把分装用尽的样品变成已销毁样品。
func TestSplitExhaustedSampleDestructionStaysEmpty(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "E", "2.000", "张三", "实验室A")
	if _, err := s.Split(SplitInput{ParentID: "E", Parts: []SplitPart{
		{ID: "E-C1", Qty: "2.000"},
	}}); err != nil {
		t.Fatalf("split: %v", err)
	}

	got, err := s.GetSample("E")
	if err != nil {
		t.Fatal(err)
	}
	if got.Remaining != "0.000" {
		t.Fatalf("前置条件：全部分装后剩余量应为 0.000, got %s", got.Remaining)
	}
	if got.Destruction != nil {
		t.Fatalf("分装用尽的样品不应有销毁信息: %+v", got.Destruction)
	}
	historyLen := len(got.History)

	// 调用方在返回详情中自行填入一份销毁信息。
	got.Destruction = &Destruction{
		Operator: "张三", Location: "实验室A",
		At: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
		Reason:   "伪造销毁", Qty: "2.000",
	}

	// 再次查询仍应没有销毁信息。
	again, err := s.GetSample("E")
	if err != nil {
		t.Fatal(err)
	}
	if again.Destruction != nil {
		t.Fatalf("自行填入的销毁信息不应进入真实记录: %+v", again.Destruction)
	}
	if again.Remaining != "0.000" || len(again.History) != historyLen {
		t.Fatalf("自行填入销毁信息不应改动数量或历史: %+v", again)
	}

	// 尝试销毁仍按零剩余量规则返回 ErrConflict，不追加销毁历史。
	if _, err := s.Destroy(DestroyInput{
		SampleID: "E", Operator: "张三", Location: "实验室A",
		At: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC), Reason: "伪造销毁",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("零剩余量样品销毁应 ErrConflict, got %v", err)
	}
	final, err := s.GetSample("E")
	if err != nil {
		t.Fatal(err)
	}
	if final.Destruction != nil || len(final.History) != historyLen ||
		final.Remaining != "0.000" {
		t.Fatalf("被拒绝的销毁改动了状态: %+v", final)
	}
}
