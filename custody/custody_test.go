package custody

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "custody.json")
	s, err := Create(path)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRegisterSuccess(t *testing.T) {
	s := newStore(t)
	v, err := s.Register(" S1 ", "10", " Alice ", " Lab A ")
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if v.ID != "S1" {
		t.Fatalf("ID = %q, want S1", v.ID)
	}
	if v.Holder != "Alice" || v.Location != "Lab A" {
		t.Fatalf("holder/location = %q/%q", v.Holder, v.Location)
	}
	if v.InitialQty != "10.000" || v.RemainingQty != "10.000" {
		t.Fatalf("qty = %s/%s, want 10.000", v.InitialQty, v.RemainingQty)
	}
	if v.ParentID != "" || len(v.Children) != 0 {
		t.Fatalf("parent/children = %q/%v", v.ParentID, v.Children)
	}
	if len(v.History) != 1 || v.History[0].Type != eventRegistered {
		t.Fatalf("history = %v, want single registered event", v.History)
	}
}

func TestRegisterQuantityFormats(t *testing.T) {
	s := newStore(t)
	cases := map[string]string{
		"1":       "1.000",
		"1.5":     "1.500",
		"1.500":   "1.500",
		"0.001":   "0.001",
		"100.25":  "100.250",
		"010.000": "10.000",
	}
	i := 0
	for in, want := range cases {
		id := fmt.Sprintf("S%d", i)
		v, err := s.Register(id, in, "A", "L")
		if err != nil {
			t.Fatalf("Register(%q) failed: %v", in, err)
		}
		if v.InitialQty != want {
			t.Fatalf("Register(%q) = %s, want %s", in, v.InitialQty, want)
		}
		i++
	}
}

func TestRegisterInvalidQuantity(t *testing.T) {
	s := newStore(t)
	bad := []string{"", "0", "-1", "1.2345", "1.2.3", "abc", " 1 ", "1.", ".5", "0.000"}
	for _, q := range bad {
		if _, err := s.Register("BAD", q, "A", "L"); !errors.Is(err, ErrInvalidQuantity) {
			t.Fatalf("Register(%q) error = %v, want ErrInvalidQuantity", q, err)
		}
	}
}

func TestRegisterInvalidInput(t *testing.T) {
	s := newStore(t)
	for _, c := range []struct{ id, holder, loc string }{
		{"", "A", "L"},
		{"S", "", "L"},
		{"S", "A", ""},
		{"  ", "A", "L"},
	} {
		if _, err := s.Register(c.id, "1", c.holder, c.loc); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Register(%q,%q,%q) error = %v, want ErrInvalidInput", c.id, c.holder, c.loc, err)
		}
	}
}

func TestRegisterDuplicateID(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "1", "A", "L"); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if _, err := s.Register("S1", "2", "B", "M"); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate Register error = %v, want ErrDuplicateID", err)
	}
	// 重复登记不得改变原记录。
	v, err := s.GetSample("S1")
	if err != nil {
		t.Fatalf("GetSample: %v", err)
	}
	if v.Holder != "A" || v.InitialQty != "1.000" {
		t.Fatalf("sample changed after duplicate: %+v", v)
	}
}

func TestAliquotSuccess(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "10", "Alice", "Lab A"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	v, err := s.Aliquot("S1", []Aliquot{
		{SampleID: "S1-1", Quantity: "3.5"},
		{SampleID: "S1-2", Quantity: "1.25"},
	})
	if err != nil {
		t.Fatalf("Aliquot: %v", err)
	}
	if v.RemainingQty != "5.250" {
		t.Fatalf("remaining = %s, want 5.250", v.RemainingQty)
	}
	if len(v.Children) != 2 {
		t.Fatalf("children = %v, want 2", v.Children)
	}

	c1, err := s.GetSample("S1-1")
	if err != nil {
		t.Fatalf("GetSample child: %v", err)
	}
	if c1.InitialQty != "3.500" || c1.RemainingQty != "3.500" {
		t.Fatalf("child qty = %s/%s", c1.InitialQty, c1.RemainingQty)
	}
	if c1.Holder != "Alice" || c1.Location != "Lab A" {
		t.Fatalf("child holder/location = %q/%q, want inherited Alice/Lab A", c1.Holder, c1.Location)
	}
	if c1.ParentID != "S1" {
		t.Fatalf("child parent = %q, want S1", c1.ParentID)
	}
	if len(c1.History) != 1 || c1.History[0].Type != eventAliquotIn || c1.History[0].SampleID != "S1" {
		t.Fatalf("child history = %v", c1.History)
	}

	// 父样历史包含登记 + 两次分出。
	if len(v.History) != 3 {
		t.Fatalf("parent history len = %d, want 3", len(v.History))
	}
	if v.History[0].Type != eventRegistered || v.History[1].Type != eventAliquotOut || v.History[2].Type != eventAliquotOut {
		t.Fatalf("parent history types = %s,%s,%s", v.History[0].Type, v.History[1].Type, v.History[2].Type)
	}
}

func TestAliquotAtomicOnBadQuantity(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "10", "A", "L"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err := s.Aliquot("S1", []Aliquot{
		{SampleID: "S1-1", Quantity: "3"},
		{SampleID: "S1-2", Quantity: "1.2345"}, // 不合法
	})
	if !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("Aliquot error = %v, want ErrInvalidQuantity", err)
	}
	// 整次不生效：余量不变，子样不存在。
	v, _ := s.GetSample("S1")
	if v.RemainingQty != "10.000" {
		t.Fatalf("remaining = %s, want 10.000 (unchanged)", v.RemainingQty)
	}
	for _, id := range []string{"S1-1", "S1-2"} {
		if _, err := s.GetSample(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("child %s should not exist, err = %v", id, err)
		}
	}
}

func TestAliquotAtomicOnDuplicateID(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "10", "A", "L"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := s.Register("S2", "1", "A", "L"); err != nil {
		t.Fatalf("Register S2: %v", err)
	}
	_, err := s.Aliquot("S1", []Aliquot{
		{SampleID: "S1-1", Quantity: "3"},
		{SampleID: "S2", Quantity: "1"}, // 与已有样品重复
	})
	if !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("Aliquot error = %v, want ErrDuplicateID", err)
	}
	v, _ := s.GetSample("S1")
	if v.RemainingQty != "10.000" {
		t.Fatalf("remaining = %s, want 10.000", v.RemainingQty)
	}
	if _, err := s.GetSample("S1-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("S1-1 should not exist, err = %v", err)
	}
}

func TestAliquotExceedsRemaining(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "10", "A", "L"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err := s.Aliquot("S1", []Aliquot{{SampleID: "S1-1", Quantity: "10.001"}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Aliquot error = %v, want ErrConflict", err)
	}
	v, _ := s.GetSample("S1")
	if v.RemainingQty != "10.000" {
		t.Fatalf("remaining = %s, want 10.000", v.RemainingQty)
	}
}

func TestAliquotExactRemaining(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "10", "A", "L"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := s.Aliquot("S1", []Aliquot{{SampleID: "S1-1", Quantity: "10"}}); err != nil {
		t.Fatalf("Aliquot exact: %v", err)
	}
	v, _ := s.GetSample("S1")
	if v.RemainingQty != "0.000" {
		t.Fatalf("remaining = %s, want 0.000", v.RemainingQty)
	}
	// 余量为零不能再分装。
	if _, err := s.Aliquot("S1", []Aliquot{{SampleID: "S1-2", Quantity: "1"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Aliquot on zero remaining error = %v, want ErrConflict", err)
	}
}

func TestAliquotChainAndIndependent(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "10", "A", "L"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := s.Aliquot("S1", []Aliquot{{SampleID: "S1-1", Quantity: "4"}}); err != nil {
		t.Fatalf("Aliquot: %v", err)
	}
	// 子样继续分装。
	if _, err := s.Aliquot("S1-1", []Aliquot{{SampleID: "S1-1-1", Quantity: "1.5"}}); err != nil {
		t.Fatalf("Aliquot child: %v", err)
	}
	c, _ := s.GetSample("S1-1")
	if c.RemainingQty != "2.500" {
		t.Fatalf("child remaining = %s, want 2.500", c.RemainingQty)
	}
	// 父样与子样的交接互不带动：父样发起交接不影响子样。
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	if _, err := s.InitiateHandover(HandoverRequest{
		HandoverID: "H1", SampleID: "S1", Giver: "A", GiverLocation: "L",
		Receiver: "B", Destination: "M", HandoverTime: now,
	}); err != nil {
		t.Fatalf("InitiateHandover: %v", err)
	}
	// 父样有待确认交接，不能再分装；子样没有，可以继续。
	if _, err := s.Aliquot("S1", []Aliquot{{SampleID: "S1-2", Quantity: "1"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("aliquot parent with pending handover error = %v, want ErrConflict", err)
	}
	if _, err := s.Aliquot("S1-1", []Aliquot{{SampleID: "S1-1-2", Quantity: "1"}}); err != nil {
		t.Fatalf("aliquot child should be independent: %v", err)
	}
}

func TestHandoverInitiatePending(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "10", "Alice", "Lab A"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	h, err := s.InitiateHandover(HandoverRequest{
		HandoverID: "H1", SampleID: "S1", Giver: "Alice", GiverLocation: "Lab A",
		Receiver: "Bob", Destination: "Lab B", HandoverTime: now,
	})
	if err != nil {
		t.Fatalf("InitiateHandover: %v", err)
	}
	if h.Confirmed {
		t.Fatal("handover should be pending")
	}
	// 原持有人和地点保持不变。
	v, _ := s.GetSample("S1")
	if v.Holder != "Alice" || v.Location != "Lab A" {
		t.Fatalf("holder/location = %q/%q, should remain Alice/Lab A", v.Holder, v.Location)
	}
	if v.PendingHandover == nil || v.PendingHandover.ID != "H1" {
		t.Fatalf("pending handover = %v, want H1", v.PendingHandover)
	}
	// 不能再次发起交接或分装。
	if _, err := s.InitiateHandover(HandoverRequest{
		HandoverID: "H2", SampleID: "S1", Giver: "Alice", GiverLocation: "Lab A",
		Receiver: "Carol", Destination: "Lab C", HandoverTime: now,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second initiate error = %v, want ErrConflict", err)
	}
	if _, err := s.Aliquot("S1", []Aliquot{{SampleID: "S1-1", Quantity: "1"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("aliquot with pending handover error = %v, want ErrConflict", err)
	}
}

func TestHandoverValidation(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "10", "Alice", "Lab A"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)

	// 交出人与当前持有人不一致。
	if _, err := s.InitiateHandover(HandoverRequest{
		HandoverID: "H1", SampleID: "S1", Giver: "Bob", GiverLocation: "Lab A",
		Receiver: "Carol", Destination: "Lab B", HandoverTime: now,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("giver mismatch error = %v, want ErrConflict", err)
	}
	// 交出地点不一致。
	if _, err := s.InitiateHandover(HandoverRequest{
		HandoverID: "H1", SampleID: "S1", Giver: "Alice", GiverLocation: "Lab B",
		Receiver: "Bob", Destination: "Lab B", HandoverTime: now,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("location mismatch error = %v, want ErrConflict", err)
	}
	// 接收人与交出人相同。
	if _, err := s.InitiateHandover(HandoverRequest{
		HandoverID: "H1", SampleID: "S1", Giver: "Alice", GiverLocation: "Lab A",
		Receiver: "Alice", Destination: "Lab B", HandoverTime: now,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("receiver==giver error = %v, want ErrInvalidInput", err)
	}
	// 不存在的样品。
	if _, err := s.InitiateHandover(HandoverRequest{
		HandoverID: "H1", SampleID: "NOPE", Giver: "A", GiverLocation: "L",
		Receiver: "B", Destination: "M", HandoverTime: now,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing sample error = %v, want ErrNotFound", err)
	}
	_ = later
}

func TestHandoverConfirm(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "10", "Alice", "Lab A"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	if _, err := s.InitiateHandover(HandoverRequest{
		HandoverID: "H1", SampleID: "S1", Giver: "Alice", GiverLocation: "Lab A",
		Receiver: "Bob", Destination: "Lab B", HandoverTime: now,
	}); err != nil {
		t.Fatalf("Initiate: %v", err)
	}

	// 错误接收人。
	if _, err := s.ConfirmHandover("H1", "Carol", "Lab B", now.Add(time.Hour)); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong receiver error = %v, want ErrConflict", err)
	}
	// 错误地点。
	if _, err := s.ConfirmHandover("H1", "Bob", "Lab C", now.Add(time.Hour)); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong destination error = %v, want ErrConflict", err)
	}
	// 接收时间早于交出时间。
	if _, err := s.ConfirmHandover("H1", "Bob", "Lab B", now.Add(-time.Hour)); !errors.Is(err, ErrConflict) {
		t.Fatalf("early receive time error = %v, want ErrConflict", err)
	}
	// 待确认状态下样品记录未变。
	v, _ := s.GetSample("S1")
	if v.Holder != "Alice" || v.Location != "Lab A" || v.PendingHandover == nil {
		t.Fatalf("sample changed before confirm: %+v", v)
	}

	// 正确确认。
	recv := now.Add(2 * time.Hour)
	h, err := s.ConfirmHandover("H1", "Bob", "Lab B", recv)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !h.Confirmed || !h.ReceiveTime.Equal(recv) {
		t.Fatalf("confirmed = %v, receiveTime = %v", h.Confirmed, h.ReceiveTime)
	}
	v, _ = s.GetSample("S1")
	if v.Holder != "Bob" || v.Location != "Lab B" {
		t.Fatalf("holder/location = %q/%q, want Bob/Lab B", v.Holder, v.Location)
	}
	if v.PendingHandover != nil {
		t.Fatalf("pending handover should be cleared")
	}
	// 历史包含发起与确认两条。
	if len(v.History) != 3 {
		t.Fatalf("history len = %d, want 3 (registered+initiated+confirmed)", len(v.History))
	}
	if v.History[1].Type != eventHandoverInitiated || v.History[2].Type != eventHandoverConfirmed {
		t.Fatalf("history types = %s,%s", v.History[1].Type, v.History[2].Type)
	}
	// 确认后待确认解除，可以继续分装。
	if _, err := s.Aliquot("S1", []Aliquot{{SampleID: "S1-1", Quantity: "1"}}); err != nil {
		t.Fatalf("aliquot after confirm: %v", err)
	}
}

func TestHandoverInitiateIdempotent(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "10", "A", "L"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	req := HandoverRequest{
		HandoverID: "H1", SampleID: "S1", Giver: "A", GiverLocation: "L",
		Receiver: "B", Destination: "M", HandoverTime: now,
	}
	h1, err := s.InitiateHandover(req)
	if err != nil {
		t.Fatalf("first initiate: %v", err)
	}
	// 完全相同的请求重复提交，返回原交接。
	h2, err := s.InitiateHandover(req)
	if err != nil {
		t.Fatalf("duplicate initiate: %v", err)
	}
	if h1.ID != h2.ID || !h1.HandoverTime.Equal(h2.HandoverTime) {
		t.Fatalf("duplicate returned different handover: %+v vs %+v", h1, h2)
	}
	// 待确认状态下历史不增加。
	v, _ := s.GetSample("S1")
	if len(v.History) != 2 {
		t.Fatalf("history len = %d, want 2 (no duplicate record)", len(v.History))
	}

	// 确认后重复提交相同请求，仍返回原交接。
	if _, err := s.ConfirmHandover("H1", "B", "M", now.Add(time.Hour)); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	h3, err := s.InitiateHandover(req)
	if err != nil {
		t.Fatalf("duplicate initiate after confirm: %v", err)
	}
	if h3.ID != h1.ID || !h3.Confirmed {
		t.Fatalf("after confirm duplicate = %+v, want same confirmed handover", h3)
	}

	// 沿用编号但改变内容，拒绝。
	changed := req
	changed.Receiver = "C"
	if _, err := s.InitiateHandover(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed content error = %v, want ErrConflict", err)
	}
	changed = req
	changed.SampleID = "S2"
	if _, err := s.InitiateHandover(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed sample error = %v, want ErrConflict", err)
	}
	changed = req
	changed.HandoverTime = now.Add(time.Hour)
	if _, err := s.InitiateHandover(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed time error = %v, want ErrConflict", err)
	}
}

func TestHandoverConfirmIdempotent(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "10", "A", "L"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	if _, err := s.InitiateHandover(HandoverRequest{
		HandoverID: "H1", SampleID: "S1", Giver: "A", GiverLocation: "L",
		Receiver: "B", Destination: "M", HandoverTime: now,
	}); err != nil {
		t.Fatalf("initiate: %v", err)
	}
	recv := now.Add(time.Hour)
	if _, err := s.ConfirmHandover("H1", "B", "M", recv); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	v, _ := s.GetSample("S1")
	histLen := len(v.History)

	// 相同接收信息重复确认，返回原结果，不增加转手记录。
	h, err := s.ConfirmHandover("H1", "B", "M", recv)
	if err != nil {
		t.Fatalf("duplicate confirm: %v", err)
	}
	if !h.Confirmed || !h.ReceiveTime.Equal(recv) {
		t.Fatalf("duplicate confirm result = %+v", h)
	}
	v, _ = s.GetSample("S1")
	if len(v.History) != histLen {
		t.Fatalf("history len = %d, want %d (no new record)", len(v.History), histLen)
	}

	// 接收信息不同，拒绝。
	if _, err := s.ConfirmHandover("H1", "C", "M", recv); !errors.Is(err, ErrConflict) {
		t.Fatalf("different receiver error = %v, want ErrConflict", err)
	}
	if _, err := s.ConfirmHandover("H1", "B", "N", recv); !errors.Is(err, ErrConflict) {
		t.Fatalf("different destination error = %v, want ErrConflict", err)
	}
	if _, err := s.ConfirmHandover("H1", "B", "M", recv.Add(time.Hour)); !errors.Is(err, ErrConflict) {
		t.Fatalf("different receive time error = %v, want ErrConflict", err)
	}
	// 拒绝后持有人和地点不变。
	v, _ = s.GetSample("S1")
	if v.Holder != "B" || v.Location != "M" {
		t.Fatalf("holder/location = %q/%q, should stay B/M", v.Holder, v.Location)
	}
}

func TestGetHandoverNotFound(t *testing.T) {
	s := newStore(t)
	if _, err := s.GetHandover("NOPE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetHandover error = %v, want ErrNotFound", err)
	}
}

func TestPersistenceReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custody.json")
	s, err := Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Register("S1", "10", "Alice", "Lab A"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := s.Aliquot("S1", []Aliquot{{SampleID: "S1-1", Quantity: "3.5"}}); err != nil {
		t.Fatalf("Aliquot: %v", err)
	}
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	if _, err := s.InitiateHandover(HandoverRequest{
		HandoverID: "H1", SampleID: "S1-1", Giver: "Alice", GiverLocation: "Lab A",
		Receiver: "Bob", Destination: "Lab B", HandoverTime: now,
	}); err != nil {
		t.Fatalf("Initiate: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s2.Close()

	// 记录仍在。
	v, err := s2.GetSample("S1")
	if err != nil {
		t.Fatalf("GetSample after reopen: %v", err)
	}
	if v.RemainingQty != "6.500" || len(v.Children) != 1 {
		t.Fatalf("after reopen S1 = %s, children %v", v.RemainingQty, v.Children)
	}
	c, err := s2.GetSample("S1-1")
	if err != nil {
		t.Fatalf("GetSample child after reopen: %v", err)
	}
	if c.PendingHandover == nil || c.PendingHandover.ID != "H1" {
		t.Fatalf("pending handover after reopen = %v", c.PendingHandover)
	}
	// 交接编号重复提交判断仍然有效。
	if _, err := s2.InitiateHandover(HandoverRequest{
		HandoverID: "H1", SampleID: "S1-1", Giver: "Alice", GiverLocation: "Lab A",
		Receiver: "Bob", Destination: "Lab B", HandoverTime: now,
	}); err != nil {
		t.Fatalf("idempotent initiate after reopen: %v", err)
	}
	if _, err := s2.InitiateHandover(HandoverRequest{
		HandoverID: "H1", SampleID: "S1-1", Giver: "Alice", GiverLocation: "Lab A",
		Receiver: "Carol", Destination: "Lab B", HandoverTime: now,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed content after reopen error = %v, want ErrConflict", err)
	}
	// 确认后状态正确。
	if _, err := s2.ConfirmHandover("H1", "Bob", "Lab B", now.Add(time.Hour)); err != nil {
		t.Fatalf("confirm after reopen: %v", err)
	}
	c, _ = s2.GetSample("S1-1")
	if c.Holder != "Bob" || c.Location != "Lab B" || c.PendingHandover != nil {
		t.Fatalf("after confirm holder/location/pending = %q/%q/%v", c.Holder, c.Location, c.PendingHandover)
	}
}

func TestOpenMissingFile(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open missing error = %v, want ErrNotFound", err)
	}
}

func TestCreateExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custody.json")
	s, err := Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	s.Close()
	if _, err := Create(path); !errors.Is(err, ErrConflict) {
		t.Fatalf("Create existing error = %v, want ErrConflict", err)
	}
}

func TestConcurrentAliquot(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "100", "A", "L"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	const n = 50
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.Aliquot("S1", []Aliquot{{
				SampleID: fmt.Sprintf("S1-%d", i),
				Quantity: "1",
			}})
		}(i)
	}
	wg.Wait()

	success := 0
	for _, err := range errs {
		if err == nil {
			success++
		}
	}
	v, _ := s.GetSample("S1")
	// 剩余量 = 100 - success，且每个成功子样 1.000。
	if v.RemainingQty != fmt.Sprintf("%d.000", 100-success) {
		t.Fatalf("remaining = %s, want %d.000 (success=%d)", v.RemainingQty, 100-success, success)
	}
	var total int64
	for i := 0; i < n; i++ {
		c, err := s.GetSample(fmt.Sprintf("S1-%d", i))
		if err == nil {
			q, err := parseQuantity(c.InitialQty)
			if err != nil {
				t.Fatalf("parse qty %q: %v", c.InitialQty, err)
			}
			total += q
		}
	}
	if total != int64(success)*1000 {
		t.Fatalf("children total = %d, want %d", total, int64(success)*1000)
	}
}

func TestConcurrentInitiateOnePending(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "10", "A", "L"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.InitiateHandover(HandoverRequest{
				HandoverID: fmt.Sprintf("H-%d", i),
				SampleID:   "S1", Giver: "A", GiverLocation: "L",
				Receiver: "B", Destination: "M", HandoverTime: now,
			})
		}(i)
	}
	wg.Wait()

	success := 0
	for _, err := range errs {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("successful initiates = %d, want exactly 1", success)
	}
	v, _ := s.GetSample("S1")
	if v.PendingHandover == nil {
		t.Fatal("expected exactly one pending handover")
	}
}

func TestConcurrentConfirmIdempotent(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "10", "A", "L"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	if _, err := s.InitiateHandover(HandoverRequest{
		HandoverID: "H1", SampleID: "S1", Giver: "A", GiverLocation: "L",
		Receiver: "B", Destination: "M", HandoverTime: now,
	}); err != nil {
		t.Fatalf("initiate: %v", err)
	}
	recv := now.Add(time.Hour)
	const n = 20
	var wg sync.WaitGroup
	results := make([]*HandoverView, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = s.ConfirmHandover("H1", "B", "M", recv)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("confirm %d: %v", i, errs[i])
		}
		if !results[i].Confirmed || !results[i].ReceiveTime.Equal(recv) {
			t.Fatalf("confirm %d result = %+v", i, results[i])
		}
	}
	// 只增加一条确认记录。
	v, _ := s.GetSample("S1")
	confirmed := 0
	for _, e := range v.History {
		if e.Type != eventHandoverConfirmed {
			continue
		}
		confirmed++
	}
	if confirmed != 1 {
		t.Fatalf("confirmed events = %d, want 1", confirmed)
	}
}

func TestHistoryOrderAcrossOperations(t *testing.T) {
	s := newStore(t)
	if _, err := s.Register("S1", "10", "A", "L"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := s.Aliquot("S1", []Aliquot{{SampleID: "S1-1", Quantity: "2"}}); err != nil {
		t.Fatalf("Aliquot: %v", err)
	}
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	if _, err := s.InitiateHandover(HandoverRequest{
		HandoverID: "H1", SampleID: "S1", Giver: "A", GiverLocation: "L",
		Receiver: "B", Destination: "M", HandoverTime: now,
	}); err != nil {
		t.Fatalf("Initiate: %v", err)
	}
	if _, err := s.ConfirmHandover("H1", "B", "M", now.Add(time.Hour)); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if _, err := s.Aliquot("S1", []Aliquot{{SampleID: "S1-2", Quantity: "1"}}); err != nil {
		t.Fatalf("Aliquot after confirm: %v", err)
	}
	v, _ := s.GetSample("S1")
	want := []string{eventRegistered, eventAliquotOut, eventHandoverInitiated, eventHandoverConfirmed, eventAliquotOut}
	if len(v.History) != len(want) {
		t.Fatalf("history len = %d, want %d", len(v.History), len(want))
	}
	for i, w := range want {
		if v.History[i].Type != w {
			t.Fatalf("history[%d] = %s, want %s", i, v.History[i].Type, w)
		}
		if i > 0 && v.History[i].Seq <= v.History[i-1].Seq {
			t.Fatalf("history seq not increasing at %d", i)
		}
	}
}
