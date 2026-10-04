package custody

import (
	"errors"
	"testing"
	"time"
)

// 构造跨交接回归的标准前置：
//  1. 原样 S-001（10.000 毫升）由张三在实验室A交给李四，李四在实验室B完成
//     接收（第一次交接 TR-001，交接量 10.000）；
//  2. 李四分出 3.250 毫升子样 S-001-A，原样剩余 6.750；
//  3. 李四把原样剩余的 6.750 毫升交给王五、目的实验室C（第二次交接
//     TR-002，处于待确认）。
//
// 返回四个关键时间：第一次交出/接收与第二次交出/接收。
func setupDivergedHandover(t *testing.T) (s *Store, hAt1, rAt1, hAt2, rAt2 time.Time) {
	t.Helper()
	s, _ = fixedStore(t)
	mustRegister(t, s, "S-001", "10.000", "张三", "实验室A")

	hAt1 = time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	rAt1 = hAt1.Add(time.Hour)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-001", SampleID: "S-001",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt1,
	}); err != nil {
		t.Fatalf("第一次交接交出: %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-001", "李四", "实验室B", rAt1}); err != nil {
		t.Fatalf("第一次交接接收: %v", err)
	}

	if _, err := s.Split(SplitInput{ParentID: "S-001", Parts: []SplitPart{
		{ID: "S-001-A", Qty: "3.250"},
	}}); err != nil {
		t.Fatalf("分生子样: %v", err)
	}

	hAt2 = time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	rAt2 = hAt2.Add(time.Hour)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-002", SampleID: "S-001",
		FromHolder: "李四", FromLocation: "实验室B",
		ToHolder: "王五", ToLocation: "实验室C",
		HandedOverAt: hAt2,
	}); err != nil {
		t.Fatalf("第二次交接交出: %v", err)
	}
	return s, hAt1, rAt1, hAt2, rAt2
}

// requireOldFirstTransfer 校验返回的仍是第一次交接的已确认记录：
// 当时的 10.000 毫升、原交出/接收人员、地点与时间，不能按当前剩余量或
// 当前持有人改写。
func requireOldFirstTransfer(t *testing.T, tr *TransferView, hAt1, rAt1 time.Time) {
	t.Helper()
	if tr.TransferID != "TR-001" || tr.SampleID != "S-001" {
		t.Fatalf("旧交接编号/样品被改写: %+v", tr)
	}
	if tr.Qty != "10.000" {
		t.Fatalf("旧交接量必须保持当时的 10.000，不能按当前剩余量改写, got %s", tr.Qty)
	}
	if tr.FromHolder != "张三" || tr.FromLocation != "实验室A" ||
		tr.ToHolder != "李四" || tr.ToLocation != "实验室B" {
		t.Fatalf("旧交接人员或地点被改写: %+v", tr)
	}
	if !tr.HandedOverAt.Equal(hAt1) {
		t.Fatalf("旧交出时间被改写: got %s want %s", tr.HandedOverAt, hAt1)
	}
	if !tr.Confirmed || tr.ConfirmedBy != "李四" || tr.ReceivedAt == nil {
		t.Fatalf("旧交接应为李四确认的已确认记录: %+v", tr)
	}
	if !tr.ReceivedAt.Equal(rAt1) {
		t.Fatalf("旧接收时间被改写: got %s want %s", *tr.ReceivedAt, rAt1)
	}
}

// requireParentMidSecondHandover 校验第二次交接待确认期间原样的当前状态：
// 李四在实验室B持有剩余 6.750，待确认详情指向 TR-002。
func requireParentMidSecondHandover(t *testing.T, p *Sample, hAt2 time.Time) {
	t.Helper()
	if p.InitialQty != "10.000" || p.Remaining != "6.750" {
		t.Fatalf("原样数量错误: init=%s rem=%s", p.InitialQty, p.Remaining)
	}
	if p.Holder != "李四" || p.Location != "实验室B" {
		t.Fatalf("待确认期间原样应由李四在实验室B持有: %+v", p)
	}
	if len(p.Children) != 1 || p.Children[0] != "S-001-A" {
		t.Fatalf("子样关系被改写: %v", p.Children)
	}
	pt := p.PendingTransfer
	if pt == nil {
		t.Fatalf("待确认详情丢失，应仍指向交给王五的 TR-002")
	}
	if pt.TransferID != "TR-002" || pt.Confirmed ||
		pt.FromHolder != "李四" || pt.FromLocation != "实验室B" ||
		pt.ToHolder != "王五" || pt.ToLocation != "实验室C" ||
		pt.Qty != "6.750" || !pt.HandedOverAt.Equal(hAt2) {
		t.Fatalf("待确认交接详情被改写: %+v", pt)
	}
}

// requireChildIndependent 校验分出的子样始终由李四在实验室B独立持有，
// 不受原样任何交接或旧请求重复提交的影响。
func requireChildIndependent(t *testing.T, s *Store) {
	t.Helper()
	c, err := s.GetSample("S-001-A")
	if err != nil {
		t.Fatalf("查询子样: %v", err)
	}
	if c.ParentID != "S-001" || c.InitialQty != "3.250" || c.Remaining != "3.250" ||
		c.Holder != "李四" || c.Location != "实验室B" {
		t.Fatalf("子样应仍由李四在实验室B独立持有 3.250 毫升: %+v", c)
	}
	if c.PendingTransfer != nil {
		t.Fatalf("子样不应被原样交接带动出待确认记录: %+v", c.PendingTransfer)
	}
	if len(c.History) != 1 || c.History[0].Kind != "split" {
		t.Fatalf("子样保管历史不应变化: %+v", c.History)
	}
}

// TestStaleHandoverResubmitDuringNextPending 覆盖第二次交接尚未确认时，
// 重新提交第一次交接的相同交出/接收请求：必须幂等返回第一次交接的已确认
// 记录，当前持有人已不是张三、当前剩余量已不是 10.000 都不能影响结果，
// 也不能清除 TR-002 的待确认记录或另建交接。
func TestStaleHandoverResubmitDuringNextPending(t *testing.T) {
	s, hAt1, rAt1, hAt2, _ := setupDivergedHandover(t)
	tokyo := time.FixedZone("UTC+9", 9*60*60)

	pBefore, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	historyBefore := len(pBefore.History) // register / TR-001 交出 / TR-001 接收 / split / TR-002 交出

	// 重新提交第一次交接的相同交出信息：编号、人员、地点带首尾空白，
	// 时间换一个时区表示同一实际时刻。
	oldHandover := HandoverInput{
		TransferID: " TR-001 ", SampleID: " S-001 ",
		FromHolder: " 张三 ", FromLocation: " 实验室A ",
		ToHolder: " 李四 ", ToLocation: " 实验室B ",
		HandedOverAt: hAt1.In(tokyo),
	}
	tr, err := s.Handover(oldHandover)
	if err != nil {
		t.Fatalf("旧交出请求重复提交应成功返回旧记录: %v", err)
	}
	requireOldFirstTransfer(t, tr, hAt1, rAt1)
	if len(s.data.Transfers) != 2 {
		t.Fatalf("旧交出请求不能另建交接, got %d 条", len(s.data.Transfers))
	}

	p, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	requireParentMidSecondHandover(t, p, hAt2)
	if len(p.History) != historyBefore {
		t.Fatalf("旧交出请求不能追加历史: before=%d after=%d", historyBefore, len(p.History))
	}

	// 重新提交第一次交接的相同接收信息：旧接收请求不能清除新的待确认记录。
	oldConfirm := ConfirmInput{
		TransferID: " TR-001 ",
		Receiver:   " 李四 ",
		AtLocation: " 实验室B ",
		ReceivedAt: rAt1.In(tokyo),
	}
	tr, err = s.Confirm(oldConfirm)
	if err != nil {
		t.Fatalf("旧接收请求重复提交应成功返回旧记录: %v", err)
	}
	requireOldFirstTransfer(t, tr, hAt1, rAt1)

	p, err = s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	requireParentMidSecondHandover(t, p, hAt2)
	if len(p.History) != historyBefore {
		t.Fatalf("旧接收请求不能追加历史: before=%d after=%d", historyBefore, len(p.History))
	}
	tr2, err := s.GetTransfer("TR-002")
	if err != nil {
		t.Fatal(err)
	}
	if tr2.Confirmed || tr2.Qty != "6.750" {
		t.Fatalf("新交接 TR-002 应仍待确认且保持 6.750: %+v", tr2)
	}

	// 子样仍由李四在实验室B独立持有。
	requireChildIndependent(t, s)

	// 旧保管事件本身也不能被改写。
	if p.History[1].Kind != "transfer-out" || p.History[1].Holder != "张三" ||
		p.History[1].Location != "实验室A" || !p.History[1].Time.Equal(hAt1) {
		t.Fatalf("第一次交出的历史事件被改写: %+v", p.History[1])
	}
	if p.History[2].Kind != "transfer-in" || p.History[2].Holder != "李四" ||
		p.History[2].Location != "实验室B" || !p.History[2].Time.Equal(rAt1) {
		t.Fatalf("第一次接收的历史事件被改写: %+v", p.History[2])
	}

	// 沿用旧编号但改变目的地点：ErrConflict，旧交接与当前样品状态都保留。
	badHandover := HandoverInput{
		TransferID: "TR-001", SampleID: "S-001",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室C",
		HandedOverAt: hAt1,
	}
	if _, err := s.Handover(badHandover); !errors.Is(err, ErrConflict) {
		t.Fatalf("沿用旧编号改目的地点应返回 ErrConflict, got %v", err)
	}
	// 把旧接收时间改成另一个合法时刻（不早于旧交出时间）：同样 ErrConflict。
	badConfirm := ConfirmInput{"TR-001", "李四", "实验室B", rAt1.Add(2 * time.Hour)}
	if _, err := s.Confirm(badConfirm); !errors.Is(err, ErrConflict) {
		t.Fatalf("旧接收改时间应返回 ErrConflict, got %v", err)
	}

	// 冲突拒绝后：旧记录、当前状态、待确认详情、历史条数全部不变。
	tr, err = s.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	requireOldFirstTransfer(t, tr, hAt1, rAt1)
	p, _ = s.GetSample("S-001")
	requireParentMidSecondHandover(t, p, hAt2)
	if len(p.History) != historyBefore {
		t.Fatalf("被拒绝的旧请求不能改动历史: before=%d after=%d", historyBefore, len(p.History))
	}
	if len(s.data.Transfers) != 2 {
		t.Fatalf("被拒绝的旧请求不能新增交接, got %d 条", len(s.data.Transfers))
	}
	requireChildIndependent(t, s)

	// 王五按新交接约定确认接收：正常完成，只产生这一次接收应有的历史。
	rAt2 := hAt2.Add(time.Hour)
	done, err := s.Confirm(ConfirmInput{"TR-002", "王五", "实验室C", rAt2})
	if err != nil {
		t.Fatalf("新交接确认接收: %v", err)
	}
	if !done.Confirmed || done.Qty != "6.750" || done.ConfirmedBy != "王五" {
		t.Fatalf("新交接确认结果错误: %+v", done)
	}
	p, _ = s.GetSample("S-001")
	if p.Holder != "王五" || p.Location != "实验室C" || p.PendingTransfer != nil {
		t.Fatalf("新交接确认后应由王五在实验室C持有且结束待确认: %+v", p)
	}
	if p.Remaining != "6.750" {
		t.Fatalf("新交接确认后原样剩余量应为 6.750, got %s", p.Remaining)
	}
	wantKinds := []string{"register", "transfer-out", "transfer-in", "split", "transfer-out", "transfer-in"}
	if len(p.History) != len(wantKinds) {
		t.Fatalf("确认后历史条数错误: %+v", p.History)
	}
	for i, k := range wantKinds {
		if p.History[i].Kind != k {
			t.Fatalf("确认后历史顺序错误: %+v", p.History)
		}
	}
	last := p.History[len(p.History)-1]
	if last.Holder != "王五" || last.Location != "实验室C" || !last.Time.Equal(rAt2) {
		t.Fatalf("最后一条历史应只属于 TR-002 的接收: %+v", last)
	}
	// 旧交接记录与独立子样依旧不变。
	tr, _ = s.GetTransfer("TR-001")
	requireOldFirstTransfer(t, tr, hAt1, rAt1)
	requireChildIndependent(t, s)
}

// TestStaleHandoverResubmitAfterNextConfirmed 覆盖第二次交接完成后，再次
// 提交第一次交接的相同请求：仍返回旧交接记录，原样继续由王五在实验室C
// 持有，不回到李四处，也不重新进入待确认；改内容仍是 ErrConflict。
func TestStaleHandoverResubmitAfterNextConfirmed(t *testing.T) {
	s, hAt1, rAt1, _, rAt2 := setupDivergedHandover(t)
	tokyo := time.FixedZone("UTC+9", 9*60*60)

	if _, err := s.Confirm(ConfirmInput{"TR-002", "王五", "实验室C", rAt2}); err != nil {
		t.Fatalf("第二次交接接收: %v", err)
	}
	pBefore, _ := s.GetSample("S-001")
	if pBefore.Holder != "王五" || pBefore.Location != "实验室C" || pBefore.PendingTransfer != nil {
		t.Fatalf("前置状态错误: %+v", pBefore)
	}
	historyBefore := len(pBefore.History)

	// 完成后再提交第一次交接的相同交出与接收请求（空白与换时区的同等表示）。
	oldHandover := HandoverInput{
		TransferID: "\tTR-001\t", SampleID: " S-001 ",
		FromHolder: " 张三 ", FromLocation: " 实验室A ",
		ToHolder: " 李四 ", ToLocation: " 实验室B ",
		HandedOverAt: hAt1.In(tokyo),
	}
	tr, err := s.Handover(oldHandover)
	if err != nil {
		t.Fatalf("新交接完成后旧交出请求仍应幂等返回: %v", err)
	}
	requireOldFirstTransfer(t, tr, hAt1, rAt1)

	oldConfirm := ConfirmInput{
		TransferID: " TR-001 ",
		Receiver:   " 李四 ",
		AtLocation: " 实验室B ",
		ReceivedAt: rAt1.In(tokyo),
	}
	tr, err = s.Confirm(oldConfirm)
	if err != nil {
		t.Fatalf("新交接完成后旧接收请求仍应幂等返回: %v", err)
	}
	requireOldFirstTransfer(t, tr, hAt1, rAt1)

	p, _ := s.GetSample("S-001")
	if p.Holder != "王五" || p.Location != "实验室C" {
		t.Fatalf("旧请求不能让原样回到李四手中: %+v", p)
	}
	if p.PendingTransfer != nil {
		t.Fatalf("旧请求不能让原样重新进入待确认: %+v", p.PendingTransfer)
	}
	if p.Remaining != "6.750" || len(p.History) != historyBefore {
		t.Fatalf("旧请求不能改动剩余量或历史: rem=%s history=%d->%d",
			p.Remaining, historyBefore, len(p.History))
	}
	if len(s.data.Transfers) != 2 {
		t.Fatalf("旧请求不能新增交接记录, got %d 条", len(s.data.Transfers))
	}
	tr2, _ := s.GetTransfer("TR-002")
	if !tr2.Confirmed || tr2.ConfirmedBy != "王五" || tr2.Qty != "6.750" {
		t.Fatalf("新交接完成状态被旧请求改写: %+v", tr2)
	}
	requireChildIndependent(t, s)

	// 完成后改旧请求内容仍须拒绝，且旧交接、当前样品状态都保留。
	badHandover := oldHandover
	badHandover.ToLocation = "实验室C"
	if _, err := s.Handover(badHandover); !errors.Is(err, ErrConflict) {
		t.Fatalf("完成后沿用旧编号改目的地点应 ErrConflict, got %v", err)
	}
	badConfirm := ConfirmInput{"TR-001", "李四", "实验室B", rAt1.Add(2 * time.Hour)}
	if _, err := s.Confirm(badConfirm); !errors.Is(err, ErrConflict) {
		t.Fatalf("完成后旧接收改时间应 ErrConflict, got %v", err)
	}

	tr, _ = s.GetTransfer("TR-001")
	requireOldFirstTransfer(t, tr, hAt1, rAt1)
	p, _ = s.GetSample("S-001")
	if p.Holder != "王五" || p.Location != "实验室C" || p.PendingTransfer != nil ||
		p.Remaining != "6.750" || len(p.History) != historyBefore {
		t.Fatalf("冲突拒绝后当前状态被改动: %+v", p)
	}
	requireChildIndependent(t, s)
}
