package custody

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// 销毁操作人/地点恢复核对相关测试。
//
// 规则：带销毁信息的样品恢复时，销毁操作人必须与样品保留的最后持有人
// 完全一致，销毁地点必须与样品保留的当前地点完全一致。任意一项不一致都
// 足以拒绝整份文件；操作人、地点缺失、为空或只有空白同样无效，不能因为
// 样品对应字段也为空就当作一致；两边都有值时按原文逐字核对，不去掉空白。
// 核对只针对同一编号样品自身的最后记录，父子样品、早期历史中的人员地点
// 差异都不是拒绝理由。

// custodyScenario 构造任务示例：原样 S-001 初始 10.000，直接分出
// 3.250 子样后销毁剩余 6.750（数量守恒、时间合法），样品保留的持有人是
// 李四、地点是实验室B。销毁信息中的操作人、地点可被调用方改写。
func custodyScenario(operator, location string) *ledger {
	parent := destroyedSample("S-001", 10000, 0, 6750, "S-001-A")
	parent.Destroyed.Operator = operator
	parent.Destroyed.Location = location
	return &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":   parent,
			"S-001-A": plainChild("S-001-A", "S-001", 3250, 3250),
		},
	}
}

// TestOpenRejectsDestroyedOperatorAndLocationMismatch 任务示例：销毁信息写成
// 王五在实验室C操作，与样品保留的最后持有人李四、地点实验室B互相矛盾，
// 即使数量与时间等其余检查均合法也必须拒绝打开。先报操作人，给出两边的值。
func TestOpenRejectsDestroyedOperatorAndLocationMismatch(t *testing.T) {
	path := writeStructLedger(t, custodyScenario("王五", "实验室C"))
	mustRejectOpen(t, path, "S-001", "操作人", "王五", "李四")
}

// TestOpenRejectsDestroyedOperatorMismatchOnly 操作人单项不一致、地点一致，
// 也足以拒绝；错误指出有问题的是操作人并给出两边各自的值。
func TestOpenRejectsDestroyedOperatorMismatchOnly(t *testing.T) {
	path := writeStructLedger(t, custodyScenario("王五", "实验室B"))
	mustRejectOpen(t, path, "S-001", "操作人", "王五", "李四")
}

// TestOpenRejectsDestroyedLocationMismatchOnly 地点单项不一致、操作人一致，
// 也足以拒绝；错误指出有问题的是销毁地点并给出两边各自的值。
func TestOpenRejectsDestroyedLocationMismatchOnly(t *testing.T) {
	path := writeStructLedger(t, custodyScenario("李四", "实验室C"))
	mustRejectOpen(t, path, "S-001", "销毁地点", "实验室C", "实验室B")
}

// TestOpenRejectsBlankDestroyedOperator 销毁操作人缺失、为空或只有空白均为
// 无效数据；样品持有人同为空也不能把它们视为一致。
func TestOpenRejectsBlankDestroyedOperator(t *testing.T) {
	// 结构构造：空串与纯空白。
	for name, operator := range map[string]string{
		"操作人为空串":  "",
		"操作人只有空白": "   ",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeStructLedger(t, custodyScenario(operator, "实验室B"))
			mustRejectOpen(t, path, "S-001", "操作人")
		})
	}

	// 原始文件：根本没有 operator 字段。
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {"NOOP": {"id":"NOOP","initial":5000,"remaining":0,"holder":"李四","location":"实验室B","children":[],"history":[
			{"kind":"register","time":"2026-10-01T09:00:00Z","holder":"李四","location":"实验室B"},
			{"kind":"destroy","time":"2026-10-03T12:00:00Z","holder":"李四","location":"实验室B"}
		],"destroyed":{"location":"实验室B","at":"2026-10-03T12:00:00Z","reason":"实验结束按规程销毁","qty":5000}}},
		"transfers": {}
	}`)
	mustRejectOpen(t, path, "NOOP", "缺少操作人")

	// 销毁操作人与样品持有人同时为空：仍属无效，不能视为一致。
	bothEmpty := writeRawLedger(t, `{
		"version": 1,
		"samples": {"BOTH": {"id":"BOTH","initial":5000,"remaining":0,"holder":"","location":"实验室B","children":[],"history":[
			{"kind":"register","time":"2026-10-01T09:00:00Z","holder":"","location":"实验室B"},
			{"kind":"destroy","time":"2026-10-03T12:00:00Z","holder":"","location":"实验室B"}
		],"destroyed":{"operator":"","location":"实验室B","at":"2026-10-03T12:00:00Z","reason":"实验结束按规程销毁","qty":5000}}},
		"transfers": {}
	}`)
	mustRejectOpen(t, bothEmpty, "BOTH", "缺少操作人")
}

// TestOpenRejectsBlankDestroyedLocation 销毁地点缺失、为空或只有空白均为
// 无效数据；样品地点同为空白也不能把它们视为一致。
func TestOpenRejectsBlankDestroyedLocation(t *testing.T) {
	// 操作人保持一致，地点为空串或纯空白。
	for name, location := range map[string]string{
		"地点为空串":  "",
		"地点只有空白": " \t ",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeStructLedger(t, custodyScenario("李四", location))
			mustRejectOpen(t, path, "S-001", "销毁地点")
		})
	}

	// 原始文件：没有 location 字段。
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {"NOLOC": {"id":"NOLOC","initial":5000,"remaining":0,"holder":"李四","location":"实验室B","children":[],"history":[
			{"kind":"register","time":"2026-10-01T09:00:00Z","holder":"李四","location":"实验室B"},
			{"kind":"destroy","time":"2026-10-03T12:00:00Z","holder":"李四","location":"实验室B"}
		],"destroyed":{"operator":"李四","at":"2026-10-03T12:00:00Z","reason":"实验结束按规程销毁","qty":5000}}},
		"transfers": {}
	}`)
	mustRejectOpen(t, path, "NOLOC", "销毁地点")

	// 销毁地点与样品当前地点同时只有空白：仍属无效，不能视为一致。
	bothBlank := writeRawLedger(t, `{
		"version": 1,
		"samples": {"BOTH": {"id":"BOTH","initial":5000,"remaining":0,"holder":"李四","location":"  ","children":[],"history":[
			{"kind":"register","time":"2026-10-01T09:00:00Z","holder":"李四","location":"  "},
			{"kind":"destroy","time":"2026-10-03T12:00:00Z","holder":"李四","location":"  "}
		],"destroyed":{"operator":"李四","location":"  ","at":"2026-10-03T12:00:00Z","reason":"实验结束按规程销毁","qty":5000}}},
		"transfers": {}
	}`)
	mustRejectOpen(t, bothBlank, "BOTH", "销毁地点")
}

// TestOpenRejectsDestroyedOperatorWithoutTrimming 两边都有非空文本时按原文
// 核对：" 李四 " 与 "李四" 不能通过去掉首尾空白被接受，错误给出两边原值。
func TestOpenRejectsDestroyedOperatorWithoutTrimming(t *testing.T) {
	l := custodyScenario(" 李四 ", "实验室B")
	path := writeStructLedger(t, l)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err == nil {
		t.Fatalf("带空白的操作人与持有人原文不同，不应靠去掉空白接受")
	}
	if s != nil {
		t.Fatalf("打开失败时不应返回可用的 Store")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("错误应包装 ErrInvalid, got %v", err)
	}
	if !strings.Contains(err.Error(), `" 李四 "`) || !strings.Contains(err.Error(), `"李四"`) {
		t.Fatalf("错误应给出销毁操作人与持有人各自的原文: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("打开失败不得改写原文件")
	}
}

// TestOpenRejectsDestroyedCustodyDespiteOtherValidSamples 文件中另有完全正常
// 的样品时，一处人员/地点矛盾也使整份文件打开失败：不返回 Store、不只加载
// 正常部分，原文件保持原样（不补填替换、不删除销毁信息改成未销毁）。
func TestOpenRejectsDestroyedCustodyDespiteOtherValidSamples(t *testing.T) {
	l := custodyScenario("王五", "实验室C")
	l.Samples["OK"] = destroyedSample("OK", 1000, 0, 1000)
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "S-001", "王五")

	raw := readFileString(t, path)
	if !strings.Contains(raw, `"destroyed"`) || !strings.Contains(raw, "王五") {
		t.Fatalf("打开失败不得删除销毁信息或替换操作人: %s", raw)
	}
}

// TestOpenDestroyedCustodyCheckedPerSample 核对只针对同一编号样品自身的最后
// 记录：父样与子样由不同人员在不同地点分别保管/销毁合法；子样自身销毁
// 操作人与它自己的最后持有人不符时，只为子样拒绝整份文件。
func TestOpenDestroyedCustodyCheckedPerSample(t *testing.T) {
	// 父样李四在实验室B销毁；子样已转到王五在实验室C，并由王五在实验室C
	// 销毁——父子销毁信息互不影响，文件正常恢复。
	parent := destroyedSample("P", 10000, 0, 7000, "C")
	child := destroyedSampleWithParent("C", "P", 3000, 3000)
	child.Holder, child.Location = "王五", "实验室C"
	child.Destroyed.Operator, child.Destroyed.Location = "王五", "实验室C"
	good := &ledger{Samples: map[string]*sampleRecord{"P": parent, "C": child}}
	s, err := Open(writeStructLedger(t, good))
	if err != nil {
		t.Fatalf("父子由不同人员在不同地点分别销毁应正常恢复: %v", err)
	}
	p, _ := s.GetSample("P")
	c, _ := s.GetSample("C")
	if p.Destruction.Operator != "李四" || p.Destruction.Location != "实验室B" {
		t.Fatalf("父样销毁详情应原样保留: %+v", p.Destruction)
	}
	if p.Holder != "李四" || p.Location != "实验室B" {
		t.Fatalf("父样最后持有人/地点应保留: %+v", p)
	}
	if c.Destruction.Operator != "王五" || c.Destruction.Location != "实验室C" ||
		c.Holder != "王五" || c.Location != "实验室C" {
		t.Fatalf("子样销毁详情与最后持有人/地点应保留: %+v", c)
	}

	// 子样销毁操作人与其自身最后持有人不符：即使父样记录完全正常，也必须
	// 拒绝，错误指出有问题的是子样 C 及其操作人，不能拿父样的李四凑一致。
	badChild := destroyedSampleWithParent("C2", "P2", 3000, 3000)
	badChild.Holder, badChild.Location = "王五", "实验室C"
	badChild.Destroyed.Operator, badChild.Destroyed.Location = "赵六", "实验室C"
	badParent := destroyedSample("P2", 10000, 0, 7000, "C2")
	bad := &ledger{Samples: map[string]*sampleRecord{"P2": badParent, "C2": badChild}}
	mustRejectOpen(t, writeStructLedger(t, bad), "C2", "操作人", "赵六", "王五")
}

// TestOpenDestroyedCustodyIgnoresEarlierHistory 早期登记、交接历史中的人员和
// 地点可以与最后记录不同：样品当前持有人李四/实验室B、销毁操作人李四/
// 实验室B 时，历史里的张三/实验室A 不构成拒绝理由。
func TestOpenDestroyedCustodyIgnoresEarlierHistory(t *testing.T) {
	rec := destroyedSample("S-001", 10000, 0, 6750, "S-001-A")
	rec.History = []historyRecord{
		{Kind: "register", Time: registerCheckAt, Holder: "张三", Location: "实验室A"},
		{Kind: "transfer-in", Time: registerCheckAt.Add(time.Hour), Holder: "李四", Location: "实验室B"},
		{Kind: "split", Time: splitCheckAt, Holder: "李四", Location: "实验室B"},
		{Kind: "destroy", Time: destroyedCheckAt, Holder: "李四", Location: "实验室B"},
	}
	l := &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":   rec,
			"S-001-A": plainChild("S-001-A", "S-001", 3250, 3250),
		},
	}
	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("早期历史的人员/地点与最后记录不同不应成为拒绝理由: %v", err)
	}
	got, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Destruction == nil || got.Destruction.Operator != "李四" ||
		got.Destruction.Location != "实验室B" || got.Destruction.Qty != "6.750" {
		t.Fatalf("销毁详情应原样保留: %+v", got.Destruction)
	}
	if got.Holder != "李四" || got.Location != "实验室B" {
		t.Fatalf("最后持有人/地点应原样保留: %+v", got)
	}
}

// TestOpenRestoresConsistentDestroyedCustody 信息一致且通过原有数量、时间
// 检查的记录正常打开，查询仍保留原销毁详情和最后持有人、地点。
func TestOpenRestoresConsistentDestroyedCustody(t *testing.T) {
	path := writeStructLedger(t, custodyScenario("李四", "实验室B"))
	s, err := Open(path)
	if err != nil {
		t.Fatalf("销毁操作人/地点与最后持有人/地点一致应正常恢复: %v", err)
	}
	got, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Destruction == nil {
		t.Fatalf("销毁详情应保留")
	}
	if got.Destruction.Operator != "李四" || got.Destruction.Location != "实验室B" ||
		got.Destruction.Qty != "6.750" {
		t.Fatalf("销毁详情应按原文保留: %+v", got.Destruction)
	}
	if got.Holder != "李四" || got.Location != "实验室B" || got.Remaining != "0.000" {
		t.Fatalf("最后持有人/地点与数量应按原文保留: %+v", got)
	}
}
