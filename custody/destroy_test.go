package custody

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// destroyInput 构造一个基础的销毁入参，便于各用例按需修改。
func destroyInput(id, op, loc string, at time.Time, reason string) DestroyInput {
	return DestroyInput{
		SampleID:    id,
		Operator:    op,
		Location:    loc,
		DestroyedAt: at,
		Reason:      reason,
	}
}

func TestDestroySuccess(t *testing.T) {
	s, clock := fixedStore(t)
	mustRegister(t, s, "P", "5.000", "张三", "实验室A")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C", Qty: "2.000"},
	}}); err != nil {
		t.Fatal(err)
	}
	// 分装后剩余 3.000。
	at := clock.Add(time.Hour)
	got, err := s.Destroy(destroyInput("P", "张三", "实验室A", at, "实验剩余样品销毁"))
	if err != nil {
		t.Fatalf("destroy: %v", err)
	}

	if got.Remaining != "0.000" {
		t.Fatalf("销毁后剩余量应为 0.000, got %s", got.Remaining)
	}
	if got.InitialQty != "5.000" || got.ParentID != "" {
		t.Fatalf("初始量/来源关系应保留: %+v", got)
	}
	if len(got.Children) != 1 || got.Children[0] != "C" {
		t.Fatalf("子样列表应保留: %+v", got.Children)
	}
	// 持有人和地点保留为销毁前最后记录。
	if got.Holder != "张三" || got.Location != "实验室A" {
		t.Fatalf("销毁不应改变持有人地点: %+v", got)
	}
	if got.PendingTransfer != nil {
		t.Fatalf("销毁后不应有待确认交接: %+v", got.PendingTransfer)
	}

	if got.Destruction == nil {
		t.Fatalf("查询应能看到销毁记录")
	}
	d := got.Destruction
	if d.Operator != "张三" || d.Location != "实验室A" || d.Reason != "实验剩余样品销毁" {
		t.Fatalf("销毁记录人员/地点/原因错误: %+v", d)
	}
	if !d.DestroyedAt.Equal(at) {
		t.Fatalf("销毁时间错误: %+v", d.DestroyedAt)
	}
	if d.Qty != "3.000" {
		t.Fatalf("实际销毁量应为销毁时刻全部剩余量 3.000, got %s", d.Qty)
	}

	// 保管历史末尾追加销毁事件，原有历史保留。
	kinds := make([]string, 0, len(got.History))
	for _, h := range got.History {
		kinds = append(kinds, h.Kind)
	}
	wantKinds := []string{"register", "split", "destroy"}
	if len(kinds) != len(wantKinds) {
		t.Fatalf("历史顺序错误: %v", kinds)
	}
	for i := range wantKinds {
		if kinds[i] != wantKinds[i] {
			t.Fatalf("历史顺序错误: %v", kinds)
		}
	}
	last := got.History[len(got.History)-1]
	if !last.Time.Equal(at) || last.Holder != "张三" || last.Location != "实验室A" {
		t.Fatalf("销毁事件内容错误: %+v", last)
	}

	// 重新查询结果一致。
	again, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if again.Destruction == nil || again.Destruction.Qty != "3.000" || again.Remaining != "0.000" {
		t.Fatalf("重新查询应返回相同销毁记录: %+v", again.Destruction)
	}
}

func TestDestroyValidation(t *testing.T) {
	s, clock := fixedStore(t)
	mustRegister(t, s, "P", "1.000", "张三", "实验室A")
	at := clock.Add(time.Hour)

	cases := []struct {
		name string
		in   DestroyInput
	}{
		{"空白编号", destroyInput("   ", "张三", "实验室A", at, "原因")},
		{"空白操作人", destroyInput("P", " ", "实验室A", at, "原因")},
		{"空白地点", destroyInput("P", "张三", "", at, "原因")},
		{"空白原因", destroyInput("P", "张三", "实验室A", at, "  ")},
		{"零时间", destroyInput("P", "张三", "实验室A", time.Time{}, "原因")},
		{"早于登记时间", destroyInput("P", "张三", "实验室A", clock.Add(-time.Second), "原因")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s.Destroy(c.in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}

	// 销毁时间与已有保管历史时间相同可以接受。
	if _, err := s.Destroy(destroyInput("P", "张三", "实验室A", clock, "时间相同")); err != nil {
		t.Fatalf("相同时间应允许, got %v", err)
	}
}

func TestDestroyNotFound(t *testing.T) {
	s, clock := fixedStore(t)
	if _, err := s.Destroy(destroyInput("NOPE", "张三", "实验室A", clock.Add(time.Hour), "原因")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在样品应 ErrNotFound, got %v", err)
	}
}

func TestDestroyConflicts(t *testing.T) {
	s, clock := fixedStore(t)
	mustRegister(t, s, "P", "2.000", "张三", "实验室A")
	at := clock.Add(time.Hour)

	// 操作人/地点与当前持有人/地点不一致 → ErrConflict。
	if _, err := s.Destroy(destroyInput("P", "李四", "实验室A", at, "原因")); !errors.Is(err, ErrConflict) {
		t.Fatalf("操作人不一致应 ErrConflict, got %v", err)
	}
	if _, err := s.Destroy(destroyInput("P", "张三", "实验室B", at, "原因")); !errors.Is(err, ErrConflict) {
		t.Fatalf("地点不一致应 ErrConflict, got %v", err)
	}

	// 有待确认交接 → ErrConflict。
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B", HandedOverAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(destroyInput("P", "张三", "实验室A", at.Add(time.Hour), "原因")); !errors.Is(err, ErrConflict) {
		t.Fatalf("有待确认交接时销毁应 ErrConflict, got %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-1", "李四", "实验室B", at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// 交接确认后持有人已变为李四，张三不能销毁。
	if _, err := s.Destroy(destroyInput("P", "张三", "实验室A", at.Add(2*time.Hour), "原因")); !errors.Is(err, ErrConflict) {
		t.Fatalf("交接后原持有人销毁应 ErrConflict, got %v", err)
	}
	if _, err := s.Destroy(destroyInput("P", "李四", "实验室B", at.Add(2*time.Hour), "原因")); err != nil {
		t.Fatalf("交接后新持有人销毁应成功: %v", err)
	}

	// 剩余量为零但未销毁（分装用尽）→ ErrConflict。
	mustRegister(t, s, "Q", "1.000", "h", "l")
	if _, err := s.Split(SplitInput{ParentID: "Q", Parts: []SplitPart{{ID: "Q-C", Qty: "1.000"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(destroyInput("Q", "h", "l", clock.Add(3*time.Hour), "原因")); !errors.Is(err, ErrConflict) {
		t.Fatalf("剩余量为零应 ErrConflict, got %v", err)
	}

	// 被拒绝的销毁不改变任何状态。
	q, _ := s.GetSample("Q")
	if q.Remaining != "0.000" || q.Destruction != nil {
		t.Fatalf("被拒绝的销毁改动了状态: %+v", q)
	}
}

func TestDestroyIdempotency(t *testing.T) {
	s, clock := fixedStore(t)
	mustRegister(t, s, "P", "3.000", "张三", "实验室A")
	at := clock.Add(time.Hour)

	first, err := s.Destroy(destroyInput("P", " 张三 ", " 实验室A ", at, " 原因 "))
	if err != nil {
		t.Fatal(err)
	}
	if first.Destruction == nil || first.Destruction.Operator != "张三" ||
		first.Destruction.Location != "实验室A" || first.Destruction.Reason != "原因" {
		t.Fatalf("入参去空白后应按内容保存: %+v", first.Destruction)
	}

	// 完全相同的人员、地点、时间、原因（时间用不同时区表示同一时刻）→ 原结果。
	same := destroyInput("P", "张三", "实验室A", at.In(time.FixedZone("X", 3600)), "原因")
	again, err := s.Destroy(same)
	if err != nil {
		t.Fatalf("重复提交应幂等返回: %v", err)
	}
	if again.Destruction == nil || !again.Destruction.DestroyedAt.Equal(at) ||
		again.Destruction.Qty != first.Destruction.Qty {
		t.Fatalf("重复提交应返回原销毁结果: %+v", again.Destruction)
	}
	if len(again.History) != len(first.History) {
		t.Fatalf("重复提交不得追加历史: before=%d after=%d", len(first.History), len(again.History))
	}

	// 改变任一项 → ErrConflict，且不覆盖原记录。
	mutated := []DestroyInput{
		destroyInput("P", "李四", "实验室A", at, "原因"),
		destroyInput("P", "张三", "实验室B", at, "原因"),
		destroyInput("P", "张三", "实验室A", at.Add(time.Minute), "原因"),
		destroyInput("P", "张三", "实验室A", at, "不同原因"),
	}
	for i, m := range mutated {
		if _, err := s.Destroy(m); !errors.Is(err, ErrConflict) {
			t.Fatalf("mutated %d: 应 ErrConflict, got %v", i, err)
		}
	}
	cur, _ := s.GetSample("P")
	if cur.Destruction.Operator != "张三" || cur.Destruction.Reason != "原因" ||
		!cur.Destruction.DestroyedAt.Equal(at) || len(cur.History) != len(first.History) {
		t.Fatalf("冲突提交不得覆盖原记录: %+v", cur.Destruction)
	}
}

func TestDestroyedSampleBlockedFromSplitAndHandover(t *testing.T) {
	s, clock := fixedStore(t)
	mustRegister(t, s, "P", "2.000", "张三", "实验室A")
	at := clock.Add(time.Hour)
	if _, err := s.Destroy(destroyInput("P", "张三", "实验室A", at, "原因")); err != nil {
		t.Fatal(err)
	}

	// 已销毁样品不得分装或发起新的交接。
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{{ID: "C", Qty: "0.001"}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("已销毁样品分装应 ErrConflict, got %v", err)
	}
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B", HandedOverAt: at.Add(time.Minute),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("已销毁样品发起交接应 ErrConflict, got %v", err)
	}

	// 先有已确认交接的样品被销毁后，相同交出/接收请求仍按原规则返回原记录，
	// 不改变已销毁状态，也不追加历史。
	mustRegister(t, s, "Q", "2.000", "张三", "实验室A")
	hAt := clock.Add(2 * time.Hour)
	tr, err := s.Handover(HandoverInput{
		TransferID: "TR-Q", SampleID: "Q",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B", HandedOverAt: hAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-Q", "李四", "实验室B", hAt.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	dAt := clock.Add(3 * time.Hour)
	if _, err := s.Destroy(destroyInput("Q", "李四", "实验室B", dAt, "交接后销毁")); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetSample("Q")

	// 重复相同交出请求 → 返回原交接。
	againHandover, err := s.Handover(HandoverInput{
		TransferID: "TR-Q", SampleID: "Q",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B", HandedOverAt: hAt,
	})
	if err != nil {
		t.Fatalf("已确认交接重复相同交出请求应返回原记录: %v", err)
	}
	if againHandover.TransferID != tr.TransferID || !againHandover.Confirmed {
		t.Fatalf("应返回原已确认交接: %+v", againHandover)
	}
	// 重复相同接收信息 → 返回原结果。
	if _, err := s.Confirm(ConfirmInput{"TR-Q", "李四", "实验室B", hAt.Add(time.Hour)}); err != nil {
		t.Fatalf("已确认交接重复相同接收应返回原结果: %v", err)
	}

	after, _ := s.GetSample("Q")
	if after.Destruction == nil || !after.Destruction.DestroyedAt.Equal(dAt) {
		t.Fatalf("重复交接不得改变已销毁状态: %+v", after.Destruction)
	}
	if len(after.History) != len(before.History) {
		t.Fatalf("重复交接不得追加历史: before=%d after=%d", len(before.History), len(after.History))
	}
}

func TestDestroyParentChildIndependent(t *testing.T) {
	s, clock := fixedStore(t)
	mustRegister(t, s, "P", "10.000", "张三", "实验室A")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C1", Qty: "3.000"},
		{ID: "C2", Qty: "3.000"},
	}}); err != nil {
		t.Fatal(err)
	}
	// 父样剩余 4.000，子样各 3.000。
	at := clock.Add(time.Hour)

	// 销毁子样 C1：不应扣减父样，也不影响其他子样。
	if _, err := s.Destroy(destroyInput("C1", "张三", "实验室A", at, "销毁子样")); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetSample("P")
	if p.Remaining != "4.000" || p.Destruction != nil {
		t.Fatalf("销毁子样不应扣减父样: rem=%s dest=%+v", p.Remaining, p.Destruction)
	}
	c2, _ := s.GetSample("C2")
	if c2.Remaining != "3.000" || c2.Destruction != nil || c2.Holder != "张三" {
		t.Fatalf("其他子样不应受影响: %+v", c2)
	}
	c1, _ := s.GetSample("C1")
	if c1.Destruction == nil || c1.Destruction.Qty != "3.000" || c1.Remaining != "0.000" {
		t.Fatalf("子样销毁记录错误: %+v", c1.Destruction)
	}

	// 销毁父样 P：已分出的子样保持不变。
	if _, err := s.Destroy(destroyInput("P", "张三", "实验室A", at.Add(time.Hour), "销毁父样")); err != nil {
		t.Fatal(err)
	}
	p, _ = s.GetSample("P")
	if p.Destruction == nil || p.Destruction.Qty != "4.000" {
		t.Fatalf("父样销毁量应为当时全部剩余量 4.000: %+v", p.Destruction)
	}
	c2, _ = s.GetSample("C2")
	if c2.Destruction != nil || c2.Remaining != "3.000" {
		t.Fatalf("销毁父样不应改变已分出的子样: %+v", c2)
	}
}

func TestDestroyDistinguishesUsedUpAndDestroyed(t *testing.T) {
	s, clock := fixedStore(t)
	mustRegister(t, s, "P", "5.000", "h", "l")
	// 分装用尽：剩余量为零但没有销毁记录。
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{{ID: "C", Qty: "5.000"}}}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetSample("P")
	if p.Remaining != "0.000" || p.Destruction != nil {
		t.Fatalf("分装用尽不应有销毁记录: rem=%s dest=%+v", p.Remaining, p.Destruction)
	}

	// 销毁：剩余量为零且有销毁记录。
	mustRegister(t, s, "D", "1.000", "h", "l")
	at := clock.Add(time.Hour)
	if _, err := s.Destroy(destroyInput("D", "h", "l", at, "销毁")); err != nil {
		t.Fatal(err)
	}
	d, _ := s.GetSample("D")
	if d.Remaining != "0.000" || d.Destruction == nil {
		t.Fatalf("销毁后应有销毁记录: rem=%s dest=%+v", d.Remaining, d.Destruction)
	}
}

func TestDestroyPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub.json")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	mustRegister(t, s, "P", "5.000", "张三", "实验室A")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{{ID: "C", Qty: "2.000"}}}); err != nil {
		t.Fatal(err)
	}
	at := clock.Add(time.Hour)
	if _, err := s.Destroy(destroyInput("P", "张三", "实验室A", at, "销毁原因")); err != nil {
		t.Fatal(err)
	}

	// 重新打开同一文件。
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s2.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if p.Remaining != "0.000" {
		t.Fatalf("重开后剩余量应为 0.000, got %s", p.Remaining)
	}
	if p.Destruction == nil {
		t.Fatalf("重开后销毁信息应保留")
	}
	d := p.Destruction
	if d.Operator != "张三" || d.Location != "实验室A" || d.Reason != "销毁原因" ||
		d.Qty != "3.000" || !d.DestroyedAt.Equal(at) {
		t.Fatalf("重开后销毁记录错误: %+v", d)
	}
	if len(p.Children) != 1 || p.Children[0] != "C" {
		t.Fatalf("重开后子样关系应保留: %+v", p.Children)
	}
	if len(p.History) != 3 || p.History[2].Kind != "destroy" {
		t.Fatalf("重开后销毁历史应保留: %+v", p.History)
	}

	// 重开后重复提交判断继续有效。
	if _, err := s2.Destroy(destroyInput("P", "张三", "实验室A", at, "销毁原因")); err != nil {
		t.Fatalf("重开后相同销毁请求应幂等返回: %v", err)
	}
	if _, err := s2.Destroy(destroyInput("P", "张三", "实验室A", at, "不同原因")); !errors.Is(err, ErrConflict) {
		t.Fatalf("重开后不同销毁请求应 ErrConflict, got %v", err)
	}

	// 重开后后续操作限制继续有效。
	if _, err := s2.Split(SplitInput{ParentID: "P", Parts: []SplitPart{{ID: "X", Qty: "0.001"}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("重开后已销毁样品分装应 ErrConflict, got %v", err)
	}
	if _, err := s2.Handover(HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B", HandedOverAt: at.Add(time.Minute),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("重开后已销毁样品交接应 ErrConflict, got %v", err)
	}
}

// TestDestroyLegacyDataWithoutDestruction 旧数据中没有销毁信息的样品一律
// 视为未销毁：即使剩余量为零也不能推断为销毁；剩余量不为零的旧样品仍可
// 执行销毁。
func TestDestroyLegacyDataWithoutDestruction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.json")
	legacy := `{
  "version": 1,
  "samples": {
    "USED": {
      "id": "USED",
      "initial": 1000,
      "remaining": 0,
      "holder": "h",
      "location": "l",
      "children": [],
      "history": [
        {"kind": "register", "time": "2026-10-02T09:00:00Z", "holder": "h", "location": "l", "detail": "登记原样"},
        {"kind": "split", "time": "2026-10-02T10:00:00Z", "holder": "h", "location": "l", "detail": "分装创建子样 C"}
      ]
    },
    "FULL": {
      "id": "FULL",
      "initial": 2000,
      "remaining": 2000,
      "holder": "h",
      "location": "l",
      "children": [],
      "history": [
        {"kind": "register", "time": "2026-10-02T09:00:00Z", "holder": "h", "location": "l", "detail": "登记原样"}
      ]
    }
  },
  "transfers": {}
}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	used, err := s.GetSample("USED")
	if err != nil {
		t.Fatal(err)
	}
	if used.Remaining != "0.000" || used.Destruction != nil {
		t.Fatalf("旧数据零剩余样品不应推断为已销毁: rem=%s dest=%+v", used.Remaining, used.Destruction)
	}
	// 旧样品剩余量为零：不能分装/交接，也不能销毁（剩余量为零）。
	at := time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC)
	if _, err := s.Destroy(destroyInput("USED", "h", "l", at, "原因")); !errors.Is(err, ErrConflict) {
		t.Fatalf("旧数据零剩余销毁应 ErrConflict, got %v", err)
	}

	// 旧数据中剩余量不为零的样品仍可正常销毁。
	full, err := s.Destroy(destroyInput("FULL", "h", "l", at, "旧数据补销毁"))
	if err != nil {
		t.Fatalf("旧数据样品应可销毁: %v", err)
	}
	if full.Destruction == nil || full.Destruction.Qty != "2.000" || full.Remaining != "0.000" {
		t.Fatalf("旧数据销毁结果错误: %+v", full.Destruction)
	}

	// 销毁后再次重开，销毁信息按新格式保留。
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	full2, err := s2.GetSample("FULL")
	if err != nil {
		t.Fatal(err)
	}
	if full2.Destruction == nil || full2.Destruction.Reason != "旧数据补销毁" {
		t.Fatalf("销毁后重开应保留销毁记录: %+v", full2.Destruction)
	}
}
