package custody

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 销毁操作人、销毁地点与样品最后持有人、地点的一致性恢复核对。
//
// 规则：带销毁信息的样品重新打开时，销毁操作人必须逐字等于样品保留的
// 最后持有人、销毁地点必须逐字等于最后地点；任一项不一致（含销毁一侧
// 缺失、为空或只有空白）都返回包装了 ErrInvalid 的错误，整份文件打开
// 失败、不返回 Store、原文件保持原样。核对只针对同一编号样品自身的最后
// 记录：父子样可由不同人员在不同地点分别保管或销毁，早期历史中的人员、
// 地点与最后记录不同不是拒绝理由。

// custodyMismatchLedger 构造任务示例的基础文件：原样 S-001 初始
// 10.000，直接分出 3.250 子样 S-001-A，实际销毁 6.750、剩余 0.000，
// 数量与时间全部合法。样品保留的持有人、地点固定为李四、实验室B，
// 销毁信息中的操作人、地点可单独注入。
func custodyMismatchLedger(operator, location string) *ledger {
	s := destroyedSample("S-001", 10000, 0, 6750, "S-001-A")
	// destroyedSample 默认两边都是李四/实验室B，这里只改销毁一侧，
	// 样品最后持有人、地点保持李四、实验室B。
	s.Destroyed.Operator = operator
	s.Destroyed.Location = location
	return &ledger{
		Samples: map[string]*sampleRecord{
			"S-001":   s,
			"S-001-A": plainChild("S-001-A", "S-001", 3250, 3250),
		},
	}
}

// TestOpenRejectsDestroyedOperatorAndLocationMismatch 任务示例：销毁数量
// 6.750、剩余 0.000、时间等其余检查均合法，但销毁信息写成王五在实验室C
// 操作，样品保留的最后持有人是李四、地点是实验室B，必须拒绝打开。人与
// 地点同时矛盾时先报操作人，给出销毁信息与样品记录两边的操作人值；地点
// 矛盾的两边值由 TestOpenRejectsDestroyedLocationMismatchOnly 覆盖。
func TestOpenRejectsDestroyedOperatorAndLocationMismatch(t *testing.T) {
	path := writeStructLedger(t, custodyMismatchLedger("王五", "实验室C"))
	mustRejectOpen(t, path, "S-001", "销毁操作人", "王五", "李四")
}

// TestOpenRejectsDestroyedOperatorMismatchOnly 仅操作人不一致也足以拒绝，
// 错误指出问题出在操作人。
func TestOpenRejectsDestroyedOperatorMismatchOnly(t *testing.T) {
	path := writeStructLedger(t, custodyMismatchLedger("王五", "实验室B"))
	mustRejectOpen(t, path, "S-001", "操作人", "王五", "李四")
}

// TestOpenRejectsDestroyedLocationMismatchOnly 仅地点不一致也足以拒绝，
// 错误指出问题出在地点，并给出两边地点。
func TestOpenRejectsDestroyedLocationMismatchOnly(t *testing.T) {
	path := writeStructLedger(t, custodyMismatchLedger("李四", "实验室C"))
	mustRejectOpen(t, path, "S-001", "销毁地点", "实验室C", "实验室B")
}

// TestOpenRejectsDestroyedOperatorMismatchBeforeLocation 操作人与地点同时
// 矛盾时先报操作人，同一份损坏文件的报错保持稳定。
func TestOpenRejectsDestroyedOperatorMismatchBeforeLocation(t *testing.T) {
	path := writeStructLedger(t, custodyMismatchLedger("王五", "实验室C"))
	s, err := Open(path)
	if err == nil || s != nil {
		t.Fatalf("人与地点同时矛盾应打开失败且不返回 Store")
	}
	msg := err.Error()
	if !strings.Contains(msg, "销毁操作人") || !strings.Contains(msg, "王五") {
		t.Fatalf("应先指出销毁操作人矛盾: %v", err)
	}
}

// TestOpenRejectsBlankDestroyedOperatorOrLocation 销毁信息中的操作人、地点
// 缺失、为空或只有空白都属于无效数据：不能因为样品对应字段也为空就视为
// 一致，也不能把纯空白当作有效值。
func TestOpenRejectsBlankDestroyedOperatorOrLocation(t *testing.T) {
	cases := []struct {
		name     string
		operator string
		location string
		mention  string
	}{
		{"操作人为空", "", "实验室B", "缺少操作人"},
		{"操作人只有空白", "   \t", "实验室B", "只有空白"},
		{"地点为空", "李四", "", "缺少销毁地点"},
		{"地点只有空白", "李四", "  \n ", "只有空白"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeStructLedger(t, custodyMismatchLedger(c.operator, c.location))
			mustRejectOpen(t, path, "S-001", c.mention)
		})
	}

	// JSON 中完全没有 operator 字段（缺失键）同样按缺少操作人拒绝。
	path := writeRawLedger(t, `{
	  "version": 1,
	  "samples": {
	    "S-001": {"id":"S-001","initial":10000,"remaining":0,"holder":"李四","location":"实验室B","children":["S-001-A"],"history":[
	      {"kind":"register","time":"2026-10-01T09:00:00Z","holder":"李四","location":"实验室B"},
	      {"kind":"split","time":"2026-10-02T09:00:00Z","holder":"李四","location":"实验室B"},
	      {"kind":"destroy","time":"2026-10-03T12:00:00Z","holder":"李四","location":"实验室B"}
	    ],"destroyed":{"location":"实验室B","at":"2026-10-03T12:00:00Z","reason":"实验结束按规程销毁","qty":6750}},
	    "S-001-A": {"id":"S-001-A","parentId":"S-001","initial":3250,"remaining":3250,"holder":"李四","location":"实验室B","children":[],"history":[
	      {"kind":"split","time":"2026-10-02T09:00:00Z","holder":"李四","location":"实验室B"}
	    ]}
	  },
	  "transfers": {}
	}`)
	mustRejectOpen(t, path, "S-001", "缺少操作人")

	// 样品自身持有人、地点也为空时，销毁一侧为空仍不能视为一致。
	bothEmpty := destroyedSample("E", 1000, 0, 1000)
	bothEmpty.Holder, bothEmpty.Location = "", ""
	bothEmpty.Destroyed.Operator, bothEmpty.Destroyed.Location = "", ""
	path = writeStructLedger(t, &ledger{Samples: map[string]*sampleRecord{"E": bothEmpty}})
	mustRejectOpen(t, path, "E", "缺少操作人")
}

// TestOpenRejectsDestroyedCustodyWithoutTrimCompare 已保存的非空文本按
// 原文逐字核对：首尾空白或内部空白不同，即使去掉空白后相同也不能接受。
func TestOpenRejectsDestroyedCustodyWithoutTrimCompare(t *testing.T) {
	// 首尾带制表符：trim 后等于李四，仍必须拒绝。
	padded := custodyMismatchLedger("\t李四\t", "实验室B")
	path := writeStructLedger(t, padded)
	mustRejectOpen(t, path, "S-001", "销毁操作人", "不一致")

	// 内部多一个空格：去不掉的差异。
	inside := custodyMismatchLedger("李 四", "实验室B")
	path = writeStructLedger(t, inside)
	mustRejectOpen(t, path, "S-001", "销毁操作人", "不一致")

	// 地点尾随空格同理。
	locPadded := custodyMismatchLedger("李四", "实验室B ")
	path = writeStructLedger(t, locPadded)
	mustRejectOpen(t, path, "S-001", "销毁地点", "不一致")
}

// TestOpenDestroyedCustodyIndependentAcrossParentAndChild 核对只看同一编号
// 样品自身的最后记录：父样与子样由不同人员在不同地点分别保管、销毁合法；
// 子样后来换人、移动不影响父样的销毁信息。
func TestOpenDestroyedCustodyIndependentAcrossParentAndChild(t *testing.T) {
	destroyAt := destroyedCheckAt
	// 父样最后由张三在实验室A 保管并在那里销毁 6.750；它早期登记历史中的
	// 人员、地点与最后记录不同也允许。
	parent := destroyedSample("P", 10000, 0, 6750, "C")
	parent.Holder, parent.Location = "张三", "实验室A"
	parent.History[0].Holder, parent.History[0].Location = "张三", "实验室A"
	parent.Destroyed.Operator, parent.Destroyed.Location = "张三", "实验室A"
	// 子样后来交接给王五、移到实验室C，并由王五在实验室C 销毁全部
	// 3.250；父子销毁信息互不一致却各自与自身最后记录一致。
	child := destroyedSampleWithParent("C", "P", 3250, 3250)
	child.Holder, child.Location = "王五", "实验室C"
	child.History = []historyRecord{
		{Kind: "split", Time: splitCheckAt, Holder: "张三", Location: "实验室A"},
		{Kind: "transfer-in", Time: splitCheckAt.Add(time.Hour), Holder: "王五", Location: "实验室C"},
		{Kind: "destroy", Time: destroyAt, Holder: "王五", Location: "实验室C"},
	}
	child.Destroyed.Operator, child.Destroyed.Location = "王五", "实验室C"
	l := &ledger{Samples: map[string]*sampleRecord{"P": parent, "C": child}}

	s, err := Open(writeStructLedger(t, l))
	if err != nil {
		t.Fatalf("父子各自与自身最后记录一致时应正常恢复: %v", err)
	}
	p, _ := s.GetSample("P")
	c, _ := s.GetSample("C")
	if p.Holder != "张三" || p.Location != "实验室A" ||
		p.Destruction == nil || p.Destruction.Operator != "张三" || p.Destruction.Location != "实验室A" {
		t.Fatalf("父样应保留张三/实验室A 的销毁详情: %+v", p)
	}
	if c.Holder != "王五" || c.Location != "实验室C" ||
		c.Destruction == nil || c.Destruction.Operator != "王五" || c.Destruction.Location != "实验室C" {
		t.Fatalf("子样应保留王五/实验室C 的销毁详情: %+v", c)
	}

	// 只改父样销毁操作人，子样记录再正常也不能放行整份文件。
	parent.Destroyed.Operator = "赵六"
	mustRejectOpen(t, writeStructLedger(t, l), "P", "赵六", "张三")
}

// TestOpenRestoresDestroyedCustodyWhenHistoryDifferedEarlier 同一编号样品
// 早期登记、交接历史中的人员、地点与最后记录不同，但销毁信息与最后记录
// 一致时正常恢复，历史差异不是拒绝理由。
func TestOpenRestoresDestroyedCustodyWhenHistoryDifferedEarlier(t *testing.T) {
	s := destroyedSample("S", 5000, 0, 5000)
	// 最后记录是李四/实验室B（构造默认），把早期登记历史改成张三/实验室A。
	s.History[0].Holder, s.History[0].Location = "张三", "实验室A"
	store, err := Open(writeStructLedger(t, &ledger{
		Samples: map[string]*sampleRecord{"S": s},
	}))
	if err != nil {
		t.Fatalf("销毁信息与最后记录一致、仅早期历史不同应正常恢复: %v", err)
	}
	got, _ := store.GetSample("S")
	if got.Holder != "李四" || got.Location != "实验室B" ||
		got.Destruction == nil || got.Destruction.Operator != "李四" || got.Destruction.Location != "实验室B" {
		t.Fatalf("销毁详情与最后持有人、地点应原样保留: %+v", got)
	}
}

// TestOpenRejectsDestroyedCustodyDespiteOtherValidSamples 文件中另有正常
// 样品时，一处销毁人员/地点矛盾仍使整份打开失败，原文件保持原样：不补
// 填或替换人员、地点，不删除销毁信息改成未销毁，也不只加载正常部分。
func TestOpenRejectsDestroyedCustodyDespiteOtherValidSamples(t *testing.T) {
	l := custodyMismatchLedger("王五", "实验室C")
	l.Samples["OK"] = destroyedSample("OK", 1000, 0, 1000)
	path := writeStructLedger(t, l)
	mustRejectOpen(t, path, "S-001", "王五", "李四")

	raw := readFileString(t, path)
	if !strings.Contains(raw, `"operator": "王五"`) || !strings.Contains(raw, `"destroyed"`) {
		t.Fatalf("打开失败不得替换操作人或删除销毁信息: %s", raw)
	}
}

// TestOpenConsistentDestroyedViaPublicAPI 端到端：经公开操作产生的合法
// 销毁文件（操作人、地点本就与最后持有人、地点一致）重开后查询仍同时
// 保留销毁详情与最后持有人、地点。
func TestOpenConsistentDestroyedViaPublicAPI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return registerCheckAt }
	mustRegister(t, s, "S-001", "10.000", "李四", "实验室B")
	if _, err := s.Split(SplitInput{ParentID: "S-001", Parts: []SplitPart{
		{ID: "S-001-A", Qty: "3.250"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(DestroyInput{
		SampleID: "S-001", Operator: "李四", Location: "实验室B",
		At: destroyedCheckAt, Reason: "实验结束按规程销毁",
	}); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("公开操作产生的合法销毁记录应正常恢复: %v", err)
	}
	got, err := s2.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Holder != "李四" || got.Location != "实验室B" {
		t.Fatalf("最后持有人、地点应保留: %+v", got)
	}
	if got.Destruction == nil || got.Destruction.Operator != "李四" ||
		got.Destruction.Location != "实验室B" || got.Destruction.Qty != "6.750" {
		t.Fatalf("销毁详情应原样保留: %+v", got.Destruction)
	}
}
