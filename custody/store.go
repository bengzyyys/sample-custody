package custody

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Store 是一份本地样品数据。所有操作都通过公开方法进行，
// 内部以互斥锁串行化，保证并发下不会超量分装或出现两条待确认交接。
type Store struct {
	mu        sync.Mutex
	path      string
	samples   map[string]*Sample
	handovers map[string]*Handover
	nextSeq   int64
	closed    bool
}

// Create 在 path 创建一份新的本地数据文件。文件已存在时返回 ErrConflict，
// 避免误删已有数据。
func Create(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: 路径为空", ErrInvalidInput)
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%w: 数据文件已存在: %s", ErrConflict, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("custody: 无法访问数据文件: %w", err)
	}
	s := &Store{
		path:      path,
		samples:   map[string]*Sample{},
		handovers: map[string]*Handover{},
	}
	if err := s.persist(); err != nil {
		return nil, err
	}
	return s, nil
}

// Open 打开一份已存在的本地数据文件。文件不存在时返回 ErrNotFound。
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: 路径为空", ErrInvalidInput)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: 数据文件不存在: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("custody: 无法读取数据文件: %w", err)
	}
	var st storeState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("custody: 数据文件已损坏: %w", err)
	}
	if st.Version != 1 {
		return nil, fmt.Errorf("custody: 不支持的数据版本: %d", st.Version)
	}
	s := &Store{
		path:      path,
		samples:   map[string]*Sample{},
		handovers: map[string]*Handover{},
		nextSeq:   st.NextSeq,
	}
	for i := range st.Samples {
		s.samples[st.Samples[i].ID] = st.Samples[i]
	}
	for i := range st.Handovers {
		s.handovers[st.Handovers[i].ID] = st.Handovers[i]
	}
	return s, nil
}

// Close 关闭 Store。数据在每次变更时都已落盘，关闭后即可用 Open 重新打开。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// persist 把当前完整状态原子地写入数据文件：先写同目录临时文件，再 rename。
// 调用方必须持有锁。
func (s *Store) persist() error {
	st := storeState{
		Version: 1,
		NextSeq: s.nextSeq,
	}
	for _, smp := range s.samples {
		st.Samples = append(st.Samples, smp)
	}
	for _, h := range s.handovers {
		st.Handovers = append(st.Handovers, h)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".custody-*.tmp")
	if err != nil {
		return fmt.Errorf("custody: 无法创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("custody: 写入临时文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("custody: 关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("custody: 落盘失败: %w", err)
	}
	return nil
}

// seq 分配下一个历史事件序号。调用方必须持有锁。
func (s *Store) seq() int64 {
	s.nextSeq++
	return s.nextSeq
}

// cloneSample 返回样品的深拷贝，用于落盘失败时回滚。
func cloneSample(smp *Sample) *Sample {
	c := *smp
	c.Children = append([]string(nil), smp.Children...)
	c.History = append([]HistoryEvent(nil), smp.History...)
	return &c
}
