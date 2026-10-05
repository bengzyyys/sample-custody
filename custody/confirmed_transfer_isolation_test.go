package custody

import (
	"errors"
	"testing"
	"time"
)

// 已确认交接返回详情隔离的公共前置：
// 原样 S-001（10.000 毫升）由张三在实验室A交给李四，李四在实验室B于 rAt
// 完成接收（交接 TR-001，交接量 10.000）。返回首次确认时拿到的详情。
func setupConfirmedTransfer(t *testing.T) (s *Store, first *TransferView, hAt, rAt time.Time) {
	t.Helper()
	s, _ = fixedStore(t)
	mustRegister(t, s, "S-001", "10.000", "张三", "实验室A")

	hAt = time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	rAt = hAt.Add(time.Hour)
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-001", SampleID: "S-001",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt,
	}); err != nil {
		t.Fatalf("交出: %v", err)
	}
	first, err := s.Confirm(ConfirmInput{"TR-001", "李四", "实验室B", rAt})
	if err != nil {
		t.Fatalf("确认接收: %v", err)
	}
	return s, first, hAt, rAt
}

// requireConfirmedTransfer 校验交接详情仍是首次真实确认的记录：
// 李四在实验室B于 rAt 接收，交接量 10.000，已确认。
func requireConfirmedTransfer(t *testing.T, tr *TransferView, hAt, rAt time.Time) {
	t.Helper()
	if tr.TransferID != "TR-001" || tr.SampleID != "S-001" {
		t.Fatalf("交接编号/样品被改写: %+v", tr)
	}
	if tr.Qty != "10.000" {
		t.Fatalf("交接量被改写: got %s", tr.Qty)
	}
	if tr.FromHolder != "张三" || tr.FromLocation != "实验室A" ||
		tr.ToHolder != "李四" || tr.ToLocation != "实验室B" {
		t.Fatalf("交接人员或地点被改写: %+v", tr)
	}
	if !tr.HandedOverAt.Equal(hAt) {
		t.Fatalf("交出时间被改写: got %s want %s", tr.HandedOverAt, hAt)
	}
	if !tr.Confirmed || tr.ConfirmedBy != "李四" || tr.ConfirmedAt != "实验室B" {
		t.Fatalf("确认状态被改写: %+v", tr)
	}
	if tr.ReceivedAt == nil || !tr.ReceivedAt.Equal(rAt) {
		t.Fatalf("接收时间被改写: got %v want %s", tr.ReceivedAt, rAt)
	}
}

// requireSampleHeldByReceiver 校验确认后样品由真实接收人李四在目的地点
// 实验室B持有，剩余量与保管历史（登记/交出/接收三条）不受影响。
func requireSampleHeldByReceiver(t *testing.T, s *Store, hAt, rAt time.Time) {
	t.Helper()
	p, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	if p.Holder != "李四" || p.Location != "实验室B" {
		t.Fatalf("样品应由李四在实验室B持有: %+v", p)
	}
	if p.Remaining != "10.000" || p.InitialQty != "10.000" {
		t.Fatalf("剩余量被改写: init=%s rem=%s", p.InitialQty, p.Remaining)
	}
	if p.PendingTransfer != nil {
		t.Fatalf("已确认交接不应再处于待确认: %+v", p.PendingTransfer)
	}
	if len(p.History) != 3 {
		t.Fatalf("保管历史条数被改写: %+v", p.History)
	}
	wantKinds := []string{"register", "transfer-out", "transfer-in"}
	for i, k := range wantKinds {
		if p.History[i].Kind != k {
			t.Fatalf("保管历史内容被改写: %+v", p.History)
		}
	}
	last := p.History[2]
	if last.Holder != "李四" || last.Location != "实验室B" || !last.Time.Equal(rAt) {
		t.Fatalf("接收历史事件被改写: %+v", last)
	}
}

// TestConfirmedReturnMutationStaysLocal 覆盖调用方修改首次确认返回的详情：
// 直接改写接收时间所指向的时间值（而非替换整个字段）及其余字段，都只能
// 改变手里的那份数据；再次查询仍显示首次真实的接收时间、确认人和确认地点，
// 样品保管事实不变，修改前已拿到的另一份详情也保留原值。
func TestConfirmedReturnMutationStaysLocal(t *testing.T) {
	s, first, hAt, rAt := setupConfirmedTransfer(t)

	// 修改前先按交接编号查询拿到另一份详情。
	before, err := s.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}

	// 直接修改首次返回详情中接收时间所指向的时间值，并改写其余字段。
	fakeAt := rAt.Add(3 * time.Hour)
	*first.ReceivedAt = fakeAt
	first.Confirmed = false
	first.ConfirmedBy = "王五"
	first.ConfirmedAt = "仓库Z"
	first.ToHolder = "王五"
	first.ToLocation = "仓库Z"
	first.Qty = "0.001"
	first.HandedOverAt = hAt.Add(5 * time.Hour)

	// 再次查询仍显示首次真实的确认信息。
	again, err := s.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	requireConfirmedTransfer(t, again, hAt, rAt)

	// 修改前已拿到的另一份详情保留原值，不随被修改的那份一起变化。
	requireConfirmedTransfer(t, before, hAt, rAt)

	// 样品继续由真实接收人在目的地点持有，剩余量与保管历史不受影响。
	requireSampleHeldByReceiver(t, s, hAt, rAt)
}

// TestQueriedAndResubmittedReturnMutationStaysLocal 覆盖修改后来查询或重复
// 提交得到的详情：无论详情来自哪个正常入口，修改都互不影响、也不影响
// 已保存的确认信息。
func TestQueriedAndResubmittedReturnMutationStaysLocal(t *testing.T) {
	s, _, hAt, rAt := setupConfirmedTransfer(t)
	fakeAt := rAt.Add(3 * time.Hour)

	// 修改按交接编号查询得到的详情（含接收时间指向的时间值）。
	queried, err := s.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	*queried.ReceivedAt = fakeAt
	queried.ConfirmedBy = "王五"

	// 重复提交相同交出信息返回的详情同样允许调用方自行修改。
	dupHandover, err := s.Handover(HandoverInput{
		TransferID: "TR-001", SampleID: "S-001",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt,
	})
	if err != nil {
		t.Fatalf("重复提交相同交出信息: %v", err)
	}
	requireConfirmedTransfer(t, dupHandover, hAt, rAt)
	*dupHandover.ReceivedAt = fakeAt.Add(time.Hour)
	dupHandover.ToLocation = "仓库Z"

	// 重复提交相同接收信息返回的详情也不例外。
	dupConfirm, err := s.Confirm(ConfirmInput{"TR-001", "李四", "实验室B", rAt})
	if err != nil {
		t.Fatalf("重复提交相同接收信息: %v", err)
	}
	requireConfirmedTransfer(t, dupConfirm, hAt, rAt)
	*dupConfirm.ReceivedAt = fakeAt.Add(2 * time.Hour)
	dupConfirm.ConfirmedAt = "仓库Z"

	// 各入口返回的详情互不相干：再次查询仍显示首次真实确认信息。
	again, err := s.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	requireConfirmedTransfer(t, again, hAt, rAt)

	// 被修改的查询结果不影响重复提交的判断：按首次真实信息再次确认仍成功。
	onceMore, err := s.Confirm(ConfirmInput{"TR-001", "李四", "实验室B", rAt})
	if err != nil {
		t.Fatalf("修改返回数据后按真实信息重复确认应成功: %v", err)
	}
	requireConfirmedTransfer(t, onceMore, hAt, rAt)

	// 样品保管事实不受影响。
	requireSampleHeldByReceiver(t, s, hAt, rAt)
}

// TestRepeatConfirmJudgedByStoredRecord 覆盖修改返回数据后的重复接收判断：
// 依据首次保存的信息，而不是调用方改过的那份详情。
func TestRepeatConfirmJudgedByStoredRecord(t *testing.T) {
	s, first, hAt, rAt := setupConfirmedTransfer(t)

	// 调用方把返回详情里的接收时间改成另一个时刻（仍晚于交出时间）。
	fakeAt := rAt.Add(3 * time.Hour)
	*first.ReceivedAt = fakeAt

	pBefore, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	historyBefore := len(pBefore.History)

	// 按首次真实的接收人、地点和时间再次确认：成功返回原交接，不追加历史。
	// 编号、人员、地点带首尾空白，时间换时区表示同一时刻，仍属相同请求。
	tokyo := time.FixedZone("UTC+9", 9*60*60)
	same, err := s.Confirm(ConfirmInput{
		TransferID: " TR-001 ",
		Receiver:   " 李四 ",
		AtLocation: " 实验室B ",
		ReceivedAt: rAt.In(tokyo),
	})
	if err != nil {
		t.Fatalf("按首次真实信息重复确认应成功: %v", err)
	}
	requireConfirmedTransfer(t, same, hAt, rAt)
	p, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.History) != historyBefore {
		t.Fatalf("重复确认不能追加接收历史: before=%d after=%d", historyBefore, len(p.History))
	}

	// 把接收时间改为返回数据里自行改过的那个时刻：即使仍晚于交出时间，
	// 也与首次保存的确认不一致，必须返回 ErrConflict，不能覆盖首次确认。
	if _, err := s.Confirm(ConfirmInput{"TR-001", "李四", "实验室B", fakeAt}); !errors.Is(err, ErrConflict) {
		t.Fatalf("按自行改过的接收时间重复确认应返回 ErrConflict, got %v", err)
	}

	// 拒绝后交接详情和样品查询都继续反映原有保管事实。
	tr, err := s.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	requireConfirmedTransfer(t, tr, hAt, rAt)
	requireSampleHeldByReceiver(t, s, hAt, rAt)
	if len(s.data.Transfers) != 1 {
		t.Fatalf("被拒绝的重复确认不能新增交接, got %d 条", len(s.data.Transfers))
	}
}

// TestResubmitSuccessReturnIsIndependentlyMutable 覆盖重复提交成功返回的
// 详情：虽然返回的是原交接，调用方对它的修改仍是独立的，不能影响下一次
// 查询或接收请求的判断。
func TestResubmitSuccessReturnIsIndependentlyMutable(t *testing.T) {
	s, _, hAt, rAt := setupConfirmedTransfer(t)

	// 重复提交相同接收信息成功，拿到返回的原交接详情并就地修改。
	dup, err := s.Confirm(ConfirmInput{"TR-001", "李四", "实验室B", rAt})
	if err != nil {
		t.Fatalf("重复提交相同接收信息: %v", err)
	}
	requireConfirmedTransfer(t, dup, hAt, rAt)
	*dup.ReceivedAt = rAt.Add(6 * time.Hour)
	dup.ConfirmedBy = "王五"
	dup.ConfirmedAt = "仓库Z"

	// 下一次查询仍显示首次真实确认信息。
	again, err := s.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	requireConfirmedTransfer(t, again, hAt, rAt)

	// 下一次接收请求仍按首次保存的信息判断：真实信息成功，改过的时刻冲突。
	if _, err := s.Confirm(ConfirmInput{"TR-001", "李四", "实验室B", rAt}); err != nil {
		t.Fatalf("修改重复提交返回的详情后，按真实信息再确认应成功: %v", err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-001", "李四", "实验室B", rAt.Add(6 * time.Hour)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("按自行改过的时刻确认应返回 ErrConflict, got %v", err)
	}

	// 样品保管事实始终不变。
	requireSampleHeldByReceiver(t, s, hAt, rAt)
}
