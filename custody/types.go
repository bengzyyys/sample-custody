package custody

import (
	"errors"
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
}

// History 是样品保管历史中的一条记录，按发生顺序追加。
type History struct {
	// Kind 为 "register"、"split"、"transfer-out" 或 "transfer-in"。
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
