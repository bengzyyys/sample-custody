package custody

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// destroyedScenario 生成任务示例文件：原样 S-001 初始 10.000，经已确认
// 交接 TR-001 后分出 3.250 的子样 S-001-A，随后销毁（剩余量 0.000）。
// destroyedQty 为文件中记录的实际销毁量（千分之一毫升）。
func destroyedScenario(destroyedQty int64) string {
	return fmt.Sprintf(`{
  "version": 1,
  "samples": {
    "S-001": {"id":"S-001","initial":10000,"remaining":0,"holder":"李四","location":"实验室B","children":["S-001-A"],"history":[
      {"kind":"register","time":"2026-10-02T09:00:00Z","holder":"张三","location":"实验室A","detail":"登记原样"},
      {"kind":"transfer-out","time":"2026-10-02T10:00:00Z","holder":"张三","location":"实验室A","detail":"发起交接 TR-001，待 李四 在 实验室B 接收"},
      {"kind":"transfer-in","time":"2026-10-02T11:00:00Z","holder":"李四","location":"实验室B","detail":"交接 TR-001 确认接收（交出时间 2026-10-02T10:00:00Z）"},
      {"kind":"split","time":"2026-10-02T12:00:00Z","holder":"李四","location":"实验室B","detail":"分装创建子样 S-001-A"},
      {"kind":"destroy","time":"2026-10-03T09:00:00Z","holder":"李四","location":"实验室B","detail":"销毁全部剩余量 6.750 毫升，原因：实验结束按规程销毁"}
    ],"destroyed":{"operator":"李四","location":"实验室B","at":"2026-10-03T09:00:00Z","reason":"实验结束按规程销毁","qty":%d}},
    "S-001-A": {"id":"S-001-A","parentId":"S-001","initial":3250,"remaining":3250,"holder":"李四","location":"实验室B","children":[],"history":[
      {"kind":"split","time":"2026-10-02T12:00:00Z","holder":"李四","location":"实验室B","detail":"由样品 S-001 分装"}
    ]}
  },
  "transfers": {
    "TR-001": {"id":"TR-001","sampleId":"S-001","fromHolder":"张三","fromLocation":"实验室A","toHolder":"李四","toLocation":"实验室B","qty":10000,"handedOverAt":"2026-10-02T10:00:00Z","confirmed":true,"receivedAt":"2026-10-02T11:00:00Z","confirmedBy":"李四"}
  }
}`, destroyedQty)
}

// TestOpenRejectsDestroyedQtyCoveringSplitChild 是任务示例的矛盾记录：
// 原样已分出 3.250 子样，实际销毁量应是 6.750，文件却记成全部初始量
// 10.000；即使剩余量已归零、交接记录合法，也必须拒绝打开。
func TestOpenRejectsDestroyedQtyCoveringSplitChild(t *testing.T) {
	path := writeRawLedger(t, destroyedScenario(10000))
	mustRejectOpen(t, path, "S-001", "销毁", "10.000", "3.250")
}

// TestOpenRestoresConsistentDestruction 是任务示例的合法分支：销毁量
// 6.750 加上直接子样初始量 3.250 恰好等于初始量 10.000，正常恢复后
// 销毁信息、子样关系与已确认交接都保留，且互不影响后续查询。
func TestOpenRestoresConsistentDestruction(t *testing.T) {
	path := writeRawLedger(t, destroyedScenario(6750))
	s, err := Open(path)
	if err != nil {
		t.Fatalf("数量守恒的销毁记录应正常恢复: %v", err)
	}
	p, err := s.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	if p.Remaining != "0.000" || p.InitialQty != "10.000" {
		t.Fatalf("恢复后数量错误: %+v", p)
	}
	if p.Destruction == nil || p.Destruction.Qty != "6.750" ||
		p.Destruction.Operator != "李四" || p.Destruction.Location != "实验室B" ||
		p.Destruction.Reason != "实验结束按规程销毁" {
		t.Fatalf("恢复后销毁信息错误: %+v", p.Destruction)
	}
	if p.Holder != "李四" || p.Location != "实验室B" {
		t.Fatalf("恢复后持有人地点应保留: %+v", p)
	}
	if len(p.Children) != 1 || p.Children[0] != "S-001-A" || len(p.History) != 5 {
		t.Fatalf("恢复后子样关系与保管历史应保留: %+v", p)
	}
	c, err := s.GetSample("S-001-A")
	if err != nil {
		t.Fatal(err)
	}
	if c.Remaining != "3.250" || c.Destruction != nil || c.ParentID != "S-001" {
		t.Fatalf("子样应保持独立未销毁: %+v", c)
	}
	tr, err := s.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Confirmed || tr.Qty != "10.000" || tr.ConfirmedBy != "李四" {
		t.Fatalf("已确认交接历史应保留: %+v", tr)
	}
}

// TestOpenRejectsDestroyedWithLeftoverRemaining 已销毁样品的当前剩余量
// 必须为 0.000，否则即使销毁量与初始量对得上也拒绝。
func TestOpenRejectsDestroyedWithLeftoverRemaining(t *testing.T) {
	content := strings.Replace(destroyedScenario(10000), `"remaining":0`, `"remaining":500`, 1)
	path := writeRawLedger(t, content)
	mustRejectOpen(t, path, "S-001", "剩余量", "0.500", "0.000")
}

// TestOpenRejectsNonPositiveDestroyedQty 实际销毁量必须大于零。
func TestOpenRejectsNonPositiveDestroyedQty(t *testing.T) {
	for name, qty := range map[string]int64{"零销毁量": 0, "负销毁量": -250} {
		t.Run(name, func(t *testing.T) {
			path := writeRawLedger(t, destroyedScenario(qty))
			mustRejectOpen(t, path, "S-001", "销毁量")
		})
	}
}

// TestOpenRejectsDestroyedWithNonPositiveInitial 参与核对的初始量必须
// 大于零，不能靠销毁量凑出平衡。
func TestOpenRejectsDestroyedWithNonPositiveInitial(t *testing.T) {
	content := `{
  "version": 1,
  "samples": {"S1": {"id":"S1","initial":0,"remaining":0,"holder":"h","location":"l","children":[],"history":[],
    "destroyed":{"operator":"h","location":"l","at":"2026-10-02T10:00:00Z","reason":"r","qty":0}}},
  "transfers": {}
}`
	path := writeRawLedger(t, content)
	mustRejectOpen(t, path, "S1", "初始量")
}

// TestOpenDestroyedWithoutChildren 没有分出子样的样品，实际销毁量必须
// 恰好等于自己的初始量。
func TestOpenDestroyedWithoutChildren(t *testing.T) {
	scenario := func(qty int64) string {
		return fmt.Sprintf(`{
  "version": 1,
  "samples": {"S1": {"id":"S1","initial":1000,"remaining":0,"holder":"h","location":"l","children":[],"history":[],
    "destroyed":{"operator":"h","location":"l","at":"2026-10-02T10:00:00Z","reason":"r","qty":%d}}},
  "transfers": {}
}`, qty)
	}

	path := writeRawLedger(t, scenario(999))
	mustRejectOpen(t, path, "S1", "1.000", "0.999", "0.000")

	path = writeRawLedger(t, scenario(1000))
	s, err := Open(path)
	if err != nil {
		t.Fatalf("无子样且销毁量等于初始量应正常恢复: %v", err)
	}
	got, err := s.GetSample("S1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Destruction == nil || got.Destruction.Qty != "1.000" || got.Remaining != "0.000" {
		t.Fatalf("恢复后销毁信息错误: %+v", got)
	}
}

// TestOpenDestroyedCountsDirectChildInitialOnly 核对时只取各直接子样
// 创建时取得的初始量：子样后来继续分装不改变原样当时分出的量，不能用
// 子样当前剩余量代替，也不能把孙样再次计入。
func TestOpenDestroyedCountsDirectChildInitialOnly(t *testing.T) {
	// P 初始 10.000，分出 C 4.000；C 又分出 G 1.500（C 当前剩余 2.500）。
	// P 的合法销毁量是 10.000 - 4.000 = 6.000，与 C 当前剩余量、孙样无关。
	scenario := func(destroyedQty int64) string {
		return fmt.Sprintf(`{
  "version": 1,
  "samples": {
    "P": {"id":"P","initial":10000,"remaining":0,"holder":"h","location":"l","children":["C"],"history":[],
      "destroyed":{"operator":"h","location":"l","at":"2026-10-02T12:00:00Z","reason":"r","qty":%d}},
    "C": {"id":"C","parentId":"P","initial":4000,"remaining":2500,"holder":"h","location":"l","children":["G"],"history":[]},
    "G": {"id":"G","parentId":"C","initial":1500,"remaining":1500,"holder":"h","location":"l","children":[],"history":[]}
  },
  "transfers": {}
}`, destroyedQty)
	}

	// 按子样当前剩余量凑出的 7.500（= 10.000 - 2.500）必须拒绝，
	// 错误文字展示初始量、销毁量与直接子样总量（4.000，不含孙样）。
	path := writeRawLedger(t, scenario(7500))
	mustRejectOpen(t, path, "P", "10.000", "7.500", "4.000")

	// 把孙样再次计入才会凑出的 8.500（= 10.000 - 4.000 - 1.500 +
	// 重复计入的 1.500 之外的错误平衡）同样不是合法销毁量。
	path = writeRawLedger(t, scenario(8500))
	mustRejectOpen(t, path, "P", "10.000", "8.500", "4.000")

	// 正确的 6.000 正常恢复，三代样品关系各自保留。
	path = writeRawLedger(t, scenario(6000))
	s, err := Open(path)
	if err != nil {
		t.Fatalf("按直接子样初始量守恒的销毁记录应正常恢复: %v", err)
	}
	p, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if p.Destruction == nil || p.Destruction.Qty != "6.000" {
		t.Fatalf("恢复后销毁信息错误: %+v", p.Destruction)
	}
	c, _ := s.GetSample("C")
	if c.Remaining != "2.500" || len(c.Children) != 1 || c.Children[0] != "G" {
		t.Fatalf("子样的后续分装应保持原样: %+v", c)
	}
}

// TestOpenDestroyedParentChildIndependentRestore 父子各自合法销毁的
// 记录恢复后仍各自独立：父样核对只算直接子样初始量，子样自己的销毁
// 记录按自身初始量核对，互不影响。
func TestOpenDestroyedParentChildIndependentRestore(t *testing.T) {
	content := `{
  "version": 1,
  "samples": {
    "P": {"id":"P","initial":10000,"remaining":0,"holder":"张三","location":"A","children":["C"],"history":[
      {"kind":"register","time":"2026-10-02T09:00:00Z","holder":"张三","location":"A"},
      {"kind":"split","time":"2026-10-02T10:00:00Z","holder":"张三","location":"A"},
      {"kind":"destroy","time":"2026-10-02T12:00:00Z","holder":"张三","location":"A"}
    ],"destroyed":{"operator":"张三","location":"A","at":"2026-10-02T12:00:00Z","reason":"父样销毁","qty":6000}},
    "C": {"id":"C","parentId":"P","initial":4000,"remaining":0,"holder":"李四","location":"B","children":[],"history":[
      {"kind":"split","time":"2026-10-02T10:00:00Z","holder":"张三","location":"A"},
      {"kind":"destroy","time":"2026-10-02T15:00:00Z","holder":"李四","location":"B"}
    ],"destroyed":{"operator":"李四","location":"B","at":"2026-10-02T15:00:00Z","reason":"子样销毁","qty":4000}}
  },
  "transfers": {}
}`
	path := writeRawLedger(t, content)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("父子各自守恒的销毁记录应正常恢复: %v", err)
	}
	p, err := s.GetSample("P")
	if err != nil {
		t.Fatal(err)
	}
	if p.Destruction == nil || p.Destruction.Qty != "6.000" ||
		p.Destruction.Operator != "张三" || p.Destruction.Location != "A" ||
		p.Destruction.Reason != "父样销毁" {
		t.Fatalf("父样销毁信息错误: %+v", p.Destruction)
	}
	c, err := s.GetSample("C")
	if err != nil {
		t.Fatal(err)
	}
	if c.Destruction == nil || c.Destruction.Qty != "4.000" ||
		c.Destruction.Operator != "李四" || c.Destruction.Location != "B" ||
		c.Destruction.Reason != "子样销毁" || c.ParentID != "P" {
		t.Fatalf("子样销毁信息错误: %+v", c.Destruction)
	}
}

// TestOpenRejectsAnyInconsistentDestruction 同一份数据里其他原样不参与
// 核对，但任何一份销毁记录不符都使整份文件打开失败，不能只加载正常样品。
func TestOpenRejectsAnyInconsistentDestruction(t *testing.T) {
	content := `{
  "version": 1,
  "samples": {
    "GOOD": {"id":"GOOD","initial":1000,"remaining":0,"holder":"h","location":"l","children":[],"history":[],
      "destroyed":{"operator":"h","location":"l","at":"2026-10-02T10:00:00Z","reason":"r","qty":1000}},
    "BAD": {"id":"BAD","initial":2000,"remaining":0,"holder":"h","location":"l","children":[],"history":[],
      "destroyed":{"operator":"h","location":"l","at":"2026-10-02T10:00:00Z","reason":"r","qty":1000}}
  },
  "transfers": {}
}`
	path := writeRawLedger(t, content)
	mustRejectOpen(t, path, "BAD", "2.000", "1.000")
}

// TestOpenRejectsDestroyedWithMissingChild 销毁核对需要直接子样的初始量，
// 子样记录缺失时无法核对，按无效数据拒绝。
func TestOpenRejectsDestroyedWithMissingChild(t *testing.T) {
	content := `{
  "version": 1,
  "samples": {"S1": {"id":"S1","initial":1000,"remaining":0,"holder":"h","location":"l","children":["ghost"],"history":[],
    "destroyed":{"operator":"h","location":"l","at":"2026-10-02T10:00:00Z","reason":"r","qty":600}}},
  "transfers": {}
}`
	path := writeRawLedger(t, content)
	mustRejectOpen(t, path, "S1", "ghost")
}

// TestOpenDestroyedNearSupportedLimit 支持上限附近的合法数量应准确恢复：
// 初始量取 int64 可表示的最大千分之一毫升数，无子样时销毁量等于初始量。
func TestOpenDestroyedNearSupportedLimit(t *testing.T) {
	content := fmt.Sprintf(`{
  "version": 1,
  "samples": {"BIG": {"id":"BIG","initial":%[1]d,"remaining":0,"holder":"h","location":"l","children":[],"history":[],
    "destroyed":{"operator":"h","location":"l","at":"2026-10-02T10:00:00Z","reason":"r","qty":%[1]d}}},
  "transfers": {}
}`, int64(math.MaxInt64))
	path := writeRawLedger(t, content)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("上限附近的合法销毁记录应准确恢复: %v", err)
	}
	got, err := s.GetSample("BIG")
	if err != nil {
		t.Fatal(err)
	}
	const want = "9223372036854775.807"
	if got.InitialQty != want || got.Destruction == nil || got.Destruction.Qty != want {
		t.Fatalf("上限数量恢复错误: init=%s destruction=%+v", got.InitialQty, got.Destruction)
	}
}

// TestOpenRejectsDestroyedSumOverflow 销毁量与直接子样初始量合计超出
// 可表示范围时按无效数据拒绝，不能因数量过大把差额忽略或接受错误记录。
func TestOpenRejectsDestroyedSumOverflow(t *testing.T) {
	content := fmt.Sprintf(`{
  "version": 1,
  "samples": {
    "BIG": {"id":"BIG","initial":%[1]d,"remaining":0,"holder":"h","location":"l","children":["C"],"history":[],
      "destroyed":{"operator":"h","location":"l","at":"2026-10-02T10:00:00Z","reason":"r","qty":%[2]d}},
    "C": {"id":"C","parentId":"BIG","initial":1000,"remaining":1000,"holder":"h","location":"l","children":[],"history":[]}
  },
  "transfers": {}
}`, int64(math.MaxInt64), int64(math.MaxInt64)-500)
	path := writeRawLedger(t, content)
	mustRejectOpen(t, path, "BIG", "范围")
}
