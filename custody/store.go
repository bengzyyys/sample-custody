package custody

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store 是一份本地样品数据的入口。零值不可用，请通过 Open 创建或重新打开。
//
// 同一时刻所有读写操作都由一把互斥锁串行化，因此并发操作同一样品时，
// 既不会超量分装，也不会出现两条待确认交接。每次成功的写操作都会
// 原子落盘（临时文件 + rename），关闭后重新打开同一文件即可恢复
// 全部样品、保管历史以及交接编号的重复提交判断。
type Store struct {
	path string
	mu   sync.Mutex
	data *ledger

	// now 可在同包测试中替换，便于确定性地控制事件时间。
	now func() time.Time
}

// Open 打开 path 指向的本地样品数据：文件不存在时创建一份新数据，
// 已存在时原样重新打开，历史记录与交接编号判断继续有效。
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: 数据文件路径不能为空", ErrInvalid)
	}

	s := &Store{path: path, now: time.Now}

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var l ledger
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, fmt.Errorf("读取本地样品数据 %s 失败: %w", path, err)
		}
		if l.Version != 1 {
			return nil, fmt.Errorf("本地样品数据 %s 的版本 %d 不受支持", path, l.Version)
		}
		if l.Samples == nil {
			l.Samples = make(map[string]*sampleRecord)
		}
		if l.Transfers == nil {
			l.Transfers = make(map[string]*transferRecord)
		}
		if err := validateRestored(&l); err != nil {
			return nil, err
		}
		s.data = &l
	case os.IsNotExist(err):
		s.data = newLedger()
		if err := s.persist(s.data); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("打开本地样品数据 %s 失败: %w", path, err)
	}
	return s, nil
}

// validateRestored 校验从文件恢复的数据中待确认交接关联是否一致。
// 样品的非空 pendingId 必须指向一条实际存在、尚未确认、且属于该样品的
// 交接；每条尚未确认的交接也必须对应一份实际存在、且 pendingId 正好指向
// 它的样品。待确认交接表示该样品当前全部剩余量尚待指定人员接收，因此
// 还必须与样品当前记录一致：交出人等于当前持有人、交出地点等于当前地点、
// 交接量精确等于当前剩余量，且样品未销毁、剩余量大于零。已确认交接属于
// 保留的历史，不要求样品继续指向它，也不核对其与样品当前持有人、地点或
// 剩余量的差异。集合中占用编号却为 null 的条目视为损坏，不能当作记录不
// 存在。任一问题都使整个文件无法恢复，绝不只加载其中一部分，也不改动
// 原文件。
func validateRestored(l *ledger) error {
	sampleIDs := make([]string, 0, len(l.Samples))
	for id := range l.Samples {
		sampleIDs = append(sampleIDs, id)
	}
	sort.Strings(sampleIDs)
	for _, id := range sampleIDs {
		rec := l.Samples[id]
		if rec == nil {
			return fmt.Errorf("%w: 样品编号 %q 已被占用但记录为 null，无法恢复", ErrInvalid, id)
		}
		if rec.PendingID == "" {
			continue
		}
		tr, ok := l.Transfers[rec.PendingID]
		switch {
		case !ok:
			return fmt.Errorf("%w: 样品 %q 记着待确认交接 %q，但该交接不存在，无法恢复",
				ErrInvalid, id, rec.PendingID)
		case tr == nil:
			return fmt.Errorf("%w: 样品 %q 记着待确认交接 %q，但该交接记录为 null，无法恢复",
				ErrInvalid, id, rec.PendingID)
		case tr.Confirmed:
			return fmt.Errorf("%w: 样品 %q 记着的交接 %q 已确认，不能仍是待确认，无法恢复",
				ErrInvalid, id, rec.PendingID)
		case tr.SampleID != id:
			return fmt.Errorf("%w: 样品 %q 记着的待确认交接 %q 属于样品 %q，无法恢复",
				ErrInvalid, id, rec.PendingID, tr.SampleID)
		}
		if err := validatePendingConsistency(id, rec, tr); err != nil {
			return err
		}
	}

	transferIDs := make([]string, 0, len(l.Transfers))
	for id := range l.Transfers {
		transferIDs = append(transferIDs, id)
	}
	sort.Strings(transferIDs)
	for _, id := range transferIDs {
		rec := l.Transfers[id]
		if rec == nil {
			return fmt.Errorf("%w: 交接编号 %q 已被占用但记录为 null，无法恢复", ErrInvalid, id)
		}
		if rec.Confirmed {
			continue
		}
		sample, ok := l.Samples[rec.SampleID]
		switch {
		case !ok:
			return fmt.Errorf("%w: 待确认交接 %q 对应的样品 %q 不存在，无法恢复",
				ErrInvalid, id, rec.SampleID)
		case sample == nil:
			return fmt.Errorf("%w: 待确认交接 %q 对应的样品 %q 记录为 null，无法恢复",
				ErrInvalid, id, rec.SampleID)
		case sample.PendingID != id:
			return fmt.Errorf("%w: 待确认交接 %q 的样品 %q 并未记着该交接编号，无法恢复",
				ErrInvalid, id, rec.SampleID)
		}
	}
	return nil
}

// validatePendingConsistency 核对一条双向关联成立的待确认交接是否与样品
// 当前记录矛盾。待确认交接转移的是该样品当前全部剩余量，所以交出人、
// 交出地点、交接量必须分别与样品的当前持有人、当前地点、当前剩余量
// 完全一致；样品还必须未销毁且仍有剩余量。任一条件不成立都返回包装了
// ErrInvalid 的错误，指明样品编号、交接编号与冲突项；数量冲突会同时
// 给出交接记录与样品当前的毫升数（三位小数）。
func validatePendingConsistency(sampleID string, sample *sampleRecord, tr *transferRecord) error {
	if sample.Destroyed != nil {
		return fmt.Errorf("%w: 样品 %q 的待确认交接 %q 仍未确认，但样品已销毁，无法恢复",
			ErrInvalid, sampleID, tr.ID)
	}
	if sample.Remaining <= 0 {
		return fmt.Errorf("%w: 样品 %q 的待确认交接 %q 仍未确认，但样品当前剩余量为 %s 毫升，无法恢复",
			ErrInvalid, sampleID, tr.ID, formatUnits(sample.Remaining))
	}
	if tr.FromHolder != sample.Holder {
		return fmt.Errorf("%w: 待确认交接 %q 的交出人 %q 与样品 %q 当前持有人 %q 不一致，无法恢复",
			ErrInvalid, tr.ID, tr.FromHolder, sampleID, sample.Holder)
	}
	if tr.FromLocation != sample.Location {
		return fmt.Errorf("%w: 待确认交接 %q 的交出地点 %q 与样品 %q 当前地点 %q 不一致，无法恢复",
			ErrInvalid, tr.ID, tr.FromLocation, sampleID, sample.Location)
	}
	if tr.Qty != sample.Remaining {
		return fmt.Errorf("%w: 待确认交接 %q 记录的交接量 %s 毫升与样品 %q 当前剩余量 %s 毫升不一致，无法恢复",
			ErrInvalid, tr.ID, formatUnits(tr.Qty), sampleID, formatUnits(sample.Remaining))
	}
	return nil
}

// persist 把整份状态原子写入文件：先写同目录临时文件，再 rename 覆盖，
// 避免进程中断留下半截数据。
func (s *Store) persist(l *ledger) error {
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("编码本地样品数据失败: %w", err)
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".custody-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时数据文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时数据文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时数据文件失败: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("替换本地样品数据文件失败: %w", err)
	}
	return nil
}

// commit 在候选状态上落盘并替换内存状态。调用方必须保证 candidate
// 已通过全部校验；落盘失败时内存状态保持不变。
func (s *Store) commit(candidate *ledger) error {
	if err := s.persist(candidate); err != nil {
		return err
	}
	s.data = candidate
	return nil
}

// RegisterInput 是登记原样的入参。
type RegisterInput struct {
	// ID 为样品编号，在这份数据内唯一，去空白后非空。
	ID string
	// Qty 为初始数量（毫升），大于零、最多三位小数的十进制字符串。
	Qty string
	// Holder 为初始持有人，去空白后非空。
	Holder string
	// Location 为初始地点，去空白后非空。
	Location string
}

// Register 登记一份原样。编号重复等冲突返回包装了 ErrConflict 的错误，
// 且不会写入任何记录。
func (s *Store) Register(in RegisterInput) (*Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, err := trimRequired("样品编号", in.ID)
	if err != nil {
		return nil, err
	}
	holder, err := trimRequired("持有人", in.Holder)
	if err != nil {
		return nil, err
	}
	location, err := trimRequired("地点", in.Location)
	if err != nil {
		return nil, err
	}
	qty, err := parseQuantity(in.Qty)
	if err != nil {
		return nil, err
	}
	if _, exists := s.data.Samples[id]; exists {
		return nil, fmt.Errorf("%w: 样品编号 %q 已存在", ErrConflict, id)
	}

	candidate := s.data.deepCopy()
	now := s.now().UTC()
	candidate.Samples[id] = &sampleRecord{
		ID:        id,
		Initial:   qty,
		Remaining: qty,
		Holder:    holder,
		Location:  location,
		Children:  []string{},
		History: []historyRecord{{
			Kind:     "register",
			Time:     now,
			Holder:   holder,
			Location: location,
			Detail:   "登记原样",
		}},
	}
	if err := s.commit(candidate); err != nil {
		return nil, err
	}
	return buildSampleView(candidate.Samples[id], candidate), nil
}

// SplitPart 是一次分装中的一个子样。
type SplitPart struct {
	// ID 为子样编号，在这份数据内唯一，去空白后非空。
	ID string
	// Qty 为该子样数量（毫升），大于零、最多三位小数。
	Qty string
}

// SplitInput 是一次分装的入参；一次分装可以同时创建多个子样。
type SplitInput struct {
	// ParentID 为来源样品编号。
	ParentID string
	// Parts 为本次要创建的全部子样，至少一个。
	Parts []SplitPart
}

// Split 执行一次原子分装。任一子样编号重复、数量不合法或子样总量超过
// 来源样品剩余量时，整次分装都不生效。成功后来源样品按整数刻度精确
// 扣减，子样继承当时的持有人和地点，并可继续分装。
//
// 返回来源样品的最新视图；子样可通过 GetSample 分别查询。
func (s *Store) Split(in SplitInput) (*Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	parentID, err := trimRequired("来源样品编号", in.ParentID)
	if err != nil {
		return nil, err
	}
	if len(in.Parts) == 0 {
		return nil, fmt.Errorf("%w: 一次分装至少要创建一个子样", ErrInvalid)
	}

	parent, ok := s.data.Samples[parentID]
	if !ok {
		return nil, fmt.Errorf("%w: 来源样品 %q 不存在", ErrNotFound, parentID)
	}

	// 先在局部完成全部校验，任何一项不通过都直接返回，不动状态。
	type parsedPart struct {
		id  string
		qty int64
	}
	parsed := make([]parsedPart, 0, len(in.Parts))
	seen := make(map[string]struct{}, len(in.Parts))
	var total int64
	for _, p := range in.Parts {
		id, err := trimRequired("子样编号", p.ID)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("%w: 本次分装中子样编号 %q 重复", ErrInvalid, id)
		}
		if _, exists := s.data.Samples[id]; exists {
			return nil, fmt.Errorf("%w: 子样编号 %q 已存在", ErrConflict, id)
		}
		if id == parentID {
			return nil, fmt.Errorf("%w: 子样编号 %q 不能与来源样品相同", ErrInvalid, id)
		}
		qty, err := parseQuantity(p.Qty)
		if err != nil {
			return nil, err
		}
		seen[id] = struct{}{}
		parsed = append(parsed, parsedPart{id: id, qty: qty})
		total += qty
		if total < 0 {
			return nil, fmt.Errorf("%w: 子样总量超出可表示范围", ErrInvalid)
		}
	}

	if parent.Destroyed != nil {
		return nil, fmt.Errorf("%w: 样品 %q 已销毁，不能再分装", ErrConflict, parentID)
	}
	if parent.PendingID != "" {
		return nil, fmt.Errorf("%w: 样品 %q 存在待确认交接 %s，不能分装",
			ErrConflict, parentID, parent.PendingID)
	}
	if parent.Remaining == 0 {
		return nil, fmt.Errorf("%w: 样品 %q 剩余量为零，不能再分装", ErrConflict, parentID)
	}
	if total > parent.Remaining {
		return nil, fmt.Errorf("%w: 子样总量 %s 毫升超过样品 %q 的剩余量 %s 毫升",
			ErrConflict, formatUnits(total), parentID, formatUnits(parent.Remaining))
	}

	candidate := s.data.deepCopy()
	cpParent := candidate.Samples[parentID]
	now := s.now().UTC()

	childIDs := make([]string, 0, len(parsed))
	for _, p := range parsed {
		childIDs = append(childIDs, p.id)
		candidate.Samples[p.id] = &sampleRecord{
			ID:        p.id,
			ParentID:  parentID,
			Initial:   p.qty,
			Remaining: p.qty,
			Holder:    cpParent.Holder,
			Location:  cpParent.Location,
			Children:  []string{},
			History: []historyRecord{{
				Kind:     "split",
				Time:     now,
				Holder:   cpParent.Holder,
				Location: cpParent.Location,
				Detail:   fmt.Sprintf("由样品 %s 分装", parentID),
			}},
		}
	}
	cpParent.Remaining -= total
	cpParent.Children = append(cpParent.Children, childIDs...)
	cpParent.History = append(cpParent.History, historyRecord{
		Kind:     "split",
		Time:     now,
		Holder:   cpParent.Holder,
		Location: cpParent.Location,
		Detail:   fmt.Sprintf("分装创建子样 %s", strings.Join(childIDs, ", ")),
	})

	if err := s.commit(candidate); err != nil {
		return nil, err
	}
	return buildSampleView(cpParent, candidate), nil
}

// HandoverInput 是发起交接（交出）的入参。
type HandoverInput struct {
	// TransferID 为本次交接编号，在本地数据中唯一，去空白后非空。
	TransferID string
	// SampleID 为要交出的样品编号。
	SampleID string
	// FromHolder 为交出人，必须与样品当前持有人完全一致（比较前对入参去空白）。
	FromHolder string
	// FromLocation 为交出地点，必须与样品当前地点完全一致。
	FromLocation string
	// ToHolder 为指定接收人，不能与交出人相同。
	ToHolder string
	// ToLocation 为目的地点。
	ToLocation string
	// HandedOverAt 为交出时间。
	HandedOverAt time.Time
}

// Handover 发起一次交接：转移该样品的全部剩余量，交接进入待确认状态，
// 原持有人和地点保持不变，直到指定接收人在目的地点确认。
//
// 交接编号已存在时：内容（样品、人员、地点、时间）完全一致则原样返回
// 既有交接（无论是否已确认）；任一内容不同则拒绝。
func (s *Store) Handover(in HandoverInput) (*TransferView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	transferID, err := trimRequired("交接编号", in.TransferID)
	if err != nil {
		return nil, err
	}
	sampleID, err := trimRequired("样品编号", in.SampleID)
	if err != nil {
		return nil, err
	}
	fromHolder, err := trimRequired("交出人", in.FromHolder)
	if err != nil {
		return nil, err
	}
	fromLocation, err := trimRequired("交出地点", in.FromLocation)
	if err != nil {
		return nil, err
	}
	toHolder, err := trimRequired("接收人", in.ToHolder)
	if err != nil {
		return nil, err
	}
	toLocation, err := trimRequired("目的地点", in.ToLocation)
	if err != nil {
		return nil, err
	}
	if in.HandedOverAt.IsZero() {
		return nil, fmt.Errorf("%w: 交出时间不能为空", ErrInvalid)
	}
	handedAt := in.HandedOverAt.UTC()

	if existing, ok := s.data.Transfers[transferID]; ok {
		if !sameHandoverRequest(existing, sampleID, fromHolder, fromLocation, toHolder, toLocation, handedAt) {
			return nil, fmt.Errorf("%w: 交接编号 %q 已被内容不同的交接使用", ErrConflict, transferID)
		}
		// 完全相同的重复提交：返回原交接，不增加任何记录。
		return buildTransferView(existing), nil
	}

	sample, ok := s.data.Samples[sampleID]
	if !ok {
		return nil, fmt.Errorf("%w: 样品 %q 不存在", ErrNotFound, sampleID)
	}
	if sample.Destroyed != nil {
		return nil, fmt.Errorf("%w: 样品 %q 已销毁，不能发起新的交接", ErrConflict, sampleID)
	}
	if sample.Remaining == 0 {
		return nil, fmt.Errorf("%w: 样品 %q 剩余量为零，不能发起交接", ErrConflict, sampleID)
	}
	if sample.PendingID != "" {
		return nil, fmt.Errorf("%w: 样品 %q 已存在待确认交接 %s",
			ErrConflict, sampleID, sample.PendingID)
	}
	if sample.Holder != fromHolder {
		return nil, fmt.Errorf("%w: 交出人 %q 与样品 %q 当前持有人 %q 不一致",
			ErrInvalid, fromHolder, sampleID, sample.Holder)
	}
	if sample.Location != fromLocation {
		return nil, fmt.Errorf("%w: 交出地点 %q 与样品 %q 当前地点 %q 不一致",
			ErrInvalid, fromLocation, sampleID, sample.Location)
	}
	if toHolder == fromHolder {
		return nil, fmt.Errorf("%w: 接收人不能与交出人 %q 相同", ErrInvalid, fromHolder)
	}

	candidate := s.data.deepCopy()
	rec := &transferRecord{
		ID:           transferID,
		SampleID:     sampleID,
		FromHolder:   fromHolder,
		FromLocation: fromLocation,
		ToHolder:     toHolder,
		ToLocation:   toLocation,
		Qty:          sample.Remaining,
		HandedOverAt: handedAt,
	}
	candidate.Transfers[transferID] = rec
	cpSample := candidate.Samples[sampleID]
	cpSample.PendingID = transferID
	cpSample.History = append(cpSample.History, historyRecord{
		Kind:     "transfer-out",
		Time:     handedAt,
		Holder:   fromHolder,
		Location: fromLocation,
		Detail:   fmt.Sprintf("发起交接 %s，待 %s 在 %s 接收", transferID, toHolder, toLocation),
	})

	if err := s.commit(candidate); err != nil {
		return nil, err
	}
	return buildTransferView(rec), nil
}

// sameHandoverRequest 判断已存交接与本次请求的内容是否完全相同。
func sameHandoverRequest(t *transferRecord, sampleID, fromHolder, fromLocation, toHolder, toLocation string, handedAt time.Time) bool {
	return t.SampleID == sampleID &&
		t.FromHolder == fromHolder &&
		t.FromLocation == fromLocation &&
		t.ToHolder == toHolder &&
		t.ToLocation == toLocation &&
		t.HandedOverAt.Equal(handedAt)
}

// ConfirmInput 是确认接收的入参。
type ConfirmInput struct {
	// TransferID 为待确认的交接编号。
	TransferID string
	// Receiver 为实际接收人，必须是发起时指定的接收人。
	Receiver string
	// AtLocation 为接收地点，必须是发起时的目的地点。
	AtLocation string
	// ReceivedAt 为接收时间，不能早于交出时间。
	ReceivedAt time.Time
}

// Confirm 由指定接收人在目的地点确认接收。确认后样品的持有人和地点
// 才真正改变，待确认状态结束。
//
// 对已确认交接重复提交完全相同的接收信息（接收人、地点、时间）时，
// 返回原结果且不增加转手记录；信息不同则拒绝。
func (s *Store) Confirm(in ConfirmInput) (*TransferView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	transferID, err := trimRequired("交接编号", in.TransferID)
	if err != nil {
		return nil, err
	}
	receiver, err := trimRequired("接收人", in.Receiver)
	if err != nil {
		return nil, err
	}
	atLocation, err := trimRequired("接收地点", in.AtLocation)
	if err != nil {
		return nil, err
	}
	if in.ReceivedAt.IsZero() {
		return nil, fmt.Errorf("%w: 接收时间不能为空", ErrInvalid)
	}
	receivedAt := in.ReceivedAt.UTC()

	rec, ok := s.data.Transfers[transferID]
	if !ok {
		// 明确的不存在错误，绝不顺带创建交接。
		return nil, fmt.Errorf("%w: 交接 %q 不存在", ErrNotFound, transferID)
	}

	if rec.Confirmed {
		same := rec.ConfirmedBy == receiver &&
			rec.ToLocation == atLocation &&
			rec.ReceivedAt != nil && rec.ReceivedAt.Equal(receivedAt)
		if !same {
			return nil, fmt.Errorf("%w: 交接 %q 已确认，接收信息与原确认不一致", ErrConflict, transferID)
		}
		return buildTransferView(rec), nil
	}

	if receiver != rec.ToHolder {
		return nil, fmt.Errorf("%w: 只有指定接收人 %q 能确认交接 %q，实际确认人为 %q",
			ErrConflict, rec.ToHolder, transferID, receiver)
	}
	if atLocation != rec.ToLocation {
		return nil, fmt.Errorf("%w: 必须在目的地点 %q 确认交接 %q，实际地点为 %q",
			ErrConflict, rec.ToLocation, transferID, atLocation)
	}
	if receivedAt.Before(rec.HandedOverAt) {
		return nil, fmt.Errorf("%w: 接收时间不能早于交出时间 %s",
			ErrInvalid, rec.HandedOverAt.Format(time.RFC3339))
	}

	candidate := s.data.deepCopy()
	cp := candidate.Transfers[transferID]
	cp.Confirmed = true
	cp.ConfirmedBy = receiver
	receivedAtCopy := receivedAt
	cp.ReceivedAt = &receivedAtCopy

	sample := candidate.Samples[cp.SampleID]
	sample.Holder = receiver
	sample.Location = atLocation
	sample.PendingID = ""
	sample.History = append(sample.History, historyRecord{
		Kind:     "transfer-in",
		Time:     receivedAt,
		Holder:   receiver,
		Location: atLocation,
		Detail:   fmt.Sprintf("交接 %s 确认接收（交出时间 %s）", transferID, cp.HandedOverAt.Format(time.RFC3339)),
	})

	if err := s.commit(candidate); err != nil {
		return nil, err
	}
	return buildTransferView(cp), nil
}

// DestroyInput 是销毁样品全部剩余量的入参；销毁不接受部分销毁。
type DestroyInput struct {
	// SampleID 为样品编号。
	SampleID string
	// Operator 为操作人，去空白后必须与样品当前持有人一致。
	Operator string
	// Location 为销毁所在地点，去空白后必须与样品当前地点一致。
	Location string
	// At 为销毁时间，不能为零，也不能早于该样品任何已有保管历史的时间。
	At time.Time
	// Reason 为销毁原因，去空白后非空。
	Reason string
}

// Destroy 销毁该编号样品当时的全部剩余量（不接受部分销毁）。成功后
// 剩余量变为 0.000，初始量、来源关系、子样列表与原有保管历史保留，
// 持有人和地点保留为销毁前的最后记录，并在保管历史末尾追加一次销毁
// 事件；返回该样品的最新查询结果。
//
// 已销毁样品重复提交完全相同的操作人、地点、时间和原因时，返回原销毁
// 结果且不再追加历史；文本按去首尾空白后的内容比较，时间按实际时刻
// 比较。任一项不同则返回 ErrConflict，不覆盖原记录。
func (s *Store) Destroy(in DestroyInput) (*Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sampleID, err := trimRequired("样品编号", in.SampleID)
	if err != nil {
		return nil, err
	}
	operator, err := trimRequired("操作人", in.Operator)
	if err != nil {
		return nil, err
	}
	location, err := trimRequired("所在地点", in.Location)
	if err != nil {
		return nil, err
	}
	reason, err := trimRequired("销毁原因", in.Reason)
	if err != nil {
		return nil, err
	}
	if in.At.IsZero() {
		return nil, fmt.Errorf("%w: 销毁时间不能为空", ErrInvalid)
	}
	at := in.At.UTC()

	rec, ok := s.data.Samples[sampleID]
	if !ok {
		return nil, fmt.Errorf("%w: 样品 %q 不存在", ErrNotFound, sampleID)
	}

	// 已销毁：只接受与首次销毁完全相同的重复提交，原样返回且不再追加历史。
	if rec.Destroyed != nil {
		if !sameDestruction(rec.Destroyed, operator, location, at, reason) {
			return nil, fmt.Errorf("%w: 样品 %q 已销毁，本次销毁信息与原销毁记录不一致",
				ErrConflict, sampleID)
		}
		return buildSampleView(rec, s.data), nil
	}

	// 未销毁样品的状态冲突（持有人/地点不匹配按入参语义归入冲突类）。
	if operator != rec.Holder {
		return nil, fmt.Errorf("%w: 操作人 %q 与样品 %q 当前持有人 %q 不一致",
			ErrConflict, operator, sampleID, rec.Holder)
	}
	if location != rec.Location {
		return nil, fmt.Errorf("%w: 销毁地点 %q 与样品 %q 当前地点 %q 不一致",
			ErrConflict, location, sampleID, rec.Location)
	}
	if rec.PendingID != "" {
		return nil, fmt.Errorf("%w: 样品 %q 存在待确认交接 %s，不能销毁",
			ErrConflict, sampleID, rec.PendingID)
	}
	if rec.Remaining == 0 {
		return nil, fmt.Errorf("%w: 样品 %q 剩余量已为零且未销毁，不能再销毁",
			ErrConflict, sampleID)
	}
	for _, h := range rec.History {
		if at.Before(h.Time) {
			return nil, fmt.Errorf("%w: 销毁时间不能早于样品 %q 已有保管历史的时间 %s",
				ErrInvalid, sampleID, h.Time.Format(time.RFC3339))
		}
	}

	candidate := s.data.deepCopy()
	cp := candidate.Samples[sampleID]
	destroyedQty := cp.Remaining
	cp.Remaining = 0
	cp.Destroyed = &destructionRecord{
		Operator: operator,
		Location: location,
		At:       at,
		Reason:   reason,
		Qty:      destroyedQty,
	}
	// 持有人和地点保留为销毁前的最后记录，仅在历史末尾追加销毁事件。
	cp.History = append(cp.History, historyRecord{
		Kind:     "destroy",
		Time:     at,
		Holder:   operator,
		Location: location,
		Detail:   fmt.Sprintf("销毁全部剩余量 %s 毫升，原因：%s", formatUnits(destroyedQty), reason),
	})

	if err := s.commit(candidate); err != nil {
		return nil, err
	}
	return buildSampleView(cp, candidate), nil
}

// sameDestruction 判断本次销毁请求与已有销毁记录是否完全相同。
// 文本字段均已去首尾空白，时间按实际时刻（Equal）比较。
func sameDestruction(d *destructionRecord, operator, location string, at time.Time, reason string) bool {
	return d.Operator == operator &&
		d.Location == location &&
		d.At.Equal(at) &&
		d.Reason == reason
}

// GetSample 按样品编号查询：返回来源关系、初始量/剩余量、当前持有人和
// 地点、直接子样、按发生顺序排列的保管历史，以及待确认交接详情。
// 样品不存在时返回包装了 ErrNotFound 的错误，不会创建任何记录。
func (s *Store) GetSample(id string) (*Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sampleID, err := trimRequired("样品编号", id)
	if err != nil {
		return nil, err
	}
	rec, ok := s.data.Samples[sampleID]
	if !ok {
		return nil, fmt.Errorf("%w: 样品 %q 不存在", ErrNotFound, sampleID)
	}
	return buildSampleView(rec, s.data), nil
}

// GetTransfer 按交接编号查询交接详情（待确认或已确认）。
// 交接不存在时返回包装了 ErrNotFound 的错误，不会创建任何记录。
func (s *Store) GetTransfer(id string) (*TransferView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	transferID, err := trimRequired("交接编号", id)
	if err != nil {
		return nil, err
	}
	rec, ok := s.data.Transfers[transferID]
	if !ok {
		return nil, fmt.Errorf("%w: 交接 %q 不存在", ErrNotFound, transferID)
	}
	return buildTransferView(rec), nil
}

// buildSampleView 把内部样品记录转换为对外视图，数量统一格式化为三位小数。
func buildSampleView(r *sampleRecord, l *ledger) *Sample {
	children := append([]string{}, r.Children...)
	history := make([]History, 0, len(r.History))
	for _, h := range r.History {
		history = append(history, History{
			Kind:     h.Kind,
			Time:     h.Time,
			Holder:   h.Holder,
			Location: h.Location,
			Detail:   h.Detail,
		})
	}
	v := &Sample{
		ID:         r.ID,
		ParentID:   r.ParentID,
		InitialQty: formatUnits(r.Initial),
		Remaining:  formatUnits(r.Remaining),
		Holder:     r.Holder,
		Location:   r.Location,
		Children:   children,
		History:    history,
	}
	if r.PendingID != "" {
		if t, ok := l.Transfers[r.PendingID]; ok {
			v.PendingTransfer = buildTransferView(t)
		}
	}
	if r.Destroyed != nil {
		v.Destruction = &Destruction{
			Operator: r.Destroyed.Operator,
			Location: r.Destroyed.Location,
			At:       r.Destroyed.At,
			Reason:   r.Destroyed.Reason,
			Qty:      formatUnits(r.Destroyed.Qty),
		}
	}
	return v
}

// buildTransferView 把内部交接记录转换为对外视图。
func buildTransferView(r *transferRecord) *TransferView {
	v := &TransferView{
		TransferID:   r.ID,
		SampleID:     r.SampleID,
		FromHolder:   r.FromHolder,
		FromLocation: r.FromLocation,
		ToHolder:     r.ToHolder,
		ToLocation:   r.ToLocation,
		Qty:          formatUnits(r.Qty),
		HandedOverAt: r.HandedOverAt,
		Confirmed:    r.Confirmed,
		ConfirmedBy:  r.ConfirmedBy,
	}
	if r.ReceivedAt != nil {
		rx := *r.ReceivedAt
		v.ReceivedAt = &rx
		v.ConfirmedAt = r.ToLocation
	}
	return v
}
