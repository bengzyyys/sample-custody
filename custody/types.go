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

	// idPresent 记录 JSON 中是否实际写出了 id 字段。Go 的字符串字段无法区分
	// “字段缺失”与“字段存在但为空字符串”，恢复核对（见
	// validateTransferIDAgreement）需要把二者作为不同原因分别报错，因此由
	// UnmarshalJSON 额外记录这一信息；它不参与序列化。
	idPresent bool `json:"-"`
}

// UnmarshalJSON 在标准解码之外记录 id 字段是否实际写出。字段存在但为空与
// 字段缺失必须区分：前者是“编号为空”，后者是“缺少自身编号”。先用一层
// RawMessage 探测字段是否存在，再按原结构标准解码，语法、类型错误与未知
// 字段处理都与标准库保持一致；该方法不改变任何已解码字段的值。
func (t *transferRecord) UnmarshalJSON(data []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	type plain transferRecord
	if err := json.Unmarshal(data, (*plain)(t)); err != nil {
		return err
	}
	_, t.idPresent = probe["id"]
	return nil
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

// UnmarshalJSON 按现有格式解码一份本地样品数据，并对整份文件里的样品集合
// 与交接集合额外保证编号唯一。JSON 允许在同一对象里重复写出同名键：samples
// 或 transfers 内部重复编号会被标准库解码进 map 时静默覆盖，而文件顶层写出
// 两个同名字段时，标准库更是只保留最后一个字段的整张 map——仅靠解码后的
// map 既发现不了跨字段重复，还会丢掉先前字段保存的数量、保管信息或接收事实。
//
// 因此 samples 与 transfers 字段分别交给 samplesDecoder 与 transfersDecoder
// 逐个字段解码，二者的编号判重、空集合处理与同名字段记录合并共用同一份实现
// （见 mergeCollectionField，各自只保留区分样品或交接的报错说明）：
//   - 每个同名字段都在原始 JSON 上做一次键级扫描，编号按 JSON 解码后实际
//     表示的文字比较（合法 Unicode 转义与直接写出同一文字视为同一编号）；
//     判重集合跨所有同名字段共享，所以同一编号无论是在一个字段内出现两次，
//     还是分处两个同名字段、中间隔着其他顶层字段或其他编号的条目，都按
//     重复处理；
//   - 一旦重复，无论两条内容是否完全相同、是否指向同一样品、确认状态
//     如何，都返回包装了 ErrInvalid 的错误，写明重复的编号以及
//     “同一编号出现多次”的原因；
//   - 没有重复时，多个同名字段中的记录合并进同一张表，不同编号分别只
//     出现在不同字段中仍能按编号查询，不因字段重名而误判冲突。
//
// 扫描只统计集合对象自身的键，值整体跳过，所以子样来源、子样列表、交接
// 关联、保管历史说明或其他字段值文字中提到某编号不会被当作重复键。
func (l *ledger) UnmarshalJSON(raw []byte) error {
	var decoded struct {
		Version   int              `json:"version"`
		Samples   samplesDecoder   `json:"samples"`
		Transfers transfersDecoder `json:"transfers"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	l.Version = decoded.Version
	l.Samples = decoded.Samples.records
	l.Transfers = decoded.Transfers.records
	return nil
}

// collectionMergeRules 携带一类编号集合（样品或交接）在解码阶段特有的报错
// 说明：duplicateMessage 按该类记录各自的含义说明同一编号出现多次为何不能
// 恢复。共同的编号判重、空集合处理与同名字段合并逻辑只维护一份（见
// mergeCollectionField），两类集合只通过 rules 保留各自的错误说明。
type collectionMergeRules struct {
	duplicateMessage func(id string) error
}

// mergeCollectionField 处理文件顶层一个集合字段（samples 或 transfers）的值，
// 是“标准解码 + 编号判重 + 跨同名字段合并”这套共同逻辑的唯一实现，样品集合
// 与交接集合都只维护这一份：
//   - 先用标准解码完成语法、类型与记录字段校验：null 解码为空表，数组或标量
//     按标准类型错误拒绝；单条记录为 null 仍作为被占用编号保留在记录表里，
//     交给后续恢复核对按损坏数据处理；
//   - 再在原始 JSON 上按键扫描做唯一性判断（见 repeatedKey），判重集合 seen
//     跨文件中所有同名字段共享：同一编号无论在一个字段内重复，还是分处多个
//     同名字段、中间隔着其他顶层字段或其他编号条目，都判为重复。重复时绝不
//     覆盖、合并或挑选其中一条，而是通过 rules.duplicateMessage 返回包装了
//     ErrInvalid、且区分样品或交接含义的错误；
//   - 没有重复时把该字段的记录并入总表 records。字段值为 null 或空对象时
//     next 为空，直接返回而不动已有表，因此后出现的空对象或 null 既不会清空
//     前面字段已保存的记录，也不会让该字段之后可能出现的重复编号漏过
//     （判重集合的累积不受空集合影响）。
//
// encoding/json 对结构体值字段实现的 Unmarshaler 只保留一个实例，每个同名字段
// 都会在同一实例上调用一次 UnmarshalJSON（即使该值是 null；字段完全缺省时则
// 不调用，records 保持 nil）。
func mergeCollectionField[T any](data []byte, seen map[string]struct{}, records map[string]*T, rules collectionMergeRules) (map[string]*T, error) {
	var next map[string]*T
	if err := json.Unmarshal(data, &next); err != nil {
		return records, err
	}
	if dup, err := repeatedKey(data, seen); err != nil {
		return records, err
	} else if dup != "" {
		return records, rules.duplicateMessage(dup)
	}
	if len(next) == 0 {
		return records, nil
	}
	if records == nil {
		records = make(map[string]*T, len(next))
	}
	for k, v := range next {
		records[k] = v
	}
	return records, nil
}

var (
	// samplesMergeRules 给共同合并逻辑提供样品集合特有的报错说明：重复编号
	// 意味着无法确定应保留哪一份数量与保管信息。
	samplesMergeRules = collectionMergeRules{
		duplicateMessage: func(id string) error {
			return fmt.Errorf("%w: 样品编号 %q 在样品集合中出现多次，同一样品编号在整份本地数据中只能对应一条样品记录；重复编号无法确定应保留哪一份数量与保管信息，无法恢复",
				ErrInvalid, id)
		},
	}
	// transfersMergeRules 给共同合并逻辑提供交接集合特有的报错说明：重复编号
	// 意味着无法确定应保留哪一条接收事实或待确认信息。
	transfersMergeRules = collectionMergeRules{
		duplicateMessage: func(id string) error {
			return fmt.Errorf("%w: 交接编号 %q 在交接集合中出现多次，同一交接编号在整份本地数据中只能对应一条交接记录；重复编号无法确定应保留哪一条接收事实或待确认信息，无法恢复",
				ErrInvalid, id)
		},
	}
)

// samplesDecoder 逐字段接收文件顶层每一个 samples 值，解码规则与交接集合
// 共用同一份实现（见 mergeCollectionField），这里只传入样品集合特有的报错
// 说明（见 samplesMergeRules）。
type samplesDecoder struct {
	records map[string]*sampleRecord
	seen    map[string]struct{}
}

// UnmarshalJSON 处理单个 samples 字段值；编号判重、空集合处理与跨同名字段
// 合并全部走 mergeCollectionField，本方法不再单独维护这套逻辑。
func (sd *samplesDecoder) UnmarshalJSON(data []byte) error {
	if sd.seen == nil {
		sd.seen = make(map[string]struct{})
	}
	records, err := mergeCollectionField(data, sd.seen, sd.records, samplesMergeRules)
	sd.records = records
	return err
}

// transfersDecoder 逐字段接收文件顶层每一个 transfers 值，解码规则与样品集合
// 共用同一份实现（见 mergeCollectionField），这里只传入交接集合特有的报错
// 说明（见 transfersMergeRules）。
type transfersDecoder struct {
	records map[string]*transferRecord
	seen    map[string]struct{}
}

// UnmarshalJSON 处理单个 transfers 字段值；编号判重、空集合处理与跨同名字段
// 合并全部走 mergeCollectionField，本方法不再单独维护这套逻辑。
func (t *transfersDecoder) UnmarshalJSON(data []byte) error {
	if t.seen == nil {
		t.seen = make(map[string]struct{})
	}
	records, err := mergeCollectionField(data, t.seen, t.records, transfersMergeRules)
	t.records = records
	return err
}

// repeatedKey 扫描一个集合对象值（samples 或 transfers 的字段值），把解码后
// 的每个编号记入共享的 seen，并返回首个此前已在 seen 中出现过的编号；没有
// 重复时返回空字符串。Decoder.Token 返回的键已完成 Unicode 反转义，所以把
// 字符写成合法转义与直接写出同一文字仍按同一编号判重。data 已由调用方先用
// json.Unmarshal 校验为合法 JSON 对象（null 等非对象值没有键，直接放行）。
//
// 扫描只统计对象自身的键，值整体跳过，因此子样来源、子样列表、交接关联、
// 保管历史说明等字段值位置出现的编号文本不会被当作键。
func repeatedKey(data []byte, seen map[string]struct{}) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	first, err := dec.Token()
	if err != nil {
		return "", err
	}
	if d, ok := first.(json.Delim); !ok || d != '{' {
		return "", nil // null 等非对象值不含键，类型校验已由标准解码完成。
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
	// 消费对象的结束 '}'。
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
