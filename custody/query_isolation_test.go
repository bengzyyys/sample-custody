package custody

import (
	"errors"
	"testing"
	"time"
)

// 查询结果隔离的公共前置：一份已分出子样、仍有剩余量并已发起交接的原样。
// 原样 P：初始 10.000，分出 C1 4.000 后剩余 6.000，交接 TR-1 待确认。
func setupPendingParent(t *testing.T) (*Store, time.Time, time.Time) {
	t.Helper()
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "10.000", "张三", "实验室A")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C1", Qty: "4.000"},
	}}); err != nil {
		t.Fatalf("split: %v", err)
	}
	hAt := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt,
	}); err != nil {
		t.Fatalf("handover: %v", err)
	}
	return s, hAt, hAt.Add(time.Hour)
}

// 校验 P 的查询结果仍是真实记录：剩余 6.000、张三在实验室A、子样 [C1]、
// 三条历史、待确认交接 TR-1 内容与原交出一致。
func requirePristineParent(t *testing.T, got *Sample, hAt time.Time) {
	t.Helper()
	if got.Remaining != "6.000" || got.InitialQty != "10.000" {
		t.Fatalf("数量被改写: init=%s rem=%s", got.InitialQty, got.Remaining)
	}
	if got.Holder != "张三" || got.Location != "实验室A" {
		t.Fatalf("待确认期间持有人/地点应保持交出前状态: %+v", got)
	}
	if len(got.Children) != 1 || got.Children[0] != "C1" {
		t.Fatalf("子样列表被改写: %v", got.Children)
	}
	if len(got.History) != 3 {
		t.Fatalf("历史条数被改写: %d", len(got.History))
	}
	wantKinds := []string{"register", "split", "transfer-out"}
	for i, k := range wantKinds {
		if got.History[i].Kind != k {
			t.Fatalf("历史内容被改写: %+v", got.History)
		}
	}
	pt := got.PendingTransfer
	if pt == nil {
		t.Fatalf("待确认交接详情丢失")
	}
	if pt.TransferID != "TR-1" || pt.ToHolder != "李四" || pt.ToLocation != "实验室B" ||
		!pt.HandedOverAt.Equal(hAt) || pt.Qty != "6.000" || pt.Confirmed {
		t.Fatalf("待确认交接详情被改写: %+v", pt)
	}
}

func TestGetSampleResultIsIsolatedCopy(t *testing.T) {
	s, hAt, _ := setupPendingParent(t)

	got, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	requirePristineParent(t, got, hAt)

	// 调用方改写返回结果中的标量、子样编号和历史内容，并增删列表项目。
	got.Remaining = "0.001"
	got.InitialQty = "0.001"
	got.Holder = "王五"
	got.Location = "仓库Z"
	got.Children[0] = "C-FAKE"
	got.Children = append(got.Children, "C-GHOST")
	got.Children = got.Children[:0]
	got.History[0].Kind = "destroy"
	got.History[0].Holder = "王五"
	got.History[0].Detail = "篡改"
	got.History = append(got.History, History{Kind: "transfer-in", Holder: "王五"})
	got.History = got.History[:1]
	got.PendingTransfer.ToHolder = "王五"
	got.PendingTransfer.Qty = "0.001"

	// 再次查询仍应看到原来的真实记录。
	again, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	requirePristineParent(t, again, hAt)

	// 既有子样的来源和数量不受篡改影响。
	c1, err := s.GetSample("C1")
	if err != nil {
		t.Fatal(err)
	}
	if c1.ParentID != "P" || c1.InitialQty != "4.000" || c1.Remaining != "4.000" ||
		c1.Holder != "张三" || c1.Location != "实验室A" {
		t.Fatalf("子样记录被改写: %+v", c1)
	}
	// 篡改中虚构的编号不存在。
	if _, err := s.GetSample("C-FAKE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("虚构子样编号不应存在, got %v", err)
	}
	if _, err := s.GetSample("C-GHOST"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("虚构子样编号不应存在, got %v", err)
	}
}

func TestGetSampleResultsAreIndependent(t *testing.T) {
	s, hAt, _ := setupPendingParent(t)

	first, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}

	// 深改第一份结果，第二份已经取得的结果应保持原样。
	first.Remaining = "0.001"
	first.Holder = "王五"
	first.Children[0] = "C-FAKE"
	first.History[0].Kind = "destroy"
	first.History[2].Detail = "篡改"
	first.PendingTransfer.ToHolder = "王五"
	first.PendingTransfer.Confirmed = true

	requirePristineParent(t, second, hAt)
}

func TestPendingTransferViewIsIsolatedCopy(t *testing.T) {
	s, hAt, rAt := setupPendingParent(t)

	got, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	// 改写待确认交接详情：接收人、目的地点、时间、确认标记、数量。
	pt := got.PendingTransfer
	pt.ToHolder = "王五"
	pt.ToLocation = "实验室C"
	pt.HandedOverAt = hAt.Add(5 * time.Hour)
	pt.Confirmed = true
	pt.ConfirmedBy = "王五"
	pt.Qty = "0.001"

	// 按交接编号查询的真实交接仍处于待确认，内容与首次交出一致。
	tr, err := s.GetTransfer("TR-1")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Confirmed || tr.ToHolder != "李四" || tr.ToLocation != "实验室B" ||
		!tr.HandedOverAt.Equal(hAt) || tr.Qty != "6.000" || tr.ReceivedAt != nil {
		t.Fatalf("真实交接被改写: %+v", tr)
	}
	// 样品不能因此换人、换地点或解除待确认限制。
	cur, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	requirePristineParent(t, cur, hAt)

	// 按被改写的详情提交确认：另一名接收人、另一个地点均返回 ErrConflict。
	if _, err := s.Confirm(ConfirmInput{"TR-1", "王五", "实验室B", rAt}); !errors.Is(err, ErrConflict) {
		t.Fatalf("按篡改接收人确认应 ErrConflict, got %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-1", "李四", "实验室C", rAt}); !errors.Is(err, ErrConflict) {
		t.Fatalf("按篡改地点确认应 ErrConflict, got %v", err)
	}
	// 原交接和原历史保留。
	tr, _ = s.GetTransfer("TR-1")
	if tr.Confirmed || tr.ToHolder != "李四" || tr.ToLocation != "实验室B" {
		t.Fatalf("被拒绝的确认改动了交接: %+v", tr)
	}
	cur, _ = s.GetSample("P")
	requirePristineParent(t, cur, hAt)

	// 原来指定的接收人在原目的地点、以不早于交出时间的接收时间确认：正常成功。
	done, err := s.Confirm(ConfirmInput{"TR-1", "李四", "实验室B", rAt})
	if err != nil {
		t.Fatalf("确认: %v", err)
	}
	if !done.Confirmed || done.Qty != "6.000" {
		t.Fatalf("确认结果数量应以真实记录为准: %+v", done)
	}
	final, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if final.Holder != "李四" || final.Location != "实验室B" || final.PendingTransfer != nil {
		t.Fatalf("确认后持有人/地点/待确认状态错误: %+v", final)
	}
	if final.Remaining != "6.000" {
		t.Fatalf("确认后剩余量应以真实记录为准, got %s", final.Remaining)
	}
	// 只追加一次接收历史。
	if len(final.History) != 4 || final.History[3].Kind != "transfer-in" ||
		final.History[3].Holder != "李四" || final.History[3].Location != "实验室B" {
		t.Fatalf("确认后历史错误: %+v", final.History)
	}
}

func TestGetSampleNoChildrenNoPendingStaysIsolated(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "S", "2.5", "张三", "实验室A")

	got, err := s.GetSample("S")
	if err != nil {
		t.Fatal(err)
	}
	// 无子样、无待确认交接：子样列表为空，历史保留，待确认详情为空。
	if len(got.Children) != 0 {
		t.Fatalf("子样列表应为空: %v", got.Children)
	}
	if len(got.History) != 1 || got.History[0].Kind != "register" {
		t.Fatalf("历史应保留: %+v", got.History)
	}
	if got.PendingTransfer != nil {
		t.Fatalf("待确认详情应为空: %+v", got.PendingTransfer)
	}
	if got.Remaining != "2.500" || got.InitialQty != "2.500" {
		t.Fatalf("数量应统一显示三位小数: init=%s rem=%s", got.InitialQty, got.Remaining)
	}

	// 调用方在返回结果上自行添入子样或交接详情，不能创建对应记录。
	got.Children = append(got.Children, "S-CHILD")
	got.PendingTransfer = &TransferView{TransferID: "TR-GHOST", SampleID: "S", ToHolder: "李四"}

	again, err := s.GetSample("S")
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Children) != 0 || again.PendingTransfer != nil {
		t.Fatalf("自行添入的内容不应进入真实记录: %+v", again)
	}
	if len(again.History) != 1 {
		t.Fatalf("历史不应变化: %+v", again.History)
	}
	if _, err := s.GetSample("S-CHILD"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("添入的子样编号不应被创建, got %v", err)
	}
	if _, err := s.GetTransfer("TR-GHOST"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("添入的交接编号不应被创建, got %v", err)
	}
}
