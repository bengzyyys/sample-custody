package custody

import "time"

// SampleView 是样品的对外视图，数量统一显示三位小数。
type SampleView struct {
	ID              string         `json:"id"`
	InitialQty      string         `json:"initialQty"`      // 初始量（三位小数）
	RemainingQty    string         `json:"remainingQty"`    // 剩余量（三位小数）
	Holder          string         `json:"holder"`          // 当前持有人
	Location        string         `json:"location"`        // 当前地点
	ParentID        string         `json:"parentId"`        // 父样编号，原样为空
	Children        []string       `json:"children"`        // 子样编号
	History         []HistoryEvent `json:"history"`         // 保管历史（按发生顺序）
	PendingHandover *HandoverView  `json:"pendingHandover"` // 待确认交接详情，无则为 nil
}

// HandoverView 是交接的对外视图。
type HandoverView struct {
	ID            string    `json:"id"`
	SampleID      string    `json:"sampleId"`
	Giver         string    `json:"giver"`
	GiverLocation string    `json:"giverLocation"`
	Receiver      string    `json:"receiver"`
	Destination   string    `json:"destination"`
	HandoverTime  time.Time `json:"handoverTime"`
	ReceiveTime   time.Time `json:"receiveTime"`
	Confirmed     bool      `json:"confirmed"`
}

// handoverView 把内部交接转为对外视图。调用方必须持有锁。
func handoverView(h *Handover) *HandoverView {
	return &HandoverView{
		ID:            h.ID,
		SampleID:      h.SampleID,
		Giver:         h.Giver,
		GiverLocation: h.GiverLocation,
		Receiver:      h.Receiver,
		Destination:   h.Destination,
		HandoverTime:  h.HandoverTime,
		ReceiveTime:   h.ReceiveTime,
		Confirmed:     h.Confirmed,
	}
}

// sampleView 把内部样品转为对外视图。调用方必须持有锁。
func (s *Store) sampleView(smp *Sample) *SampleView {
	v := &SampleView{
		ID:           smp.ID,
		InitialQty:   formatQuantity(smp.InitialQty),
		RemainingQty: formatQuantity(smp.RemainingQty),
		Holder:       smp.Holder,
		Location:     smp.Location,
		ParentID:     smp.ParentID,
		Children:     append([]string(nil), smp.Children...),
		History:      append([]HistoryEvent(nil), smp.History...),
	}
	if smp.PendingHandover != "" {
		if h, ok := s.handovers[smp.PendingHandover]; ok {
			v.PendingHandover = handoverView(h)
		}
	}
	return v
}
