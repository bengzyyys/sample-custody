package custody

import (
	"errors"
	"testing"
	"time"
)

// 这组回归测试保护“旧交接的相同请求”在样品已经继续分装、并进入下一次
// 交接之后仍然按首次内容幂等返回：旧记录不能被当前剩余量、当前持有人或
// 新的待确认交接改写，旧请求也不能另建交接、清除待确认或追加历史。
//
// 场景：
//  1. P 初始 10.000 毫升，张三在实验室A交给李四，李四在实验室B完成接收（TR-1）；
//  2. 李四分出 3.250 毫升子样 CHILD，P 剩余 6.750；
//  3. 李四把 P 剩余的 6.750 毫升在第二次交接 TR-2 中交给王五（实验室C）。
//
// TR-2 的待确认与已确认两种状态分别由两个用例覆盖。

// setupSequentialTransfers 搭好上述场景，并让 TR-2 处于待确认状态。
func setupSequentialTransfers(t *testing.T) (s *Store, hAt1, rAt1, hAt2 time.Time) {
	t.Helper()
	s, _ = fixedStore(t)
	mustRegister(t, s, "P", "10.000", "张三", "实验室A")

	hAt1 = time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	rAt1 = hAt1.Add(time.Hour)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt1,
	}); err != nil {
		t.Fatalf("第一次交接发起失败: %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-1", "李四", "实验室B", rAt1}); err != nil {
		t.Fatalf("第一次交接接收失败: %v", err)
	}

	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "CHILD", Qty: "3.250"},
	}}); err != nil {
		t.Fatalf("分装失败: %v", err)
	}

	hAt2 = time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-2", SampleID: "P",
		FromHolder: "李四", FromLocation: "实验室B",
		ToHolder: "王五", ToLocation: "实验室C",
		HandedOverAt: hAt2,
	}); err != nil {
		t.Fatalf("第二次交接发起失败: %v", err)
	}
	return s, hAt1, rAt1, hAt2
}

// requireOldTransferView 校验返回的仍是第一次交接 TR-1 的已确认记录：
// 数量为当时的 10.000，人员、地点、时间均为首次交接时的值。
func requireOldTransferView(t *testing.T, tr *TransferView, hAt1, rAt1 time.Time) {
	t.Helper()
	if tr == nil {
		t.Fatalf("应返回第一次交接 TR-1 的记录")
	}
	if tr.TransferID != "TR-1" || tr.SampleID != "P" {
		t.Fatalf("旧交接编号或样品被改写: %+v", tr)
	}
	if tr.Qty != "10.000" {
		t.Fatalf("旧交接数量必须保持交接发生时的 10.000，不能按当前剩余量显示, got %s", tr.Qty)
	}
	if tr.FromHolder != "张三" || tr.FromLocation != "实验室A" ||
		tr.ToHolder != "李四" || tr.ToLocation != "实验室B" || tr.ConfirmedBy != "李四" {
		t.Fatalf("旧交接的交出/接收人员或地点被当前状态改写: %+v", tr)
	}
	if !tr.Confirmed || tr.ReceivedAt == nil {
		t.Fatalf("旧交接应为已确认状态: %+v", tr)
	}
	if !tr.HandedOverAt.Equal(hAt1) || !tr.ReceivedAt.Equal(rAt1) {
		t.Fatalf("旧交接的交出/接收时间被改写: handover=%s received=%v",
			tr.HandedOverAt.Format(time.RFC3339), tr.ReceivedAt)
	}
}

// requirePendingSecondTransferState 校验 TR-2 待确认期间，原样、子样和
// 历史都保持“旧请求提交前”的状态。
func requirePendingSecondTransferState(t *testing.T, s *Store, hAt1, rAt1, hAt2 time.Time) {
	t.Helper()
	got, err := s.GetSample("P")
	if err != nil {
		t.Fatalf("查询原样: %v", err)
	}
	if got.InitialQty != "10.000" || got.Remaining != "6.750" {
		t.Fatalf("原样数量被改写: init=%s remaining=%s", got.InitialQty, got.Remaining)
	}
	// 待确认期间原持有人和地点不变，且不能因为旧交出人已是张三而受到影响。
	if got.Holder != "李四" || got.Location != "实验室B" {
		t.Fatalf("TR-2 待确认期间原样仍应由李四在实验室B持有: %+v", got)
	}
	pt := got.PendingTransfer
	if pt == nil || pt.TransferID != "TR-2" {
		t.Fatalf("待确认详情必须仍指向第二次交接 TR-2，旧接收请求不能清除它: %+v", pt)
	}
	if pt.Confirmed || pt.Qty != "6.750" || pt.FromHolder != "李四" || pt.FromLocation != "实验室B" ||
		pt.ToHolder != "王五" || pt.ToLocation != "实验室C" || !pt.HandedOverAt.Equal(hAt2) {
		t.Fatalf("TR-2 待确认详情被改写: %+v", pt)
	}
	if len(got.Children) != 1 || got.Children[0] != "CHILD" {
		t.Fatalf("原样的子样关系被改写: %v", got.Children)
	}
	wantKinds := []string{"register", "transfer-out", "transfer-in", "split", "transfer-out"}
	if len(got.History) != len(wantKinds) {
		t.Fatalf("保管历史条数被改写: got %d 条 %+v", len(got.History), got.History)
	}
	for i, k := range wantKinds {
		if got.History[i].Kind != k {
			t.Fatalf("保管历史顺序或内容被改写: %+v", got.History)
		}
	}
	// 旧事件仍记录第一次交接时的人员、地点和时间。
	if got.History[1].Holder != "张三" || got.History[1].Location != "实验室A" ||
		!got.History[1].Time.Equal(hAt1) ||
		got.History[2].Holder != "李四" || got.History[2].Location != "实验室B" ||
		!got.History[2].Time.Equal(rAt1) {
		t.Fatalf("第一次交接的旧历史事件被改写: %+v", got.History[1:3])
	}

	// 分出的子样独立持有，不受原样任何交接和旧请求影响。
	child, err := s.GetSample("CHILD")
	if err != nil {
		t.Fatalf("查询子样: %v", err)
	}
	if child.InitialQty != "3.250" || child.Remaining != "3.250" ||
		child.Holder != "李四" || child.Location != "实验室B" {
		t.Fatalf("子样应仍由李四在实验室B独立持有 3.250 毫升: %+v", child)
	}

	// 旧交出请求不能另建一条交接：全份数据始终只有 TR-1、TR-2 两条。
	if len(s.data.Transfers) != 2 {
		t.Fatalf("重复提交旧交出不得新增交接记录, got %d 条", len(s.data.Transfers))
	}
}

func TestOldTransferResubmissionDuringNextPendingHandover(t *testing.T) {
	s, hAt1, rAt1, hAt2 := setupSequentialTransfers(t)

	oldHandover := HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt1,
	}
	oldConfirm := ConfirmInput{"TR-1", "李四", "实验室B", rAt1}

	// TR-2 待确认期间，重新提交第一次交接的相同交出信息：张三已不是当前
	// 持有人，但原请求仍应成功返回 TR-1 的已确认记录。
	tr, err := s.Handover(oldHandover)
	if err != nil {
		t.Fatalf("重复提交旧交出请求应返回原交接，不能因当前持有人变化而拒绝: %v", err)
	}
	requireOldTransferView(t, tr, hAt1, rAt1)

	// 重新提交第一次交接的相同接收信息，同样返回原确认结果。
	tr, err = s.Confirm(oldConfirm)
	if err != nil {
		t.Fatalf("重复提交旧接收请求应返回原结果: %v", err)
	}
	requireOldTransferView(t, tr, hAt1, rAt1)

	// 两次旧请求返回后：原样仍由李四在实验室B持有，待确认仍是 TR-2，
	// 剩余量、子样、历史和交接数量都不变化。
	requirePendingSecondTransferState(t, s, hAt1, rAt1, hAt2)

	// 比较规则保持不变：文本字段忽略首尾空白；换一个时区表示同一时刻
	// （10:00/11:00 UTC == 18:00/19:00 UTC+8）仍属于重复提交。
	utc8 := time.FixedZone("UTC+8", 8*60*60)
	hAt1Alt := time.Date(2026, 10, 2, 18, 0, 0, 0, utc8)
	rAt1Alt := time.Date(2026, 10, 2, 19, 0, 0, 0, utc8)
	paddedHandover := oldHandover
	paddedHandover.TransferID = "  TR-1  "
	paddedHandover.SampleID = "  P  "
	paddedHandover.FromHolder = "   张三   "
	paddedHandover.FromLocation = " 实验室A "
	paddedHandover.ToHolder = " 李四 "
	paddedHandover.ToLocation = "  实验室B  "
	paddedHandover.HandedOverAt = hAt1Alt
	tr, err = s.Handover(paddedHandover)
	if err != nil {
		t.Fatalf("带首尾空白、换时区表示同一时刻的旧交出请求应视为重复提交: %v", err)
	}
	requireOldTransferView(t, tr, hAt1, rAt1)
	tr, err = s.Confirm(ConfirmInput{" TR-1 ", "  李四  ", " 实验室B ", rAt1Alt})
	if err != nil {
		t.Fatalf("带首尾空白、换时区表示同一时刻的旧接收请求应视为重复提交: %v", err)
	}
	requireOldTransferView(t, tr, hAt1, rAt1)
	requirePendingSecondTransferState(t, s, hAt1, rAt1, hAt2)

	// 沿用旧编号但改变目的地点：ErrConflict，旧交接与当前样品状态都保留。
	mutatedHandover := oldHandover
	mutatedHandover.ToLocation = "实验室X"
	if _, err := s.Handover(mutatedHandover); !errors.Is(err, ErrConflict) {
		t.Fatalf("沿用旧编号改变目的地点应返回 ErrConflict, got %v", err)
	}
	// 把旧接收时间改成另一个合法时刻：ErrConflict，且不能清除 TR-2 待确认。
	if _, err := s.Confirm(ConfirmInput{"TR-1", "李四", "实验室B", rAt1.Add(time.Minute)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("旧交接以不同接收时间重复确认应返回 ErrConflict, got %v", err)
	}
	requirePendingSecondTransferState(t, s, hAt1, rAt1, hAt2)

	// 王五按新交接约定确认接收：正常完成，只追加这一次接收应有的历史。
	done, err := s.Confirm(ConfirmInput{"TR-2", "王五", "实验室C", hAt2.Add(time.Hour)})
	if err != nil {
		t.Fatalf("TR-2 应能正常确认接收: %v", err)
	}
	if !done.Confirmed || done.Qty != "6.750" || done.ToHolder != "王五" || done.ToLocation != "实验室C" {
		t.Fatalf("TR-2 确认结果错误: %+v", done)
	}
	got, _ := s.GetSample("P")
	if got.Holder != "王五" || got.Location != "实验室C" || got.PendingTransfer != nil {
		t.Fatalf("TR-2 确认后应改为王五在实验室C持有并结束待确认: %+v", got)
	}
	if got.Remaining != "6.750" {
		t.Fatalf("TR-2 确认后原样剩余量应为 6.750, got %s", got.Remaining)
	}
	wantKinds := []string{"register", "transfer-out", "transfer-in", "split", "transfer-out", "transfer-in"}
	if len(got.History) != len(wantKinds) {
		t.Fatalf("TR-2 确认只应追加一次接收历史, got %d 条 %+v", len(got.History), got.History)
	}
	for i, k := range wantKinds {
		if got.History[i].Kind != k {
			t.Fatalf("TR-2 确认后历史顺序错误: %+v", got.History)
		}
	}
	last := got.History[len(got.History)-1]
	if last.Holder != "王五" || last.Location != "实验室C" || !last.Time.Equal(hAt2.Add(time.Hour)) {
		t.Fatalf("新增历史应只记录 TR-2 的接收: %+v", last)
	}
}

func TestOldTransferResubmissionAfterNextHandoverCompleted(t *testing.T) {
	s, hAt1, rAt1, hAt2 := setupSequentialTransfers(t)
	rAt2 := hAt2.Add(time.Hour)
	if _, err := s.Confirm(ConfirmInput{"TR-2", "王五", "实验室C", rAt2}); err != nil {
		t.Fatalf("TR-2 接收失败: %v", err)
	}

	oldHandover := HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt1,
	}
	oldConfirm := ConfirmInput{"TR-1", "李四", "实验室B", rAt1}

	before, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	beforeHistory := append([]History(nil), before.History...)

	// 新交接完成后再提交第一次交接的相同交出、接收请求，仍返回旧记录。
	tr, err := s.Handover(oldHandover)
	if err != nil {
		t.Fatalf("新交接完成后重复旧交出请求应返回原交接: %v", err)
	}
	requireOldTransferView(t, tr, hAt1, rAt1)
	tr, err = s.Confirm(oldConfirm)
	if err != nil {
		t.Fatalf("新交接完成后重复旧接收请求应返回原结果: %v", err)
	}
	requireOldTransferView(t, tr, hAt1, rAt1)

	after, _ := s.GetSample("P")
	// 原样继续由王五在实验室C持有，不回到李四处，也不重新进入待确认。
	if after.Holder != "王五" || after.Location != "实验室C" || after.PendingTransfer != nil {
		t.Fatalf("旧请求返回后当前持有状态不应变化: %+v", after)
	}
	if after.InitialQty != "10.000" || after.Remaining != "6.750" {
		t.Fatalf("原样数量不应变化: init=%s remaining=%s", after.InitialQty, after.Remaining)
	}
	// 不追加、也不改写历史中的任何旧事件。
	requireSameHistory(t, beforeHistory, after.History)
	if len(s.data.Transfers) != 2 {
		t.Fatalf("重复提交旧交接不得新增交接记录, got %d 条", len(s.data.Transfers))
	}
	child, _ := s.GetSample("CHILD")
	if child.Holder != "李四" || child.Location != "实验室B" || child.Remaining != "3.250" {
		t.Fatalf("子样持有状态不应受原样交接影响: %+v", child)
	}

	// 新交接完成后，旧请求的冲突变体同样返回 ErrConflict 且状态保留。
	mutatedHandover := oldHandover
	mutatedHandover.ToLocation = "实验室X"
	if _, err := s.Handover(mutatedHandover); !errors.Is(err, ErrConflict) {
		t.Fatalf("沿用旧编号改变目的地点应返回 ErrConflict, got %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-1", "李四", "实验室B", rAt1.Add(2 * time.Minute)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("旧交接以另一合法接收时间重复确认应返回 ErrConflict, got %v", err)
	}

	final, _ := s.GetSample("P")
	if final.Holder != "王五" || final.Location != "实验室C" || final.PendingTransfer != nil {
		t.Fatalf("冲突请求不得改动当前持有状态: %+v", final)
	}
	requireSameHistory(t, beforeHistory, final.History)
}

// requireSameHistory 逐字段比较两段保管历史，确保旧事件不被追加或改写。
func requireSameHistory(t *testing.T, want, got []History) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("保管历史条数发生变化: want %d, got %d (%+v)", len(want), len(got), got)
	}
	for i := range want {
		a, b := want[i], got[i]
		if a.Kind != b.Kind || a.Holder != b.Holder || a.Location != b.Location ||
			a.Detail != b.Detail || !a.Time.Equal(b.Time) {
			t.Fatalf("第 %d 条保管历史被改写:\nwant=%+v\ngot =%+v", i, a, b)
		}
	}
}
