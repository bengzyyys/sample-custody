package custody

import (
	"errors"
	"testing"
	"time"
)

// 已确认交接返回详情隔离的公共前置：原样 S（10.000 毫升）由张三在
// 实验室A 交给李四，李四在实验室B 于交出一小时后确认接收 TR-1。
// 返回首次确认的真实入参与交出时间；确认动作由各测试自行发起，以便拿到
// 首次确认成功时返回的那份详情。
func setupConfirmedTransfer(t *testing.T) (*Store, ConfirmInput, time.Time) {
	t.Helper()
	s, _ := fixedStore(t)
	mustRegister(t, s, "S", "10.000", "张三", "实验室A")
	hAt := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	if _, err := s.Handover(HandoverInput{
		TransferID:   "TR-1",
		SampleID:     "S",
		FromHolder:   "张三",
		FromLocation: "实验室A",
		ToHolder:     "李四",
		ToLocation:   "实验室B",
		HandedOverAt: hAt,
	}); err != nil {
		t.Fatalf("handover: %v", err)
	}
	cin := ConfirmInput{
		TransferID: "TR-1",
		Receiver:   "李四",
		AtLocation: "实验室B",
		ReceivedAt: hAt.Add(time.Hour),
	}
	return s, cin, hAt
}

// requirePristineConfirmedTransfer 校验交接详情仍是首次确认保存的真实
// 记录：已确认、真实接收人、确认地点、真实接收时间，以及原交出信息与
// 交接量（10.000 毫升原样全部剩余量）。
func requirePristineConfirmedTransfer(t *testing.T, tr *TransferView, cin ConfirmInput, hAt time.Time) {
	t.Helper()
	if tr.TransferID != "TR-1" || tr.SampleID != "S" {
		t.Fatalf("交接编号/样品被改写: %+v", tr)
	}
	if tr.FromHolder != "张三" || tr.FromLocation != "实验室A" ||
		tr.ToHolder != "李四" || tr.ToLocation != "实验室B" {
		t.Fatalf("交接人员或地点被改写: %+v", tr)
	}
	if tr.Qty != "10.000" {
		t.Fatalf("交接量被改写: got %s", tr.Qty)
	}
	if !tr.HandedOverAt.Equal(hAt) {
		t.Fatalf("交出时间被改写: got %s want %s", tr.HandedOverAt, hAt)
	}
	if !tr.Confirmed {
		t.Fatalf("交接应仍为已确认: %+v", tr)
	}
	if tr.ConfirmedBy != cin.Receiver {
		t.Fatalf("确认人被改写: got %q want %q", tr.ConfirmedBy, cin.Receiver)
	}
	if tr.ConfirmedAt != cin.AtLocation {
		t.Fatalf("确认地点被改写: got %q want %q", tr.ConfirmedAt, cin.AtLocation)
	}
	if tr.ReceivedAt == nil {
		t.Fatalf("已确认交接的接收时间丢失: %+v", tr)
	}
	if !tr.ReceivedAt.Equal(cin.ReceivedAt) {
		t.Fatalf("接收时间被改写: got %s want %s", *tr.ReceivedAt, cin.ReceivedAt)
	}
}

// requirePristineSampleAfterConfirm 校验样品侧的保管事实仍是首次确认后的
// 状态：李四在实验室B 持有全部 10.000 毫升，无待确认交接，保管历史仍为
// 登记/交出/接收三条且时间、人员、地点与首次记录一致。
func requirePristineSampleAfterConfirm(t *testing.T, s *Store, cin ConfirmInput, hAt time.Time) {
	t.Helper()
	got, err := s.GetSample("S")
	if err != nil {
		t.Fatalf("get sample: %v", err)
	}
	if got.InitialQty != "10.000" || got.Remaining != "10.000" {
		t.Fatalf("数量被改写: init=%s rem=%s", got.InitialQty, got.Remaining)
	}
	if got.Holder != cin.Receiver || got.Location != cin.AtLocation {
		t.Fatalf("样品应继续由真实接收人在目的地点持有: %+v", got)
	}
	if got.PendingTransfer != nil {
		t.Fatalf("确认后不应再有待确认交接: %+v", got.PendingTransfer)
	}
	wantKinds := []string{"register", "transfer-out", "transfer-in"}
	if len(got.History) != len(wantKinds) {
		t.Fatalf("保管历史条数被改写: got %d want %d (%+v)", len(got.History), len(wantKinds), got.History)
	}
	for i, k := range wantKinds {
		if got.History[i].Kind != k {
			t.Fatalf("保管历史内容被改写: %+v", got.History)
		}
	}
	out := got.History[1]
	if out.Holder != "张三" || out.Location != "实验室A" || !out.Time.Equal(hAt) {
		t.Fatalf("交出历史事件被改写: %+v", out)
	}
	in := got.History[2]
	if in.Holder != cin.Receiver || in.Location != cin.AtLocation || !in.Time.Equal(cin.ReceivedAt) {
		t.Fatalf("接收历史事件被改写: %+v", in)
	}
}

// TestConfirmResultIsIsolatedCopy 覆盖首次确认成功返回的详情：无论直接
// 修改接收时间指针指向的时间值，还是整体替换接收时间字段，连同确认人、
// 确认地点、确认标记、交出信息一起改写，都只能改动调用方手里的那份数据。
// 按交接编号再查与样品查询仍显示首次真实接收事实。
func TestConfirmResultIsIsolatedCopy(t *testing.T) {
	s, cin, hAt := setupConfirmedTransfer(t)
	// 篡改时刻仍晚于交出时间，以区分 ErrInvalid（接收早于交出）的情形。
	tamperedAt := cin.ReceivedAt.Add(2 * time.Hour)

	first, err := s.Confirm(cin)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	requirePristineConfirmedTransfer(t, first, cin, hAt)

	// 方式一：直接修改接收时间所指向的时间值（不替换字段），并改写其余确认信息。
	*first.ReceivedAt = tamperedAt
	first.Confirmed = false
	first.ConfirmedBy = "王五"
	first.ConfirmedAt = "实验室C"
	first.ToHolder = "王五"
	first.ToLocation = "实验室C"

	tr, err := s.GetTransfer("TR-1")
	if err != nil {
		t.Fatal(err)
	}
	requirePristineConfirmedTransfer(t, tr, cin, hAt)
	requirePristineSampleAfterConfirm(t, s, cin, hAt)

	// 方式二：整体替换接收时间字段，并继续改写交出侧信息。
	repl := tamperedAt.Add(time.Hour)
	first.ReceivedAt = &repl
	first.HandedOverAt = hAt.Add(5 * time.Hour)
	first.Qty = "0.001"

	tr, err = s.GetTransfer("TR-1")
	if err != nil {
		t.Fatal(err)
	}
	requirePristineConfirmedTransfer(t, tr, cin, hAt)
	requirePristineSampleAfterConfirm(t, s, cin, hAt)

	// 按编号查询入口拿到的详情同样隔离：直接改指针所指时间值不影响下一次查询。
	queried, err := s.GetTransfer("TR-1")
	if err != nil {
		t.Fatal(err)
	}
	*queried.ReceivedAt = tamperedAt
	queried.ConfirmedBy = "王五"
	again, err := s.GetTransfer("TR-1")
	if err != nil {
		t.Fatal(err)
	}
	requirePristineConfirmedTransfer(t, again, cin, hAt)
	requirePristineSampleAfterConfirm(t, s, cin, hAt)
}

// prepareConfirmedViews 在一份已确认交接数据上，从四个正常入口各取一份
// 交接详情：首次确认成功、按交接编号查询、重复提交相同交出信息、重复提交
// 相同接收信息。
func prepareConfirmedViews(t *testing.T) (*Store, map[string]*TransferView, ConfirmInput, time.Time) {
	t.Helper()
	s, cin, hAt := setupConfirmedTransfer(t)

	first, err := s.Confirm(cin)
	if err != nil {
		t.Fatalf("首次确认: %v", err)
	}
	queried, err := s.GetTransfer("TR-1")
	if err != nil {
		t.Fatal(err)
	}
	// 已确认交接重复提交相同交出信息：返回原交接，详情中带首次确认信息。
	reHandover, err := s.Handover(HandoverInput{
		TransferID: "TR-1", SampleID: "S",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B",
		HandedOverAt: hAt,
	})
	if err != nil {
		t.Fatalf("重复提交相同交出信息: %v", err)
	}
	// 已确认交接重复提交相同接收信息：返回原交接，不追加接收历史。
	reConfirm, err := s.Confirm(cin)
	if err != nil {
		t.Fatalf("重复提交相同接收信息: %v", err)
	}

	views := map[string]*TransferView{
		"首次确认返回": first,
		"编号查询返回": queried,
		"重复交出返回": reHandover,
		"重复接收返回": reConfirm,
	}
	return s, views, cin, hAt
}

// TestConfirmedTransferViewsAreIndependentAcrossEntries 覆盖四个会返回已确认
// 交接详情的正常入口：首次确认成功、按交接编号查询、重复提交相同交出信息、
// 重复提交相同接收信息。深改其中任意一份详情（含直接改接收时间指针所指的
// 时间值），既不能影响真实记录，也不能牵连此前从其他入口拿到的另一份详情。
func TestConfirmedTransferViewsAreIndependentAcrossEntries(t *testing.T) {
	tamperedAt := func(cin ConfirmInput) time.Time {
		return cin.ReceivedAt.Add(2 * time.Hour)
	}

	for _, mutated := range []string{"首次确认返回", "编号查询返回", "重复交出返回", "重复接收返回"} {
		mutated := mutated
		t.Run(mutated, func(t *testing.T) {
			// 每个入口独立建一份前置，避免各入口的篡改互相累积。
			s, views, cin, hAt := prepareConfirmedViews(t)
			changed := tamperedAt(cin)

			// 篡改前各入口拿到的详情都与首次真实记录一致。
			for _, v := range views {
				requirePristineConfirmedTransfer(t, v, cin, hAt)
			}

			v := views[mutated]
			// 深改这份详情：直接改接收时间指针所指时间值，并改写确认与交出信息。
			*v.ReceivedAt = changed
			v.Confirmed = false
			v.ConfirmedBy = "王五"
			v.ConfirmedAt = "实验室C"
			v.ToHolder = "王五"
			v.ToLocation = "实验室C"
			v.HandedOverAt = hAt.Add(5 * time.Hour)
			v.Qty = "0.001"

			// 真实记录不变。
			fresh, err := s.GetTransfer("TR-1")
			if err != nil {
				t.Fatal(err)
			}
			requirePristineConfirmedTransfer(t, fresh, cin, hAt)
			requirePristineSampleAfterConfirm(t, s, cin, hAt)

			// 从其他入口已经拿到的详情也不能跟着这份一起变化。
			for other, ov := range views {
				if other == mutated {
					continue
				}
				requirePristineConfirmedTransfer(t, ov, cin, hAt)
			}
		})
	}
}

// TestConfirmedTransferCopiesDoNotAffectReconfirm 覆盖返回详情被修改后，
// 重复接收的幂等与冲突判断仍只依据首次保存的信息：
//   - 按首次真实接收人、地点、时间（首尾空白 + 换时区表示同一时刻）再次
//     确认，成功返回原交接且不追加接收历史；
//   - 把请求接收时间改成返回详情里自行改过的另一时刻（仍晚于交出时间），
//     返回可用 errors.Is 识别的 ErrConflict，不覆盖首次确认；
//   - 拒绝后交接详情与样品查询继续反映原有保管事实；
//   - 重复提交成功返回的详情本身也是独立副本，修改它不影响下一次判断。
func TestConfirmedTransferCopiesDoNotAffectReconfirm(t *testing.T) {
	s, cin, hAt := setupConfirmedTransfer(t)
	tokyo := time.FixedZone("UTC+9", 9*60*60)
	// 篡改时刻晚于交出时间两小时：若按篡改时刻请求，属于信息冲突而非
	// “接收早于交出”，错误分类必须是 ErrConflict。
	tamperedAt := cin.ReceivedAt.Add(2 * time.Hour)

	first, err := s.Confirm(cin)
	if err != nil {
		t.Fatalf("首次确认: %v", err)
	}
	requirePristineConfirmedTransfer(t, first, cin, hAt)
	requirePristineSampleAfterConfirm(t, s, cin, hAt)

	// 与首次真实接收完全相同的请求：编号、人员、地点带首尾空白，时间换时区
	// 表示同一实际时刻。
	trueConfirm := func() ConfirmInput {
		return ConfirmInput{
			TransferID: " TR-1 ",
			Receiver:   " 李四 ",
			AtLocation: " 实验室B ",
			ReceivedAt: cin.ReceivedAt.In(tokyo),
		}
	}

	// 篡改一份已确认详情：直接改接收时间指针所指时间值，并改确认人/地点/标记。
	tamperView := func(v *TransferView) {
		t.Helper()
		if v.ReceivedAt == nil {
			t.Fatalf("前置：已确认详情应带接收时间")
		}
		*v.ReceivedAt = tamperedAt
		v.Confirmed = false
		v.ConfirmedBy = "王五"
		v.ConfirmedAt = "实验室C"
		v.ToHolder = "王五"
		v.ToLocation = "实验室C"
	}

	// 每次篡改后，重复接收的判断都必须回到首次保存的信息上；返回的原交接
	// 详情交还给调用方继续独立修改。
	replayAndAssert := func(stage string) *TransferView {
		t.Helper()
		replay, err := s.Confirm(trueConfirm())
		if err != nil {
			t.Fatalf("%s：按首次真实信息重复接收应成功返回原交接, got %v", stage, err)
		}
		requirePristineConfirmedTransfer(t, replay, cin, hAt)

		// 用篡改详情中的另一时刻（仍晚于交出时间）重复接收：ErrConflict。
		bad := ConfirmInput{
			TransferID: "TR-1",
			Receiver:   "李四",
			AtLocation: "实验室B",
			ReceivedAt: tamperedAt,
		}
		if _, err := s.Confirm(bad); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s：按篡改时刻重复接收应 ErrConflict, got %v", stage, err)
		}

		// 拒绝后交接详情与样品保管事实都不变，冲突请求既不覆盖确认也不追加历史。
		tr, err := s.GetTransfer("TR-1")
		if err != nil {
			t.Fatal(err)
		}
		requirePristineConfirmedTransfer(t, tr, cin, hAt)
		requirePristineSampleAfterConfirm(t, s, cin, hAt)
		if len(s.data.Transfers) != 1 {
			t.Fatalf("%s：重复接收不得另建交接, got %d 条", stage, len(s.data.Transfers))
		}
		return replay
	}

	// 1) 篡改首次确认返回的详情（直接改指针所指时间值）。
	tamperView(first)
	replay := replayAndAssert("首次确认详情被改后")

	// 2) 同一份详情再整体替换接收时间字段，判断依据仍是首次保存的信息。
	repl := tamperedAt
	first.ReceivedAt = &repl
	replayAndAssert("首次确认详情字段被替换后")

	// 3) 重复接收成功返回的详情也是独立副本：修改它不影响下一次查询和接收请求。
	tamperView(replay)
	replay = replayAndAssert("重复接收返回详情被改后")

	// 4) 按编号查询得到的详情被改，同样不影响判断。
	queried, err := s.GetTransfer("TR-1")
	if err != nil {
		t.Fatal(err)
	}
	tamperView(queried)
	replayAndAssert("查询返回详情被改后")

	// 5) 重复提交相同交出信息返回的详情（含首次确认信息）也是独立副本。
	reHandover, err := s.Handover(HandoverInput{
		TransferID: " TR-1 ", SampleID: " S ",
		FromHolder: " 张三 ", FromLocation: " 实验室A ",
		ToHolder: " 李四 ", ToLocation: " 实验室B ",
		HandedOverAt: hAt.In(tokyo),
	})
	if err != nil {
		t.Fatalf("重复提交相同交出信息应返回原交接: %v", err)
	}
	requirePristineConfirmedTransfer(t, reHandover, cin, hAt)
	tamperView(reHandover)
	replay = replayAndAssert("重复交出返回详情被改后")

	// 最终：样品仍由真实接收人在目的地点持有，数量与保管历史不变；最后一次
	// 幂等重复接收返回的详情仍可独立修改，不影响再下一次查询与接收请求。
	tamperView(replay)
	final, err := s.GetTransfer("TR-1")
	if err != nil {
		t.Fatal(err)
	}
	requirePristineConfirmedTransfer(t, final, cin, hAt)
	requirePristineSampleAfterConfirm(t, s, cin, hAt)
	last, err := s.Confirm(trueConfirm())
	if err != nil {
		t.Fatalf("最终按真实信息重复接收应仍成功: %v", err)
	}
	requirePristineConfirmedTransfer(t, last, cin, hAt)
	requirePristineSampleAfterConfirm(t, s, cin, hAt)
}
