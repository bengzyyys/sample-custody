package custody

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 数量上限相关常量：内部以千分之一毫升的 int64 保存，最大可接受数量
// 9223372036854775.807 毫升恰好是 math.MaxInt64 个最小刻度。
const (
	maxQtyString   = "9223372036854775.807" // 上限本身，登记应成功
	belowMaxString = "9223372036854775.806" // 比上限少 0.001
	twoBelowString = "9223372036854775.805" // 比上限少 0.002
	threeBelowStr  = "9223372036854775.804" // 比上限少 0.003
)

// openBoundaryStore 打开一个使用确定时钟的临时数据，并返回其落盘路径，
// 便于在大数量分装后重新打开校验持久化结果。
func openBoundaryStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.now = func() time.Time { return time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC) }
	return s, path
}

// TestSplitAtMaximumQuantityStaysPrecise 回归大数量下的精确分装：
// 以上限 9223372036854775.807 毫升登记原样，先分出仅少 0.001 的子样，
// 再分出最后的 0.001；整个过程不能因为数值很大而丢掉最后一位小数。
func TestSplitAtMaximumQuantityStaysPrecise(t *testing.T) {
	s, path := openBoundaryStore(t)

	const (
		parentID = "P"
		firstID  = "C1"
		lastID   = "C2"
	)

	// 以当前可接受的最大数量登记原样，应成功且查询逐位一致。
	reg := mustRegister(t, s, parentID, maxQtyString, " 张三 ", " 实验室A ")
	if reg.InitialQty != maxQtyString || reg.Remaining != maxQtyString {
		t.Fatalf("上限数量登记后显示不一致: init=%s remaining=%s want %s",
			reg.InitialQty, reg.Remaining, maxQtyString)
	}
	if reg.Holder != "张三" || reg.Location != "实验室A" {
		t.Fatalf("持有人地点应去空白: %+v", reg)
	}

	// 分出 9223372036854775.806：来源必须恰好剩余 0.001，初始量保持登记值。
	parent, err := s.Split(SplitInput{ParentID: parentID, Parts: []SplitPart{
		{ID: firstID, Qty: belowMaxString},
	}})
	if err != nil {
		t.Fatalf("接近上限的大数量分装应成功: %v", err)
	}
	if parent.Remaining != "0.001" {
		t.Fatalf("分出 %s 后来源应恰好剩余 0.001, got %s", belowMaxString, parent.Remaining)
	}
	if parent.InitialQty != maxQtyString {
		t.Fatalf("分装不得改变来源初始量: got %s want %s", parent.InitialQty, maxQtyString)
	}
	if len(parent.Children) != 1 || parent.Children[0] != firstID {
		t.Fatalf("来源子样列表应按分装顺序记录: %v", parent.Children)
	}

	// 子样的初始量和剩余量都必须与请求逐位一致，不能丢掉最后一位小数，
	// 来源关系、持有人和地点与分装时一致。
	first, err := s.GetSample(firstID)
	if err != nil {
		t.Fatalf("大数量子样应可查询: %v", err)
	}
	if first.InitialQty != belowMaxString || first.Remaining != belowMaxString {
		t.Fatalf("大数量子样数量不精确: init=%s remaining=%s want %s",
			first.InitialQty, first.Remaining, belowMaxString)
	}
	if first.ParentID != parentID || first.Holder != "张三" || first.Location != "实验室A" {
		t.Fatalf("大数量子样继承关系错误: %+v", first)
	}
	if len(first.History) != 1 || first.History[0].Kind != "split" {
		t.Fatalf("大数量子样保管历史错误: %+v", first.History)
	}

	// 对同一来源接着分出最后 0.001：剩余量准确变为 0.000，初始量仍为登记值。
	parent, err = s.Split(SplitInput{ParentID: parentID, Parts: []SplitPart{
		{ID: lastID, Qty: "0.001"},
	}})
	if err != nil {
		t.Fatalf("分出最后 0.001 应成功: %v", err)
	}
	if parent.Remaining != "0.000" {
		t.Fatalf("分出最后 0.001 后剩余应为 0.000, got %s", parent.Remaining)
	}
	if parent.InitialQty != maxQtyString {
		t.Fatalf("用尽后来源初始量仍应保留登记值: got %s want %s",
			parent.InitialQty, maxQtyString)
	}
	// 分装用尽而非销毁：不应出现销毁信息。
	if parent.Destruction != nil {
		t.Fatalf("分装用尽不应带销毁记录: %+v", parent.Destruction)
	}
	if got := parent.Children; len(got) != 2 || got[0] != firstID || got[1] != lastID {
		t.Fatalf("子样应按分装顺序记录编号: %v", got)
	}
	if len(parent.History) != 3 {
		t.Fatalf("来源历史应为 登记/分装/分装 三条, got %d", len(parent.History))
	}
	for i, wantKind := range []string{"register", "split", "split"} {
		if parent.History[i].Kind != wantKind {
			t.Fatalf("来源历史顺序错误: %+v", parent.History)
		}
	}

	// 最后一个子样保留实际分出量，来源关系与持有人地点同样一致。
	last, err := s.GetSample(lastID)
	if err != nil {
		t.Fatalf("最后子样应可查询: %v", err)
	}
	if last.InitialQty != "0.001" || last.Remaining != "0.001" {
		t.Fatalf("最后子样数量错误: init=%s remaining=%s", last.InitialQty, last.Remaining)
	}
	if last.ParentID != parentID || last.Holder != "张三" || last.Location != "实验室A" {
		t.Fatalf("最后子样继承关系错误: %+v", last)
	}

	// 大整数数量落盘后重新打开仍逐位精确，子样顺序与来源关系不丢。
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rp, err := reopened.GetSample(parentID)
	if err != nil {
		t.Fatal(err)
	}
	if rp.InitialQty != maxQtyString || rp.Remaining != "0.000" ||
		rp.Destruction != nil ||
		len(rp.Children) != 2 || rp.Children[0] != firstID || rp.Children[1] != lastID {
		t.Fatalf("重开后来源状态不精确: %+v", rp)
	}
	rc1, err := reopened.GetSample(firstID)
	if err != nil {
		t.Fatal(err)
	}
	if rc1.InitialQty != belowMaxString || rc1.Remaining != belowMaxString ||
		rc1.ParentID != parentID || rc1.Holder != "张三" || rc1.Location != "实验室A" {
		t.Fatalf("重开后大数量子样状态不精确: %+v", rc1)
	}
	rc2, err := reopened.GetSample(lastID)
	if err != nil {
		t.Fatal(err)
	}
	if rc2.InitialQty != "0.001" || rc2.Remaining != "0.001" || rc2.ParentID != parentID {
		t.Fatalf("重开后最后子样状态错误: %+v", rc2)
	}
}

// TestSplitRejectedWhenTotalOutsideLargeBounds 回归整次分装在大数量下的
// 合计拒绝规则：各子样数量分别合法，但合计超出可表示范围时返回 ErrInvalid；
// 合计仍在支持范围内、仅比来源剩余量多 0.001 时返回 ErrConflict。
// 两类拒绝都必须完全不生效。
func TestSplitRejectedWhenTotalOutsideLargeBounds(t *testing.T) {
	s, _ := openBoundaryStore(t)

	// --- 场景一：合计超过 9223372036854775.807 的支持上限 ---------------
	// 两个子样数量分别合法（一个取上限本身，一个取最小合法量），但合计
	// 超过上限一个最小刻度，int64 累加会回绕；必须判为 ErrInvalid，
	// 不能把合计误判成一个更小的合法数量而放行。
	mustRegister(t, s, "BIG", maxQtyString, "张三", "实验室A")
	bigBefore, err := s.GetSample("BIG")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Split(SplitInput{ParentID: "BIG", Parts: []SplitPart{
		{ID: "OVF1", Qty: maxQtyString}, // 上限本身，单独合法
		{ID: "OVF2", Qty: "0.001"},      // 最小合法量，合计 = 上限 + 0.001
	}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("合计超出支持范围应返回 ErrInvalid, got %v", err)
	}
	if errors.Is(err, ErrConflict) {
		t.Fatalf("超出表示范围不能按普通超量处理为 ErrConflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "超出可表示范围") {
		t.Fatalf("错误内容应能看出是合计超出表示范围, got %v", err)
	}

	// 被拒绝后来源数量、子样编号、保管历史、持有人地点与待确认状态全部不变。
	bigAfter, err := s.GetSample("BIG")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bigAfter, bigBefore) {
		t.Fatalf("被拒绝分装改动了来源状态:\n before=%+v\n after =%+v", bigBefore, bigAfter)
	}
	if bigAfter.Remaining != maxQtyString || bigAfter.InitialQty != maxQtyString {
		t.Fatalf("被拒绝分装不得扣减来源数量: init=%s remaining=%s",
			bigAfter.InitialQty, bigAfter.Remaining)
	}
	if len(bigAfter.Children) != 0 || len(bigAfter.History) != 1 {
		t.Fatalf("被拒绝分装不得追加子样编号或保管历史: %+v", bigAfter)
	}
	if bigAfter.Holder != "张三" || bigAfter.Location != "实验室A" ||
		bigAfter.PendingTransfer != nil || bigAfter.Destruction != nil {
		t.Fatalf("被拒绝分装不得改变持有人、地点及待确认状态: %+v", bigAfter)
	}
	for _, id := range []string{"OVF1", "OVF2"} {
		if got, err := s.GetSample(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("被拒绝批次的子样 %s 应查不到, got view=%+v err=%v", id, got, err)
		}
	}

	// --- 场景二：合计仍在支持范围内，但比来源剩余量多 0.001 -------------
	// 来源初始为上限 - 0.001，先成功分出 0.001（来源剩上限 - 0.002），
	// 制造拒绝前已存在的子样和保管历史。
	mustRegister(t, s, "NEAR", belowMaxString, "张三", "实验室A")
	if _, err := s.Split(SplitInput{ParentID: "NEAR", Parts: []SplitPart{
		{ID: "E0", Qty: "0.001"},
	}}); err != nil {
		t.Fatalf("前置小分装应成功: %v", err)
	}
	nearBefore, err := s.GetSample("NEAR")
	if err != nil {
		t.Fatal(err)
	}
	if nearBefore.Remaining != twoBelowString {
		t.Fatalf("前置分装后来源应剩余 %s, got %s", twoBelowString, nearBefore.Remaining)
	}

	// (上限 - 0.003) + 0.002 = 上限 - 0.001，合计未超支持范围，
	// 但比来源剩余量（上限 - 0.002）恰好多 0.001。
	_, err = s.Split(SplitInput{ParentID: "NEAR", Parts: []SplitPart{
		{ID: "OVR1", Qty: threeBelowStr}, // 上限 - 0.003，单独合法
		{ID: "OVR2", Qty: "0.002"},       // 合计 上限 - 0.001 <= 上限
	}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("合计超过来源剩余量应返回 ErrConflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "超过") || !strings.Contains(err.Error(), "剩余量") {
		t.Fatalf("错误内容应能看出是合计超过来源剩余量, got %v", err)
	}

	// 来源数量、子样列表与历史保持在拒绝前；待确认/销毁状态不变。
	nearAfter, err := s.GetSample("NEAR")
	if err != nil {
		t.Fatal(err)
	}
	if nearAfter.InitialQty != belowMaxString || nearAfter.Remaining != twoBelowString {
		t.Fatalf("被拒绝分装不得扣减来源数量: init=%s remaining=%s",
			nearAfter.InitialQty, nearAfter.Remaining)
	}
	if len(nearAfter.Children) != 1 || nearAfter.Children[0] != "E0" {
		t.Fatalf("拒绝前已存在的子样列表应原样保留: %v", nearAfter.Children)
	}
	if len(nearAfter.History) != 2 ||
		nearAfter.History[0].Kind != "register" || nearAfter.History[1].Kind != "split" {
		t.Fatalf("拒绝前已存在的保管历史应继续保留: %+v", nearAfter.History)
	}
	if nearAfter.Holder != "张三" || nearAfter.Location != "实验室A" ||
		nearAfter.PendingTransfer != nil || nearAfter.Destruction != nil {
		t.Fatalf("被拒绝分装不得改变持有人、地点及待确认状态: %+v", nearAfter)
	}

	// 拒绝前已经存在的子样继续保留且数量不变；本次拟创建的子样仍查不到。
	e0, err := s.GetSample("E0")
	if err != nil {
		t.Fatalf("拒绝前已存在的子样应继续可查: %v", err)
	}
	if e0.InitialQty != "0.001" || e0.Remaining != "0.001" || e0.ParentID != "NEAR" {
		t.Fatalf("拒绝前已存在的子样状态被改动: %+v", e0)
	}
	for _, id := range []string{"OVR1", "OVR2"} {
		if got, err := s.GetSample(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("被拒绝批次的子样 %s 应查不到, got view=%+v err=%v", id, got, err)
		}
	}
}
