package custody

import "time"

// 保管历史事件类型。
const (
	eventRegistered        = "registered"         // 登记入库
	eventAliquotOut        = "aliquot_out"        // 分装分出（父样）
	eventAliquotIn         = "aliquot_in"         // 分装分得（子样）
	eventHandoverInitiated = "handover_initiated" // 发起交接（待确认）
	eventHandoverConfirmed = "handover_confirmed" // 交接确认接收
)

// HistoryEvent 是一条按发生顺序排列的保管历史。
type HistoryEvent struct {
	Seq        int64     `json:"seq"`                  // 单调递增序号，决定排列顺序
	Type       string    `json:"type"`                 // 事件类型，见 event* 常量
	Time       time.Time `json:"time,omitempty"`       // 事件时间（交接事件使用）
	Qty        int64     `json:"qty,omitempty"`        // 涉及数量（千分之一毫升）
	SampleID   string    `json:"sampleId,omitempty"`   // 关联的另一方样品编号
	HandoverID string    `json:"handoverId,omitempty"` // 关联的交接编号
	From       string    `json:"from,omitempty"`       // 交出人 / 原持有人
	To         string    `json:"to,omitempty"`         // 接收人 / 新持有人
	Location   string    `json:"location,omitempty"`   // 地点
}

// Sample 是一份样品（原样或子样）。
type Sample struct {
	ID              string         `json:"id"`
	InitialQty      int64          `json:"initialQty"`                // 初始量（千分之一毫升）
	RemainingQty    int64          `json:"remainingQty"`              // 剩余量（千分之一毫升）
	Holder          string         `json:"holder"`                    // 当前持有人
	Location        string         `json:"location"`                  // 当前地点
	ParentID        string         `json:"parentId,omitempty"`        // 父样编号，原样为空
	Children        []string       `json:"children"`                  // 子样编号列表
	History         []HistoryEvent `json:"history"`                   // 保管历史（按发生顺序）
	PendingHandover string         `json:"pendingHandover,omitempty"` // 待确认交接编号，空表示无
}

// Handover 是一次交接记录。
type Handover struct {
	ID            string    `json:"id"`
	SampleID      string    `json:"sampleId"`
	Giver         string    `json:"giver"`
	GiverLocation string    `json:"giverLocation"`
	Receiver      string    `json:"receiver"`
	Destination   string    `json:"destination"`
	HandoverTime  time.Time `json:"handoverTime"`
	ReceiveTime   time.Time `json:"receiveTime,omitempty"`
	Confirmed     bool      `json:"confirmed"`
}

// storeState 是落盘的完整数据。
type storeState struct {
	Version   int         `json:"version"`
	NextSeq   int64       `json:"nextSeq"`
	Samples   []*Sample   `json:"samples"`
	Handovers []*Handover `json:"handovers"`
}

// HandoverRequest 是发起交接的请求。
type HandoverRequest struct {
	HandoverID    string    // 交接编号（本地唯一）
	SampleID      string    // 要交接的样品编号
	Giver         string    // 交出人，必须与样品当前持有人一致
	GiverLocation string    // 交出地点，必须与样品当前地点一致
	Receiver      string    // 指定接收人，不能与交出人相同
	Destination   string    // 目的地点
	HandoverTime  time.Time // 交出时间
}

// Aliquot 是一个子样分装项。
type Aliquot struct {
	SampleID string // 子样独立编号
	Quantity string // 子样数量（毫升，最多三位小数）
}
