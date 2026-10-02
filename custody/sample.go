package custody

import (
	"fmt"
	"math"
	"strings"
)

// Register 登记一份原样。sampleID 在这份数据内必须唯一；编号、持有人、
// 地点去掉首尾空白后不能为空；quantity 只接受大于零、最多三位小数的十进制值。
func (s *Store) Register(sampleID, quantity, holder, location string) (*SampleView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}

	sampleID = strings.TrimSpace(sampleID)
	holder = strings.TrimSpace(holder)
	location = strings.TrimSpace(location)
	if sampleID == "" {
		return nil, fmt.Errorf("%w: 样品编号为空", ErrInvalidInput)
	}
	if holder == "" {
		return nil, fmt.Errorf("%w: 持有人为空", ErrInvalidInput)
	}
	if location == "" {
		return nil, fmt.Errorf("%w: 地点为空", ErrInvalidInput)
	}
	qty, err := parseQuantity(quantity)
	if err != nil {
		return nil, err
	}
	if _, exists := s.samples[sampleID]; exists {
		return nil, fmt.Errorf("%w: 样品编号已存在: %s", ErrDuplicateID, sampleID)
	}

	smp := &Sample{
		ID:           sampleID,
		InitialQty:   qty,
		RemainingQty: qty,
		Holder:       holder,
		Location:     location,
		Children:     []string{},
		History: []HistoryEvent{{
			Seq:      s.seq(),
			Type:     eventRegistered,
			Qty:      qty,
			From:     holder,
			Location: location,
		}},
	}
	s.samples[sampleID] = smp

	if err := s.persist(); err != nil {
		delete(s.samples, sampleID)
		return nil, fmt.Errorf("custody: 落盘失败，登记已回滚: %w", err)
	}
	return s.sampleView(smp), nil
}

// Aliquot 把来源样品的剩余量分成一个或多个子样。任一子样编号重复或数量
// 不合法，整次分装都不生效；总量不得超过来源样品的剩余量。成功后扣减
// 来源样品剩余量，子样继承当时的持有人和地点，并可继续分装。
func (s *Store) Aliquot(sourceID string, subs []Aliquot) (*SampleView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}

	sourceID = strings.TrimSpace(sourceID)
	source, ok := s.samples[sourceID]
	if !ok {
		return nil, fmt.Errorf("%w: 来源样品不存在: %s", ErrNotFound, sourceID)
	}
	if source.RemainingQty <= 0 {
		return nil, fmt.Errorf("%w: 来源样品剩余量为零，不能分装: %s", ErrConflict, sourceID)
	}
	if source.PendingHandover != "" {
		return nil, fmt.Errorf("%w: 来源样品有待确认交接，不能分装: %s", ErrConflict, sourceID)
	}
	if len(subs) == 0 {
		return nil, fmt.Errorf("%w: 未指定子样", ErrInvalidInput)
	}

	type parsedSub struct {
		id  string
		qty int64
	}
	parsed := make([]parsedSub, 0, len(subs))
	total := int64(0)
	seen := make(map[string]bool, len(subs))
	for _, sub := range subs {
		id := strings.TrimSpace(sub.SampleID)
		if id == "" {
			return nil, fmt.Errorf("%w: 子样编号为空", ErrInvalidInput)
		}
		if seen[id] {
			return nil, fmt.Errorf("%w: 同一批次中子样编号重复: %s", ErrDuplicateID, id)
		}
		seen[id] = true
		if _, exists := s.samples[id]; exists {
			return nil, fmt.Errorf("%w: 子样编号已存在: %s", ErrDuplicateID, id)
		}
		qty, err := parseQuantity(sub.Quantity)
		if err != nil {
			return nil, err
		}
		if total > math.MaxInt64-qty {
			return nil, fmt.Errorf("%w: 子样总量过大", ErrInvalidQuantity)
		}
		total += qty
		parsed = append(parsed, parsedSub{id: id, qty: qty})
	}
	if total > source.RemainingQty {
		return nil, fmt.Errorf("%w: 子样总量 %s 超过来源剩余量 %s",
			ErrConflict, formatQuantity(total), formatQuantity(source.RemainingQty))
	}

	// 快照用于落盘失败时回滚。
	sourceClone := cloneSample(source)
	created := make([]*Sample, 0, len(parsed))

	for _, p := range parsed {
		child := &Sample{
			ID:           p.id,
			InitialQty:   p.qty,
			RemainingQty: p.qty,
			Holder:       source.Holder,
			Location:     source.Location,
			ParentID:     source.ID,
			Children:     []string{},
			History: []HistoryEvent{{
				Seq:      s.seq(),
				Type:     eventAliquotIn,
				Qty:      p.qty,
				SampleID: source.ID,
				From:     source.Holder,
				Location: source.Location,
			}},
		}
		s.samples[p.id] = child
		created = append(created, child)
		source.Children = append(source.Children, p.id)
		source.History = append(source.History, HistoryEvent{
			Seq:      s.seq(),
			Type:     eventAliquotOut,
			Qty:      p.qty,
			SampleID: p.id,
		})
	}
	source.RemainingQty -= total

	if err := s.persist(); err != nil {
		s.samples[sourceID] = sourceClone
		for _, c := range created {
			delete(s.samples, c.ID)
		}
		return nil, fmt.Errorf("custody: 落盘失败，分装已回滚: %w", err)
	}
	return s.sampleView(source), nil
}

// GetSample 按编号查询样品。不存在时返回 ErrNotFound，不会顺带创建记录。
func (s *Store) GetSample(sampleID string) (*SampleView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	sampleID = strings.TrimSpace(sampleID)
	smp, ok := s.samples[sampleID]
	if !ok {
		return nil, fmt.Errorf("%w: 样品不存在: %s", ErrNotFound, sampleID)
	}
	return s.sampleView(smp), nil
}
