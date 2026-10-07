package custody

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// 公开的哨兵错误。不存在的样品或交接、非法入参、冲突操作各自独立，
// 便于调用方用 errors.Is 区分；具体错误消息仍会说明原因。
var (
	// ErrInvalid 表示入参不合法（空白编号、数量格式或数值错误、交接人地点不一致等）。
	ErrInvalid = errors.New("custody: invalid argument")
	// ErrNotFound 表示样品编号或交接编号在这份本地数据中不存在。
	ErrNotFound = errors.New("custody: not found")
	// ErrConflict 表示与当前状态冲突（编号重复但内容不同、超量分装、
	// 已有待确认交接、零量样品继续流转、接收条件不满足等）。
	ErrConflict = errors.New("custody: conflict")
)

// Sample 是一次按样品编号查询得到的完整视图。
//
// 数量一律以三位小数字符串展示，单位为毫升。ParentID 为空说明该样品是
// 登记的原样；非空时指向分装来源。Children 只含直接由本样品分装出的
// 子样编号，按分装发生顺序排列。History 按发生顺序记录该样品自身的
// 保管变化，PendingTransfer 非空时是该样品当前唯一的待确认交接。
type Sample struct {
	ID         string    `json:"id"`
	ParentID   string    `json:"parentId,omitempty"`
	InitialQty string    `json:"initialQty"`
	Remaining  string    `json:"remaining"`
	Holder     string    `json:"holder"`
	Location   string    `json:"location"`
	Children   []string  `json:"children"`
	History    []History `json:"history"`

	// PendingTransfer 为该样品当前待确认交接的详情；没有待确认交接时为 nil。
	PendingTransfer *TransferView `json:"pendingTransfer,omitempty"`

	// Destruction 非空表示该样品已销毁，其中记录销毁操作人、地点、时间、
	// 原因和实际销毁量；未销毁样品（包括剩余量因分装用尽而归零的样品）
	// 一律为 nil。因此 Remaining 为 "0.000" 时可据此区分“分装用尽”与
	// “已销毁”。
	Destruction *Destruction `json:"destruction,omitempty"`
}

// Destruction 是样品销毁信息的对外只读视图。
type Destruction struct {
	// Operator 与 Location 为销毁时登记的操作人和地点，
	// 与销毁前样品最后的持有人、地点一致。
	Operator string    `json:"operator"`
	Location string    `json:"location"`
	At       time.Time `json:"at"`
	Reason   string    `json:"reason"`
	// Qty 为实际销毁量（销毁发生时该样品的全部剩余量），三位小数毫升。
	Qty string `json:"qty"`
}

// History 是样品保管历史中的一条记录，按发生顺序追加。
type History struct {
	// Kind 为 "register"、"split"、"transfer-out"、"transfer-in" 或 "destroy"。
	Kind string `json:"kind"`
	// Time 是该事件发生的时间。分装没有独立的外部时间，沿用登记语义记为创建时刻。
	Time time.Time `json:"time"`
	// Holder 与 Location 是事件发生时（或事件确立后）该样品的持有人和地点。
	Holder   string `json:"holder"`
	Location string `json:"location"`
	// Detail 携带事件相关编号：分装为父/子关系说明，交接为交接编号。
	Detail string `json:"detail,omitempty"`
}

// TransferView 是交接记录对外的只读视图，待确认与已确认共用同一结构。
type TransferView struct {
	TransferID string `json:"transferId"`
	SampleID   string `json:"sampleId"`

	FromHolder   string    `json:"fromHolder"`
	FromLocation string    `json:"fromLocation"`
	ToHolder     string    `json:"toHolder"`
	ToLocation   string    `json:"toLocation"`
	Qty          string    `json:"qty"`
	HandedOverAt time.Time `json:"handedOverAt"`

	// Confirmed 为 false 时处于待确认，ReceivedAt/Receiver 为零值含义。
	Confirmed   bool       `json:"confirmed"`
	ReceivedAt  *time.Time `json:"receivedAt,omitempty"`
	ConfirmedBy string     `json:"confirmedBy,omitempty"`
	ConfirmedAt string     `json:"confirmedAtLocation,omitempty"`
}

// --- 内部持久化模型 -----------------------------------------------------
// 数量字段以千分之一毫升的整数保存，避免任何浮点参与计算。

type sampleRecord struct {
	ID        string          `json:"id"`
	ParentID  string          `json:"parentId,omitempty"`
	Initial   int64           `json:"initial"`
	Remaining int64           `json:"remaining"`
	Holder    string          `json:"holder"`
	Location  string          `json:"location"`
	Children  []string        `json:"children"`
	History   []historyRecord `json:"history"`

	// PendingID 非空时指向 transfers 中一条尚未确认的交接；
	// 同一样品任意时刻至多存在一条待确认交接。
	PendingID string `json:"pendingId,omitempty"`

	// Destroyed 非空时表示样品已销毁，记录销毁操作人、地点、时间、原因
	// 与实际销毁量。旧数据没有该字段（nil）的样品一律视为未销毁，
	// 即使 Remaining 为零也不能据此推断为销毁。
	Destroyed *destructionRecord `json:"destroyed,omitempty"`
}

// destructionRecord 是销毁信息的内部持久化模型。销毁只处理样品当时的
// 全部剩余量，Qty 保存当时销毁的千分之一毫升整数。
type destructionRecord struct {
	Operator string    `json:"operator"`
	Location string    `json:"location"`
	At       time.Time `json:"at"`
	Reason   string    `json:"reason"`
	Qty      int64     `json:"qty"`
}

type historyRecord struct {
	Kind     string    `json:"kind"`
	Time     time.Time `json:"time"`
	Holder   string    `json:"holder"`
	Location string    `json:"location"`
	Detail   string    `json:"detail,omitempty"`
}

type transferRecord struct {
	ID           string    `json:"id"`
	SampleID     string    `json:"sampleId"`
	FromHolder   string    `json:"fromHolder"`
	FromLocation string    `json:"fromLocation"`
	ToHolder     string    `json:"toHolder"`
	ToLocation   string    `json:"toLocation"`
	Qty          int64     `json:"qty"`
	HandedOverAt time.Time `json:"handedOverAt"`

	Confirmed   bool       `json:"confirmed"`
	ReceivedAt  *time.Time `json:"receivedAt,omitempty"`
	ConfirmedBy string     `json:"confirmedBy,omitempty"`
}

// ledger 是一份本地样品数据的完整可序列化状态。
type ledger struct {
	// Version 便于以后识别数据格式。
	Version   int                        `json:"version"`
	Samples   map[string]*sampleRecord   `json:"samples"`
	Transfers map[string]*transferRecord `json:"transfers"`
}

func newLedger() *ledger {
	return &ledger{
		Version:   1,
		Samples:   make(map[string]*sampleRecord),
		Transfers: make(map[string]*transferRecord),
	}
}

// UnmarshalJSON 按现有格式解码一份本地样品数据，并在交接集合上额外保证
// 交接编号在整份数据中唯一。transfers 是一个 JSON 对象，标准库把它解码进
// map 时，同一键出现多次会静默用后一条覆盖前一条；顶层若写了多个同名
// transfers 字段，标准库又只保留最后一个字段的内容——仅靠解码后的 map
// 无法再发现这两类重复，原先的接收事实或待确认信息可能因此丢失。因此
// 解码前后各做一次：
//   - 先按现有格式正常解码，缺省、null 或空对象的集合保持原有含义；
//   - 再在原始 JSON 上做一次 token 级扫描（见 duplicateJSONKeys），统计
//     顶层全部 transfers 对象的键：一旦同一编号（按 JSON 解码后实际表示的
//     文字比较，合法 Unicode 转义与直接写出同一文字视为同一编号）在整份
//     数据的所有交接集合里合计出现多次，无论两条记录在同一集合内还是分属
//     两个同名 transfers 字段、相邻还是隔着其他交接或其他顶层字段、内容
//     是否相同、确认状态如何，都返回包装了 ErrInvalid 的错误，并写明重复
//     的交接编号以及“同一编号出现多次”的原因。
//
// 扫描只读取 transfers 对象的键，其值作为整体跳过，因此样品保管历史的
// 说明、交接记录字段值或其他说明文字中再次提到某编号不会被当作重复键。
// 原始扫描无法解析（正常解码也会失败）时同样中止，绝不带着重复编号恢复。
func (l *ledger) UnmarshalJSON(raw []byte) error {
	type plainLedger ledger
	if err := json.Unmarshal(raw, (*plainLedger)(l)); err != nil {
		return err
	}

	dup, err := duplicateJSONKeys(raw, "transfers")
	if err != nil {
		return err
	}
	if dup != "" {
		return fmt.Errorf("%w: 交接编号 %q 在整份数据的交接集合中出现多次，同一交接编号只能对应一条交接记录；重复编号无法确定应保留哪一条接收事实或待确认信息，无法恢复",
			ErrInvalid, dup)
	}
	return nil
}

// duplicateJSONKeys 扫描一段对象 JSON，返回顶层指定名字段（field）对应
// 的全部对象里重复出现的键。字段缺省、为 null 或为空对象时不存在重复，
// 返回空字符串。比较基于 JSON 解码后实际表示的文字：Decoder.Token 返回的
// 字符串键已完成 Unicode 反转义，所以 "TR-001" 与 "TR-001" 这类
// 合法转义表示同一文字时会被判为同一键。
//
// 顶层同名字段可能出现多次（例如两个 transfers 字段），此时所有同名对象
// 共用同一份已见集合：同一编号分属两个同名字段同样判为重复，不能被标准库
// “只保留最后一个字段”的解码语义绕过；同名字段各自只含不同编号时不算重复。
// 扫描只在目标对象内统计“键”，对象与数组的值都按括号配对整体跳过，
// 不读取其中任何字符串内容——因此写在字段值、保管历史说明等位置的编号
// 文本不会被当作键统计。顶层其他字段的内容同样整体跳过。返回的重复键
// 取按实际文字判重时第一个再次出现的键；扫描过程中一旦 JSON 无法解析
// 即返回该错误。
func duplicateJSONKeys(raw []byte, field string) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	// 进入顶层对象。
	if tok, err := dec.Token(); err != nil {
		return "", err
	} else if d, ok := tok.(json.Delim); !ok || d != '{' {
		return "", fmt.Errorf("期望顶层为 JSON 对象")
	}

	// 顶层全部同名 transfers 对象共用同一份已见集合：同一编号无论出现在
	// 同一对象内还是分属多个同名字段，合计出现多次即判为重复。
	seen := make(map[string]struct{})
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		key, _ := tok.(string)
		if key != field {
			if err := skipJSONValue(dec); err != nil {
				return "", err
			}
			continue
		}
		// 找到目标字段：先取其起始 token，判断是否为对象。顶层出现多个
		// 同名字段时逐一检查并累计进同一 seen，避免被标准库“取最后一个”
		// 的语义绕过判重。
		start, err := dec.Token()
		if err != nil {
			return "", err
		}
		d, ok := start.(json.Delim)
		if !ok || d != '{' {
			// null、标量或数组都不是对象，且该值已随上面的 Token 消费完毕。
			continue
		}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return "", err
			}
			k := kt.(string)
			if err := skipJSONValue(dec); err != nil {
				return "", err
			}
			if _, exists := seen[k]; exists {
				return k, nil
			}
			seen[k] = struct{}{}
		}
		// 消费目标对象的结束 '}'。
		if _, err := dec.Token(); err != nil {
			return "", err
		}
	}
	// 消费顶层对象的结束 '}'。
	if _, err := dec.Token(); err != nil {
		return "", err
	}
	return "", nil
}

// skipJSONValue 跳过紧跟在一个键之后的值：标量已随下一次 Token 直接读出，
// 对象或数组则按嵌套深度跳过其全部内容（JSON 解码保证分界符正确配对）。
// 调用方必须刚读出键、尚未读取值。
func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if _, ok := tok.(json.Delim); !ok {
		return nil // 标量值（字符串、数字、布尔、null）。
	}
	depth := 1
	for depth > 0 {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if td, ok := t.(json.Delim); ok {
			switch td {
			case '{', '[':
				depth++
			default: // '}' 或 ']'
				depth--
			}
		}
	}
	return nil
}

// deepCopy 复制整份状态。所有写操作都在拷贝上完成校验与修改，
// 只有全部校验通过后才用它替换内存状态并落盘；任一步失败都不会
// 改动现有数量、位置、历史或待确认记录。
func (l *ledger) deepCopy() *ledger {
	cp := newLedger()
	for id, s := range l.Samples {
		sc := *s
		sc.Children = append([]string(nil), s.Children...)
		sc.History = make([]historyRecord, len(s.History))
		copy(sc.History, s.History)
		if s.Destroyed != nil {
			dc := *s.Destroyed
			sc.Destroyed = &dc
		}
		cp.Samples[id] = &sc
	}
	for id, t := range l.Transfers {
		tc := *t
		if t.ReceivedAt != nil {
			r := *t.ReceivedAt
			tc.ReceivedAt = &r
		}
		cp.Transfers[id] = &tc
	}
	return cp
}
