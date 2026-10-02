package custody

import (
	"fmt"
	"strings"
	"time"
)

// InitiateHandover 发起一次交接。交出人和交出地点必须与样品当前记录一致，
// 接收人不能与交出人相同。交接转移该样品全部剩余量，发起后处于待确认，
// 原持有人和地点保持不变；同一样品已有待确认交接时不能再次发起或分装。
//
// 交接编号在本地数据中唯一。重复提交同一编号且内容完全相同的交出请求，
// 返回原交接（无论是否已确认）；沿用编号但改变样品、人员、地点或时间时拒绝。
func (s *Store) InitiateHandover(req HandoverRequest) (*HandoverView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}

	req.HandoverID = strings.TrimSpace(req.HandoverID)
	req.SampleID = strings.TrimSpace(req.SampleID)
	req.Giver = strings.TrimSpace(req.Giver)
	req.GiverLocation = strings.TrimSpace(req.GiverLocation)
	req.Receiver = strings.TrimSpace(req.Receiver)
	req.Destination = strings.TrimSpace(req.Destination)

	if req.HandoverID == "" {
		return nil, fmt.Errorf("%w: 交接编号为空", ErrInvalidInput)
	}
	if req.SampleID == "" {
		return nil, fmt.Errorf("%w: 样品编号为空", ErrInvalidInput)
	}
	if req.Giver == "" {
		return nil, fmt.Errorf("%w: 交出人为空", ErrInvalidInput)
	}
	if req.GiverLocation == "" {
		return nil, fmt.Errorf("%w: 交出地点为空", ErrInvalidInput)
	}
	if req.Receiver == "" {
		return nil, fmt.Errorf("%w: 接收人为空", ErrInvalidInput)
	}
	if req.Destination == "" {
		return nil, fmt.Errorf("%w: 目的地点为空", ErrInvalidInput)
	}
	if req.HandoverTime.IsZero() {
		return nil, fmt.Errorf("%w: 交出时间为零", ErrInvalidInput)
	}
	if req.Giver == req.Receiver {
		return nil, fmt.Errorf("%w: 接收人不能与交出人相同", ErrInvalidInput)
	}

	// 交接编号幂等：同编号同内容返回原交接，改内容拒绝。
	if existing, ok := s.handovers[req.HandoverID]; ok {
		if existing.SampleID == req.SampleID &&
			existing.Giver == req.Giver &&
			existing.GiverLocation == req.GiverLocation &&
			existing.Receiver == req.Receiver &&
			existing.Destination == req.Destination &&
			existing.HandoverTime.Equal(req.HandoverTime) {
			return handoverView(existing), nil
		}
		return nil, fmt.Errorf("%w: 交接编号已被不同内容使用: %s", ErrConflict, req.HandoverID)
	}

	smp, ok := s.samples[req.SampleID]
	if !ok {
		return nil, fmt.Errorf("%w: 样品不存在: %s", ErrNotFound, req.SampleID)
	}
	if smp.RemainingQty <= 0 {
		return nil, fmt.Errorf("%w: 样品剩余量为零，不能交接: %s", ErrConflict, req.SampleID)
	}
	if smp.PendingHandover != "" {
		return nil, fmt.Errorf("%w: 样品已有待确认交接: %s", ErrConflict, req.SampleID)
	}
	if smp.Holder != req.Giver {
		return nil, fmt.Errorf("%w: 交出人 %q 与当前持有人 %q 不一致", ErrConflict, req.Giver, smp.Holder)
	}
	if smp.Location != req.GiverLocation {
		return nil, fmt.Errorf("%w: 交出地点 %q 与当前地点 %q 不一致", ErrConflict, req.GiverLocation, smp.Location)
	}

	h := &Handover{
		ID:            req.HandoverID,
		SampleID:      req.SampleID,
		Giver:         req.Giver,
		GiverLocation: req.GiverLocation,
		Receiver:      req.Receiver,
		Destination:   req.Destination,
		HandoverTime:  req.HandoverTime,
	}
	smp.PendingHandover = h.ID
	smp.History = append(smp.History, HistoryEvent{
		Seq:        s.seq(),
		Type:       eventHandoverInitiated,
		Time:       req.HandoverTime,
		HandoverID: h.ID,
		From:       req.Giver,
		To:         req.Receiver,
		Location:   req.Destination,
	})
	s.handovers[h.ID] = h

	if err := s.persist(); err != nil {
		smp.PendingHandover = ""
		smp.History = smp.History[:len(smp.History)-1]
		delete(s.handovers, h.ID)
		return nil, fmt.Errorf("custody: 落盘失败，交接已回滚: %w", err)
	}
	return handoverView(h), nil
}

// ConfirmHandover 由指定接收人在目的地点确认接收，接收时间不能早于交出时间。
// 确认后才改变持有人和地点，并结束待确认状态。
//
// 已确认的交接重复提交相同接收信息仍返回原结果，不增加转手记录；
// 接收信息不同则拒绝。
func (s *Store) ConfirmHandover(handoverID, receiver, destination string, receiveTime time.Time) (*HandoverView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}

	handoverID = strings.TrimSpace(handoverID)
	receiver = strings.TrimSpace(receiver)
	destination = strings.TrimSpace(destination)
	if handoverID == "" {
		return nil, fmt.Errorf("%w: 交接编号为空", ErrInvalidInput)
	}
	if receiver == "" {
		return nil, fmt.Errorf("%w: 接收人为空", ErrInvalidInput)
	}
	if destination == "" {
		return nil, fmt.Errorf("%w: 目的地点为空", ErrInvalidInput)
	}
	if receiveTime.IsZero() {
		return nil, fmt.Errorf("%w: 接收时间为零", ErrInvalidInput)
	}

	h, ok := s.handovers[handoverID]
	if !ok {
		return nil, fmt.Errorf("%w: 交接不存在: %s", ErrNotFound, handoverID)
	}

	// 已确认：相同接收信息幂等返回原结果，不同则拒绝。
	if h.Confirmed {
		if h.Receiver == receiver && h.Destination == destination && h.ReceiveTime.Equal(receiveTime) {
			return handoverView(h), nil
		}
		return nil, fmt.Errorf("%w: 交接已确认，接收信息不一致: %s", ErrConflict, handoverID)
	}

	if h.Receiver != receiver {
		return nil, fmt.Errorf("%w: 只有指定接收人 %q 能确认，收到 %q", ErrConflict, h.Receiver, receiver)
	}
	if h.Destination != destination {
		return nil, fmt.Errorf("%w: 必须在目的地点 %q 确认，收到 %q", ErrConflict, h.Destination, destination)
	}
	if receiveTime.Before(h.HandoverTime) {
		return nil, fmt.Errorf("%w: 接收时间 %s 早于交出时间 %s",
			ErrConflict, receiveTime.Format(time.RFC3339), h.HandoverTime.Format(time.RFC3339))
	}

	smp, ok := s.samples[h.SampleID]
	if !ok {
		return nil, fmt.Errorf("%w: 样品不存在: %s", ErrNotFound, h.SampleID)
	}

	smpClone := cloneSample(smp)
	oldHolder := smp.Holder

	h.Confirmed = true
	h.ReceiveTime = receiveTime
	smp.Holder = receiver
	smp.Location = destination
	smp.PendingHandover = ""
	smp.History = append(smp.History, HistoryEvent{
		Seq:        s.seq(),
		Type:       eventHandoverConfirmed,
		Time:       receiveTime,
		HandoverID: h.ID,
		From:       oldHolder,
		To:         receiver,
		Location:   destination,
	})

	if err := s.persist(); err != nil {
		s.samples[h.SampleID] = smpClone
		h.Confirmed = false
		h.ReceiveTime = time.Time{}
		return nil, fmt.Errorf("custody: 落盘失败，确认已回滚: %w", err)
	}
	return handoverView(h), nil
}

// GetHandover 按交接编号查询。不存在时返回 ErrNotFound，不会顺带创建记录。
func (s *Store) GetHandover(handoverID string) (*HandoverView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	handoverID = strings.TrimSpace(handoverID)
	h, ok := s.handovers[handoverID]
	if !ok {
		return nil, fmt.Errorf("%w: 交接不存在: %s", ErrNotFound, handoverID)
	}
	return handoverView(h), nil
}
