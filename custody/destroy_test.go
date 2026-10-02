package custody

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDestroySuccess 成功销毁全部剩余量：数量、视图、历史与保留字段。
func TestDestroySuccess(t *testing.T) {
	s, now := fixedStore(t)
	mustRegister(t, s, "P", "4.250", "张三", "实验室A")

	at := now.Add(2 * time.Hour)
	got, err := s.Destroy(DestroyInput{
		SampleID: " P ", Operator: " 张三 ", Location: " 实验室A ",
		At: at, Reason: " 实验结束按规程销毁 ",
	})
	if err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if got.Remaining != "0.000" {
		t.Fatalf("销毁后剩余量应为 0.000, got %s", got.Remaining)
	}
	if got.InitialQty != "4.250" {
		t.Fatalf("初始量应保留, got %s", got.InitialQty)
	}
	// 持有人和地点保留为销毁前的最后记录。
	if got.Holder != "张三" || got.Location != "实验室A" {
		t.Fatalf("销毁不应改变持有人地点: %+v", got)
	}
	if got.Destruction == nil {
		t.Fatalf("已销毁样品查询应带销毁记录")
	}
	d := got.Destruction
	if d.Operator != "张三" || d.Location != "实验室A" ||
		d.Reason != "实验结束按规程销毁" || d.Qty != "4.250" || !d.At.Equal(at) {
		t.Fatalf("销毁记录内容错误: %+v", d)
	}
	if got.PendingTransfer != nil {
		t.Fatalf("销毁样品不应有待确认交接")
	}

	// 历史末尾追加一次销毁事件，原有历史保留。
	if len(got.History) != 2 {
		t.Fatalf("应在原历史后追加一次销毁事件, got %d 条", len(got.History))
	}
	last := got.History[len(got.History)-1]
	if last.Kind != "destroy" || !last.Time.Equal(at) ||
		last.Holder != "张三" || last.Location != "实验室A" {
		t.Fatalf("销毁历史事件错误: %+v", last)
	}
	if got.History[0].Kind != "register" {
		t.Fatalf("原有保管历史应保留: %+v", got.History)
	}

	// 重新查询结果一致。
	again, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if again.Destruction == nil || again.Destruction.Qty != "4.250" || again.Remaining != "0.000" {
		t.Fatalf("重新查询销毁信息错误: %+v", again)
	}
}

// TestDestroyDistinguishesFromSplitExhaustion 剩余量为零时必须能区分
// “分装用尽”和“已销毁”。
func TestDestroyDistinguishesFromSplitExhaustion(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "1.000", "h", "l")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{{ID: "C", Qty: "1.000"}}}); err != nil {
		t.Fatal(err)
	}
	exhausted, _ := s.GetSample("P")
	if exhausted.Remaining != "0.000" {
		t.Fatalf("前置条件：剩余量应为 0.000")
	}
	if exhausted.Destruction != nil {
		t.Fatalf("分装用尽的样品不应有销毁记录: %+v", exhausted.Destruction)
	}

	// 子样有剩余量，销毁后带销毁记录。
	destroyed, err := s.Destroy(DestroyInput{
		SampleID: "C", Operator: "h", Location: "l",
		At: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC), Reason: "过期",
	})
	if err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if destroyed.Remaining != "0.000" || destroyed.Destruction == nil {
		t.Fatalf("销毁后应能通过销毁记录区分: %+v", destroyed)
	}

	// 未销毁样品没有销毁记录。
	fresh := mustRegister(t, s, "Q", "3.000", "h", "l")
	if fresh.Destruction != nil {
		t.Fatalf("未销毁样品不应有销毁记录")
	}
}

func TestDestroyValidation(t *testing.T) {
	s, now := fixedStore(t)
	mustRegister(t, s, "P", "1.000", "张三", "实验室A")

	base := func() DestroyInput {
		return DestroyInput{
			SampleID: "P", Operator: "张三", Location: "实验室A",
			At: now.Add(time.Hour), Reason: "废弃",
		}
	}
	cases := []struct {
		name string
		mut  func(DestroyInput) DestroyInput
		want error
	}{
		{"空白编号", func(i DestroyInput) DestroyInput { i.SampleID = "  "; return i }, ErrInvalid},
		{"空白操作人", func(i DestroyInput) DestroyInput { i.Operator = " "; return i }, ErrInvalid},
		{"空白地点", func(i DestroyInput) DestroyInput { i.Location = "\t"; return i }, ErrInvalid},
		{"空白原因", func(i DestroyInput) DestroyInput { i.Reason = ""; return i }, ErrInvalid},
		{"零时间", func(i DestroyInput) DestroyInput { i.At = time.Time{}; return i }, ErrInvalid},
		{"早于登记历史", func(i DestroyInput) DestroyInput { i.At = now.Add(-time.Second); return i }, ErrInvalid},
		{"持有人不匹配", func(i DestroyInput) DestroyInput { i.Operator = "李四"; return i }, ErrConflict},
		{"地点不匹配", func(i DestroyInput) DestroyInput { i.Location = "实验室B"; return i }, ErrConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s.Destroy(c.mut(base())); !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}

	// 销毁时间恰好等于已有历史时间可以接受（相同时间允许）。
	if _, err := s.Destroy(func() DestroyInput {
		i := base()
		i.At = now // 与登记历史同一时刻
		return i
	}()); err != nil {
		t.Fatalf("销毁时间与历史时间相同应允许, got %v", err)
	}
}

// TestDestroyNotFound 样品不存在返回 ErrNotFound，且不创建记录。
func TestDestroyNotFound(t *testing.T) {
	s, now := fixedStore(t)
	if _, err := s.Destroy(DestroyInput{
		SampleID: "ghost", Operator: "h", Location: "l", At: now, Reason: "r",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := s.GetSample("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败的销毁不应创建样品")
	}
}

// TestDestroyPendingConflict 有待确认交接时不能销毁。
func TestDestroyPendingConflict(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "1.000", "h", "l")
	at := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "h", FromLocation: "l",
		ToHolder: "x", ToLocation: "y", HandedOverAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "h", Location: "l",
		At: at.Add(time.Hour), Reason: "r",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("有待确认交接时销毁应返回 ErrConflict, got %v", err)
	}
	got, _ := s.GetSample("P")
	if got.Destruction != nil || got.PendingTransfer == nil || got.Remaining != "1.000" {
		t.Fatalf("被拒绝的销毁改动了状态: %+v", got)
	}
}

// TestDestroyZeroRemainingConflict 未销毁但剩余量为零返回 ErrConflict。
func TestDestroyZeroRemainingConflict(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "1.000", "h", "l")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{{ID: "C", Qty: "1.000"}}}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetSample("P")
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	if _, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "h", Location: "l", At: at, Reason: "r",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("分装用尽未销毁样品再销毁应 ErrConflict, got %v", err)
	}
	after, _ := s.GetSample("P")
	if after.Destruction != nil || len(after.History) != len(before.History) ||
		after.Remaining != before.Remaining {
		t.Fatalf("被拒绝的销毁改动了状态: before=%+v after=%+v", before, after)
	}
}

// TestDestroyIdempotent 已销毁样品重复提交相同信息返回原结果，不追加历史；
// 改变任一项返回 ErrConflict，不覆盖原记录。
func TestDestroyIdempotent(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "2.500", "张三", "实验室A")
	at := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)

	in := DestroyInput{
		SampleID: "P", Operator: "张三", Location: "实验室A",
		At: at, Reason: "污染",
	}
	first, err := s.Destroy(in)
	if err != nil {
		t.Fatal(err)
	}

	// 文本带首尾空白、时间用不同时区表示同一时刻，仍视为完全相同。
	same := in
	same.Operator = " 张三 "
	same.Location = "实验室A "
	same.Reason = " 污染 "
	same.At = at.In(time.FixedZone("UTC+8", 8*3600))
	again, err := s.Destroy(same)
	if err != nil {
		t.Fatalf("完全相同的重复提交应返回原结果, got %v", err)
	}
	if again.Remaining != first.Remaining || again.Destruction == nil ||
		again.Destruction.Qty != "2.500" {
		t.Fatalf("重复提交返回内容错误: %+v", again)
	}
	if len(again.History) != len(first.History) {
		t.Fatalf("重复提交不得追加历史: first=%d again=%d",
			len(first.History), len(again.History))
	}

	// 改变任一项都返回 ErrConflict，且不覆盖原记录。
	mutCases := map[string]func(DestroyInput) DestroyInput{
		"操作人不同": func(i DestroyInput) DestroyInput { i.Operator = "李四"; return i },
		"地点不同":  func(i DestroyInput) DestroyInput { i.Location = "实验室B"; return i },
		"时间不同":  func(i DestroyInput) DestroyInput { i.At = at.Add(time.Second); return i },
		"原因不同":  func(i DestroyInput) DestroyInput { i.Reason = "过期"; return i },
	}
	for name, mut := range mutCases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Destroy(mut(in)); !errors.Is(err, ErrConflict) {
				t.Fatalf("want ErrConflict, got %v", err)
			}
		})
	}
	got, _ := s.GetSample("P")
	if got.Destruction.Operator != "张三" || got.Destruction.Location != "实验室A" ||
		got.Destruction.Reason != "污染" || !got.Destruction.At.Equal(at) {
		t.Fatalf("原销毁记录被覆盖: %+v", got.Destruction)
	}
	if len(got.History) != len(first.History) {
		t.Fatalf("被拒绝的重复提交不得追加历史")
	}
}

// TestDestroyedSampleCannotFlow 已销毁样品不得再分装或发起新交接；
// 既有已确认交接的相同交出/接收请求仍按原规则返回原记录。
func TestDestroyedSampleCannotFlow(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "2.000", "h", "l")
	hAt := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	rAt := hAt.Add(time.Hour)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "h", FromLocation: "l",
		ToHolder: "x", ToLocation: "y", HandedOverAt: hAt,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-1", "x", "y", rAt}); err != nil {
		t.Fatal(err)
	}
	dAt := rAt.Add(time.Hour)
	destroyed, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "x", Location: "y", At: dAt, Reason: "r",
	})
	if err != nil {
		t.Fatalf("destroy: %v", err)
	}

	// 不能再分装或发起新的交接。
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{{ID: "C", Qty: "0.001"}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("已销毁样品分装应 ErrConflict, got %v", err)
	}
	newHandover := HandoverInput{
		TransferID: "TR-2", SampleID: "P",
		FromHolder: "x", FromLocation: "y",
		ToHolder: "z", ToLocation: "w", HandedOverAt: dAt.Add(time.Hour),
	}
	if _, err := s.Handover(newHandover); !errors.Is(err, ErrConflict) {
		t.Fatalf("已销毁样品发起新交接应 ErrConflict, got %v", err)
	}

	// 既有已确认交接的相同交出请求仍返回原记录，不改变销毁状态、不追加历史。
	oldHandover := HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "h", FromLocation: "l",
		ToHolder: "x", ToLocation: "y", HandedOverAt: hAt,
	}
	tr, err := s.Handover(oldHandover)
	if err != nil {
		t.Fatalf("既有交接相同交出请求应幂等返回原记录, got %v", err)
	}
	if !tr.Confirmed || tr.TransferID != "TR-1" {
		t.Fatalf("返回的不是原交接记录: %+v", tr)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-1", "x", "y", rAt}); err != nil {
		t.Fatalf("既有交接相同接收请求应幂等返回原结果, got %v", err)
	}
	got, _ := s.GetSample("P")
	if got.Destruction == nil || got.Remaining != "0.000" {
		t.Fatalf("幂等重放后销毁状态应不变: %+v", got)
	}
	if len(got.History) != len(destroyed.History) {
		t.Fatalf("幂等重放不得追加历史: before=%d after=%d",
			len(destroyed.History), len(got.History))
	}
}

// TestDestroyParentChildIndependent 父样与子样的销毁各自独立。
func TestDestroyParentChildIndependent(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "10.000", "张三", "A")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C", Qty: "4.000"},
	}}); err != nil {
		t.Fatal(err)
	}
	// 父样剩余 6.000，销毁父样不动子样。
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	p, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "张三", Location: "A", At: at, Reason: "r",
	})
	if err != nil {
		t.Fatalf("destroy parent: %v", err)
	}
	if p.Remaining != "0.000" || p.Destruction == nil || p.Destruction.Qty != "6.000" {
		t.Fatalf("父样销毁视图错误: %+v", p)
	}
	if len(p.Children) != 1 || p.Children[0] != "C" {
		t.Fatalf("销毁应保留子样列表: %+v", p.Children)
	}
	c, _ := s.GetSample("C")
	if c.Destruction != nil || c.Remaining != "4.000" || c.ParentID != "P" {
		t.Fatalf("销毁父样不能改变已分出的子样: %+v", c)
	}

	// 子样仍可独立流转并销毁；销毁子样不再扣减父样。
	c2, err := s.Handover(HandoverInput{
		TransferID: "TC", SampleID: "C",
		FromHolder: "张三", FromLocation: "A",
		ToHolder: "李四", ToLocation: "B", HandedOverAt: at.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("未销毁子样应能继续交接: %v", err)
	}
	if c2.Confirmed {
		t.Fatalf("子样交接应处于待确认")
	}
	if _, err := s.Confirm(ConfirmInput{"TC", "李四", "B", at.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	cd, err := s.Destroy(DestroyInput{
		SampleID: "C", Operator: "李四", Location: "B",
		At: at.Add(3 * time.Hour), Reason: "用尽销毁",
	})
	if err != nil {
		t.Fatalf("destroy child: %v", err)
	}
	if cd.Destruction.Qty != "4.000" || cd.Remaining != "0.000" {
		t.Fatalf("子样销毁视图错误: %+v", cd)
	}
	p2, _ := s.GetSample("P")
	if p2.Remaining != "0.000" || p2.Destruction == nil || p2.Destruction.Qty != "6.000" {
		t.Fatalf("销毁子样不得再扣减父样: %+v", p2)
	}
}

// TestDestroyPersistsAcrossReopen 重开后销毁信息、剩余量、操作限制与
// 幂等判断继续有效。
func TestDestroyPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "destroy.json")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	mustRegister(t, s, "P", "3.125", "张三", "A")
	at := clock.Add(time.Hour)
	first, err := s.Destroy(DestroyInput{
		SampleID: "P", Operator: "张三", Location: "A", At: at, Reason: "污染",
	})
	if err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if got.Remaining != "0.000" || got.InitialQty != "3.125" {
		t.Fatalf("重开后数量错误: %+v", got)
	}
	if got.Destruction == nil || got.Destruction.Operator != "张三" ||
		got.Destruction.Location != "A" || got.Destruction.Reason != "污染" ||
		got.Destruction.Qty != "3.125" || !got.Destruction.At.Equal(at) {
		t.Fatalf("重开后销毁信息应保留: %+v", got.Destruction)
	}
	if len(got.History) != len(first.History) ||
		got.History[len(got.History)-1].Kind != "destroy" {
		t.Fatalf("重开后销毁历史应保留: %+v", got.History)
	}

	// 重开后重复提交相同信息仍幂等。
	again, err := s2.Destroy(DestroyInput{
		SampleID: "P", Operator: " 张三 ", Location: "A", At: at, Reason: "污染",
	})
	if err != nil {
		t.Fatalf("重开后相同销毁请求应幂等返回: %v", err)
	}
	if len(again.History) != len(got.History) {
		t.Fatalf("幂等重复不得追加历史")
	}
	// 改任一项仍拒绝；分装与新交接仍被限制。
	if _, err := s2.Destroy(DestroyInput{
		SampleID: "P", Operator: "张三", Location: "A", At: at.Add(time.Minute), Reason: "污染",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("重开后销毁幂等判断应继续生效, got %v", err)
	}
	if _, err := s2.Split(SplitInput{ParentID: "P", Parts: []SplitPart{{ID: "C", Qty: "0.001"}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("重开后已销毁样品仍不得分装, got %v", err)
	}
	if _, err := s2.Handover(HandoverInput{
		TransferID: "TR-X", SampleID: "P",
		FromHolder: "张三", FromLocation: "A",
		ToHolder: "李四", ToLocation: "B", HandedOverAt: at.Add(time.Hour),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("重开后已销毁样品仍不得发起新交接, got %v", err)
	}
}

// TestDestroyLegacyDataWithoutDestruction 旧数据中没有销毁信息的样品一律
// 视为未销毁，即使剩余量为零也不能推断为销毁。
func TestDestroyLegacyDataWithoutDestruction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.json")
	regAt := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	legacy := ledger{
		Version: 1,
		Samples: map[string]*sampleRecord{
			"OLD": {
				ID:        "OLD",
				Initial:   1000,
				Remaining: 0, // 旧数据：分装用尽但没有销毁信息
				Holder:    "h",
				Location:  "l",
				Children:  []string{},
				History: []historyRecord{{
					Kind: "register", Time: regAt, Holder: "h", Location: "l",
				}},
				// 没有 Destroyed 字段
			},
		},
		Transfers: map[string]*transferRecord{},
	}
	raw, err := json.MarshalIndent(&legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSample("OLD")
	if err != nil {
		t.Fatal(err)
	}
	if got.Remaining != "0.000" {
		t.Fatalf("前置条件：剩余量应为 0.000, got %s", got.Remaining)
	}
	if got.Destruction != nil {
		t.Fatalf("旧数据无销毁信息时不得推断为已销毁: %+v", got.Destruction)
	}
	// 视为未销毁且剩余量为零：销毁请求按冲突处理，而不是幂等返回。
	if _, err := s.Destroy(DestroyInput{
		SampleID: "OLD", Operator: "h", Location: "l",
		At: regAt.Add(time.Hour), Reason: "r",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("旧零剩余量样品销毁应 ErrConflict, got %v", err)
	}
}

// TestDestroyRejectedKeepsState 各类拒绝都不得改变数量、状态、历史。
func TestDestroyRejectedKeepsState(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "1.500", "张三", "实验室A")
	before, _ := s.GetSample("P")
	at := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)

	try := func(in DestroyInput) {
		t.Helper()
		if _, err := s.Destroy(in); err == nil {
			t.Fatalf("应拒绝: %+v", in)
		}
	}
	try(DestroyInput{SampleID: "P", Operator: "李四", Location: "实验室A", At: at, Reason: "r"})
	try(DestroyInput{SampleID: "P", Operator: "张三", Location: "X", At: at, Reason: "r"})
	try(DestroyInput{SampleID: "P", Operator: "张三", Location: "实验室A", At: time.Time{}, Reason: "r"})
	try(DestroyInput{SampleID: "P", Operator: "张三", Location: "实验室A", At: at, Reason: " "})
	try(DestroyInput{SampleID: " ", Operator: "张三", Location: "实验室A", At: at, Reason: "r"})

	after, _ := s.GetSample("P")
	if after.Remaining != before.Remaining || after.Holder != before.Holder ||
		after.Location != before.Location || after.Destruction != nil ||
		len(after.History) != len(before.History) {
		t.Fatalf("被拒绝的销毁改动了状态: before=%+v after=%+v", before, after)
	}
}
