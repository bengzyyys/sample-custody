package custody

import (
	"errors"
	"testing"
	"time"
)

// 查询结果只是当次记录的副本：调用方对返回视图的任何改写，
// 都不能影响样品与交接的真实保管记录，也不能影响其他已取得的副本。
func TestGetSampleResultIsACopy(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "10.000", "张三", "实验室A")
	if _, err := s.Split(SplitInput{ParentID: "P", Parts: []SplitPart{
		{ID: "C1", Qty: "4.000"},
	}}); err != nil {
		t.Fatal(err)
	}
	hAt := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt,
	}); err != nil {
		t.Fatal(err)
	}

	// 连续取得两份查询结果。
	v1, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}

	// 基线：待确认期间仍显示交出人的持有人与地点，剩余量三位小数，
	// 待确认详情携带真实编号、接收人、目的地点、交出时间与全部剩余量。
	if v1.Holder != "张三" || v1.Location != "实验室A" || v1.Remaining != "6.000" {
		t.Fatalf("查询基线错误: %+v", v1)
	}
	if len(v1.Children) != 1 || v1.Children[0] != "C1" {
		t.Fatalf("子样列表基线错误: %v", v1.Children)
	}
	if len(v1.History) != 3 {
		t.Fatalf("历史基线应为 register/split/transfer-out 三条, got %d", len(v1.History))
	}
	pt := v1.PendingTransfer
	if pt == nil || pt.TransferID != "TR-1" || pt.ToHolder != "李四" ||
		pt.ToLocation != "实验室B" || !pt.HandedOverAt.Equal(hAt) ||
		pt.Qty != "6.000" || pt.Confirmed {
		t.Fatalf("待确认交接详情基线错误: %+v", pt)
	}

	// 调用方改写第一份结果：数量、持有人、地点、子样编号、历史内容，
	// 并在返回的列表中增删项目。
	v1.Remaining = "999.999"
	v1.Holder = "王五"
	v1.Location = "实验室Z"
	v1.Children[0] = "FAKE-CHILD"
	v1.Children = append(v1.Children, "EXTRA-CHILD")
	v1.History[0].Kind = "destroy"
	v1.History[0].Holder = "王五"
	v1.History[0].Detail = "篡改"
	v1.History = append(v1.History[:1], History{Kind: "transfer-in", Holder: "王五"})

	// 另一份已经取得的结果应保持原样。
	if v2.Remaining != "6.000" || v2.Holder != "张三" || v2.Location != "实验室A" {
		t.Fatalf("另一份查询结果被第一份的修改污染: %+v", v2)
	}
	if len(v2.Children) != 1 || v2.Children[0] != "C1" || len(v2.History) != 3 ||
		v2.History[0].Kind != "register" {
		t.Fatalf("另一份查询结果的列表被污染: %+v", v2)
	}

	// 再次查询仍应看到原来的真实记录。
	v3, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if v3.Remaining != "6.000" || v3.Holder != "张三" || v3.Location != "实验室A" {
		t.Fatalf("改写返回结果不应改动真实记录: %+v", v3)
	}
	if len(v3.Children) != 1 || v3.Children[0] != "C1" {
		t.Fatalf("真实子样列表被改写: %v", v3.Children)
	}
	if len(v3.History) != 3 || v3.History[0].Kind != "register" ||
		v3.History[0].Holder != "张三" || v3.History[1].Kind != "split" ||
		v3.History[2].Kind != "transfer-out" {
		t.Fatalf("真实保管历史被改写: %+v", v3.History)
	}

	// 既有子样的来源和数量不受影响。
	c1, err := s.GetSample("C1")
	if err != nil {
		t.Fatal(err)
	}
	if c1.ParentID != "P" || c1.InitialQty != "4.000" || c1.Remaining != "4.000" {
		t.Fatalf("子样记录被改写: %+v", c1)
	}
}

// 对待确认交接详情的隔离要求：改写返回的详情不影响真实交接，
// 按被改写的详情提交确认仍会失败，按真实内容确认则正常成功。
func TestPendingTransferDetailIsACopy(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "P", "8.000", "张三", "实验室A")
	hAt := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-1", SampleID: "P",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if got.PendingTransfer == nil {
		t.Fatalf("应有待确认交接详情")
	}

	// 调用方在返回的详情中改掉接收人、目的地点、时间和确认标记。
	fake := got.PendingTransfer
	fake.ToHolder = "王五"
	fake.ToLocation = "实验室Z"
	fake.HandedOverAt = hAt.Add(5 * time.Hour)
	fake.Confirmed = true
	fake.ConfirmedBy = "王五"

	// 按交接编号查询的真实交接仍处于待确认，内容与首次交出一致。
	tr, err := s.GetTransfer("TR-1")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Confirmed || tr.ToHolder != "李四" || tr.ToLocation != "实验室B" ||
		!tr.HandedOverAt.Equal(hAt) || tr.Qty != "8.000" {
		t.Fatalf("真实交接被返回详情的改写污染: %+v", tr)
	}

	// 样品不能因此换人、换地点或解除待确认限制。
	again, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if again.Holder != "张三" || again.Location != "实验室A" || again.PendingTransfer == nil {
		t.Fatalf("样品状态被返回详情的改写污染: %+v", again)
	}

	// 按被改写的详情，以另一名接收人或另一个地点提交确认，仍应 ErrConflict。
	if _, err := s.Confirm(ConfirmInput{"TR-1", "王五", "实验室B", hAt.Add(time.Hour)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("按改写的接收人确认应拒绝, got %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-1", "李四", "实验室Z", hAt.Add(time.Hour)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("按改写的地点确认应拒绝, got %v", err)
	}

	// 拒绝后原交接和原历史保留。
	tr, _ = s.GetTransfer("TR-1")
	if tr.Confirmed || tr.ToHolder != "李四" || tr.ToLocation != "实验室B" {
		t.Fatalf("被拒绝的确认改动了交接: %+v", tr)
	}
	again, _ = s.GetSample("P")
	if len(again.History) != 2 || again.PendingTransfer == nil {
		t.Fatalf("被拒绝的确认改动了历史或待确认状态: %+v", again)
	}

	// 原来指定的接收人在原目的地点、以不早于原交出时间的接收时间确认，正常成功。
	done, err := s.Confirm(ConfirmInput{"TR-1", "李四", "实验室B", hAt})
	if err != nil {
		t.Fatalf("按真实内容确认应成功: %v", err)
	}
	if !done.Confirmed || done.Qty != "8.000" {
		t.Fatalf("确认结果应以真实记录为准: %+v", done)
	}

	// 只追加一次接收历史，交接数量和样品剩余量仍以真实记录为准。
	final, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if len(final.History) != 3 || final.History[2].Kind != "transfer-in" {
		t.Fatalf("确认后应只追加一次接收历史: %+v", final.History)
	}
	if final.Remaining != "8.000" || final.Holder != "李四" || final.Location != "实验室B" ||
		final.PendingTransfer != nil {
		t.Fatalf("确认后样品状态错误: %+v", final)
	}
	tr, _ = s.GetTransfer("TR-1")
	if tr.Qty != "8.000" || tr.ConfirmedBy != "李四" {
		t.Fatalf("确认后交接内容应以真实记录为准: %+v", tr)
	}
}

// 没有待确认交接、也没有子样的查询结果：子样列表为空、历史保留、
// 待确认详情为空；调用方自行添入的内容不能创建对应记录。
func TestGetSampleEmptyResultStaysEmpty(t *testing.T) {
	s, _ := fixedStore(t)
	mustRegister(t, s, "S", "2.500", "张三", "实验室A")

	got, err := s.GetSample("S")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Children) != 0 || got.PendingTransfer != nil {
		t.Fatalf("新登记样品不应有子样或待确认交接: %+v", got)
	}
	if len(got.History) != 1 || got.History[0].Kind != "register" {
		t.Fatalf("历史应保留登记记录: %+v", got.History)
	}
	if got.InitialQty != "2.500" || got.Remaining != "2.500" {
		t.Fatalf("数量应统一显示三位小数: %+v", got)
	}

	// 调用方在返回结果上自行添入子样、交接详情和历史。
	got.Children = append(got.Children, "GHOST-CHILD")
	got.PendingTransfer = &TransferView{TransferID: "GHOST-TR", ToHolder: "王五"}
	got.History = append(got.History, History{Kind: "transfer-out", Holder: "王五"})

	// 再次查询仍为空，历史仍只有一条。
	again, err := s.GetSample("S")
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Children) != 0 || again.PendingTransfer != nil || len(again.History) != 1 {
		t.Fatalf("自行添入的内容不应进入真实记录: %+v", again)
	}

	// 这些不存在的编号仍应返回 ErrNotFound。
	if _, err := s.GetSample("GHOST-CHILD"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("添入的子样编号不应被创建, got %v", err)
	}
	if _, err := s.GetTransfer("GHOST-TR"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("添入的交接编号不应被创建, got %v", err)
	}
}
