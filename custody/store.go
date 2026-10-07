package custody

import (
	"bytes"
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
//
// 交接集合中每个交接编号只能出现一次：同一编号写了两条（无论是否相邻、
// 内容是否相同）都使整份文件打开失败，返回包装了 ErrInvalid 的错误，
// 原文件内容保持原样，绝不只加载其中正常部分。
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

// UnmarshalJSON 恢复整份状态。交接集合不能直接用 map 解码：JSON 对象里
// 同一编号写两条时，普通解码会让后一条悄悄覆盖前一条，原先的接收事实或
// 待确认信息就此丢失。这里改为逐键解码（见 decodeTransfers），同一交接
// 编号出现两次即拒绝整份数据。
func (l *ledger) UnmarshalJSON(data []byte) error {
	var raw struct {
		Version   int                      `json:"version"`
		Samples   map[string]*sampleRecord `json:"samples"`
		Transfers json.RawMessage          `json:"transfers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	transfers, err := decodeTransfers(raw.Transfers)
	if err != nil {
		return err
	}
	l.Version = raw.Version
	l.Samples = raw.Samples
	l.Transfers = transfers
	return nil
}

// decodeTransfers 解码交接集合，并保证交接编号在读取已有数据时同样唯一：
//   - 集合缺省或为 null 时返回 nil，由调用方按既有约定替换为空集合；
//     空对象返回空集合，含义均不变；
//   - 为对象时逐个键解码，键按 JSON 解码后实际表示的文字比较（合法
//     Unicode 转义写法与字面写法表示同一编号），同一编号第二次出现即
//     返回包装了 ErrInvalid 的错误，写明重复的交接编号与同一编号只能
//     出现一次的原因。无论两条记录内容是否相同、是否相邻，也绝不按
//     确认状态或关联样品选择其中一条、删除或合并条目、自动改号来接受；
//   - 只在交接集合的键上判断唯一性：样品保管历史的说明、交接记录的
//     字段值或其他说明文字中再次提到某个编号不算重复；不同编号的交接
//     关联同一份样品（样品的多次转手）也属正常，不在此拒绝。
func decodeTransfers(raw json.RawMessage) (map[string]*transferRecord, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("交接集合必须是 JSON 对象或 null")
	}
	transfers := make(map[string]*transferRecord)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		id, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("交接集合的键必须是字符串")
		}
		if _, dup := transfers[id]; dup {
			return nil, fmt.Errorf("%w: 交接编号 %q 在交接集合中重复出现，同一交接编号只能出现一次，无法恢复",
				ErrInvalid, id)
		}
		var rec *transferRecord
		if err := dec.Decode(&rec); err != nil {
			return nil, err
		}
		transfers[id] = rec
	}
	if _, err := dec.Token(); err != nil { // 闭合的 '}'
		return nil, err
	}
	return transfers, nil
}

// validateRestored 校验从文件恢复的数据中每份样品自身的数量、待确认交接
// 与销毁数量是否一致。
//
// 每个样品先核对自身数量与待确认交接，再核对销毁数量：已销毁样品仍挂着
// 待确认交接本身就是损坏，沿用原有的交接恢复报错。
//
// 没有销毁信息的样品仍视为未销毁（不会仅凭 0.000 补出销毁记录），但它的
// 数量必须自洽：自身初始量必须大于零，剩余量可以为零却不能为负、也不能
// 超过自身初始量，并且剩余量加上已经直接分出的总量也不能超过自身初始量。
// 直接分出总量按子样记录的来源编号认定、只计各直接子样创建时取得的初始
// 量（见 validateActiveQuantities）。这一规则同时适用于登记的原样和分装
// 产生的子样；子样的上限是它自己创建时取得的初始量，不能借用父样或其他
// 样品的量。样品是否挂着待确认交接不影响这项检查：即使待确认交接量恰好
// 等于错误的剩余量，数量越界的样品也必须拒绝（见 validateActiveQuantities）。
//
// 对带有销毁信息的样品，先核对销毁时间（见 validateDestroyedTime）：销毁
// 时间缺失或为零值一律拒绝（与是否有保管历史无关），且按实际时刻不得早于
// 该样品自身任何一条保管历史的发生时刻；随后逐一核对数量守恒（见
// validateDestroyedQuantity）：
// 当前剩余量必须为 0.000，实际销毁量与自身初始量都大于零，且实际销毁量
// 加上各直接子样创建时取得的初始量之和恰好等于该样品自身的初始量。参与
// 核对的子样关系还必须一致：子样列表中的编号对应现存样品、其来源编号
// 指向本样品且不重复，来源编号指向本样品的子样也必须全部列在其中；重复
// 列入、来源不符或漏列都按无效数据拒绝，不能只凭数量等式接受。这里只
// 统计直接子样创建时的量：子样后来继续分装、转交或销毁都不改变原样
// 当时已经分出的量，不能改用子样当前剩余量，孙样也不重复计入；没有分出
// 子样的样品，实际销毁量就应等于自己的初始量。
//
// 时间与数量都成立后，还核对销毁操作人、销毁地点与该样品自身保留的最后
// 持有人、当前地点是否一致（见 validateDestroyedCustody）：销毁后持有人
// 和地点保留为销毁前的最后记录，两处信息互相矛盾会让查询同时返回冲突
// 内容，必须按无效数据拒绝。核对只看同一编号样品自身的最后记录，父样与
// 分装子样可由不同人员在不同地点分别销毁，子样后来换人、移动不影响父样；
// 早期登记、交接历史中的人员、地点与最后记录不同也不构成拒绝理由。销毁
// 操作人或地点缺失、为空或只有空白同样无效，不能因样品对应字段也为空而
// 当作一致；已保存的非空文本按原文核对，不通过去掉空白来接受不同内容。
//
// 样品的非空 PendingID 必须指向一条实际存在、尚未确认、且属于该样品的
// 交接；每条尚未确认的交接也必须对应一份实际存在、且 PendingID 正好指向
// 它的样品。待确认交接表示样品当前全部剩余量尚待指定人员接收，因此其
// 内容还必须与样品当前记录一致：样品未销毁、剩余量大于零、交出人等于
// 当前持有人、交出地点等于当前地点、交接量精确等于当前剩余量。
//
// 已确认交接属于保留的历史，不要求样品继续指向它，也不核对其与样品当前
// 持有人、地点或剩余量的差异（样品后来分装、移动或销毁都不能否定当时的
// 接收事实）；但它必须关联一份实际存在的样品记录，交接量必须大于零且不
// 超过该样品自身的初始量（见 validateConfirmedReceipt），并保留有效的
// 接收信息：确认人非空且与该交接原先指定的接收人一致，接收时间存在且
// 非零，且按实际时刻不早于交出时间（恰好相等合法，时区只影响表示）。
// 集合中占用编号却为 null 的条目视为损坏，不能当作记录不存在。任一问题
// 都使整个文件无法恢复，绝不只加载其中一部分，也不改动原文件。
func validateRestored(l *ledger) error {
	sampleIDs := make([]string, 0, len(l.Samples))
	for id := range l.Samples {
		sampleIDs = append(sampleIDs, id)
	}
	sort.Strings(sampleIDs)
	for _, id := range sampleIDs {
		rec := l.Samples[id]
		if rec == nil {
			if refs := transferIDsReferencingSample(l, id); len(refs) > 0 {
				return fmt.Errorf("%w: 样品编号 %q 已被占用但记录为 null，引用它的交接 %s 无法关联到实际样品，无法恢复",
					ErrInvalid, id, strings.Join(quoteAll(refs), ", "))
			}
			return fmt.Errorf("%w: 样品编号 %q 已被占用但记录为 null，无法恢复", ErrInvalid, id)
		}
		// 先核对样品自身数量是否自洽（含直接分出总量）：数量越界的样品即使
		// 挂着交接量恰好一致的待确认交接，也不能被交接检查放行，必须按数量
		// 规则拒绝。
		if rec.Destroyed == nil {
			if err := validateActiveQuantities(id, rec, l); err != nil {
				return err
			}
		}
		if rec.PendingID != "" {
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
			if err := validatePendingContent(id, rec, tr); err != nil {
				return err
			}
		}
		// 销毁核对放在待确认交接核对之后：已销毁样品仍挂着待确认交接
		// 本身就是损坏，沿用原有的交接恢复报错，保持与既有交接检查的兼容。
		if rec.Destroyed != nil {
			if err := validateDestroyedTime(id, rec); err != nil {
				return err
			}
			if err := validateDestroyedQuantity(id, rec, l); err != nil {
				return err
			}
			if err := validateDestroyedCustody(id, rec); err != nil {
				return err
			}
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
			sample, ok := l.Samples[rec.SampleID]
			switch {
			case !ok:
				return fmt.Errorf("%w: 已确认交接 %q 关联的样品 %q 不存在，无法恢复",
					ErrInvalid, id, rec.SampleID)
			case sample == nil:
				return fmt.Errorf("%w: 已确认交接 %q 关联的样品 %q 记录为 null，无法恢复",
					ErrInvalid, id, rec.SampleID)
			}
			if err := validateConfirmedReceipt(rec, sample); err != nil {
				return err
			}
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
		default:
			if err := validatePendingContent(rec.SampleID, sample, rec); err != nil {
				return err
			}
		}
	}
	return nil
}

// transferIDsReferencingSample 返回所有（含已确认与待确认）关联到给定样品
// 编号的交接编号，按编号排序，用于在样品条目损坏（如 null）时仍能指出
// 哪些交接因此失去关联。
func transferIDsReferencingSample(l *ledger, sampleID string) []string {
	var ids []string
	for id, tr := range l.Transfers {
		if tr != nil && tr.SampleID == sampleID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// quoteAll 把编号逐个加上 %q 引号，便于在错误信息中列出。
func quoteAll(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = fmt.Sprintf("%q", id)
	}
	return out
}

// validateActiveQuantities 核对一份没有销毁信息（即未销毁）的样品数量是否
// 合理。重新打开旧数据时，没有销毁信息的样品仍视为未销毁，不会仅凭剩余量
// 0.000 补出销毁记录，但它的数量必须自洽：
//   - 自身初始量必须大于零（原样取登记量，子样取创建时分得的量）；
//   - 剩余量可以为零（分装用尽的旧约定），但不能为负；
//   - 剩余量不能超过自身初始量；
//   - 剩余量加上已经直接分出的总量，不能超过自身初始量。
//
// 直接分出总量按子样记录中的来源编号（ParentID）认定：全数据中任何来源
// 编号指向本样品的子样都算直接子样，每份只计它创建时取得的初始量，不能
// 因为本样品的子样列表漏列就少算（未销毁样品不做来源关系一致性核对，
// 漏列不影响这里的统计）。子样后来继续分装、交接或销毁都不减少来源样品
// 当时已经分出的量，不能改用子样当前剩余量；孙样不计入祖父样，其他原样
// 和兄弟子样也不参与本次核对。剩余量与直接分出总量之和小于初始量仍可
// 恢复，不要求强行补齐差额（保留旧数据的接受约定）。
//
// 这一规则对登记的原样和分装产生的子样同样适用：子样的上限是它自己创建
// 时取得的初始量，与其父样或其他样品的数量无关，不能借用父样的量。样品
// 是否挂着待确认交接不影响本检查——即使交接量恰好等于错误的剩余量，数量
// 越界也必须拒绝。数量按 0.001 毫升精度核对，上限附近的合法合计必须准确
// 接受；合计超过 int64 可表示范围时按无效数据拒绝，不能因数量过大把回绕
// 后的差额当作合法结果接受。任一不符都返回包装了 ErrInvalid 的错误，信息
// 写明样品编号、违反的规则与初始量、剩余量、直接分出总量，统一按三位
// 小数毫升展示。
func validateActiveQuantities(sampleID string, rec *sampleRecord, l *ledger) error {
	const msgTail = "，无法恢复"
	switch {
	case rec.Initial <= 0:
		return fmt.Errorf("%w: 未销毁样品 %q 的初始量 %s 毫升必须大于零%s",
			ErrInvalid, sampleID, formatUnits(rec.Initial), msgTail)
	case rec.Remaining < 0:
		return fmt.Errorf("%w: 未销毁样品 %q 的剩余量 %s 毫升不能为负（初始量 %s 毫升）%s",
			ErrInvalid, sampleID, formatUnits(rec.Remaining), formatUnits(rec.Initial), msgTail)
	case rec.Remaining > rec.Initial:
		return fmt.Errorf("%w: 未销毁样品 %q 的剩余量 %s 毫升超过自身初始量 %s 毫升，剩余量不得大于初始量%s",
			ErrInvalid, sampleID, formatUnits(rec.Remaining), formatUnits(rec.Initial), msgTail)
	}

	// 直接分出总量的枚举、累计与拒绝规则只有一份实现（见
	// sumDirectChildrenInitial）：打开未销毁数据、发起销毁、打开已销毁
	// 数据三个场景共用，不再各自重复处理子样初始量不大于零与累计超出支持
	// 范围。这里按“未销毁样品恢复失败”的语义把共同问题渲染为 ErrInvalid；
	// 未销毁样品不做来源关系一致性核对，子样列表漏列不影响统计。
	directChildrenTotal, prob := sumDirectChildrenInitial(l, sampleID)
	if prob != nil {
		return prob.asActiveRestoreError(sampleID, rec.Remaining)
	}

	// 参与核对数量（这里取当前剩余量）与直接分出总量相加、合计溢出判断及
	// 与自身初始量的比较同样只有一份实现（见 checkConservationTotal）；
	// 未销毁旧记录保留“合计可以小于初始量”的接受约定，只拒绝合计大于初始
	// 量（含相加溢出）的记录。这里按“未销毁样品恢复失败”的语义渲染。
	res := checkConservationTotal(rec.Remaining, directChildrenTotal, rec.Initial, false)
	switch res.outcome {
	case accountedOverflow:
		return fmt.Errorf("%w: 未销毁样品 %q 的剩余量 %s 毫升与直接分出总量 %s 毫升合计超出可表示范围%s",
			ErrInvalid, sampleID, formatUnits(rec.Remaining),
			formatUnits(directChildrenTotal), msgTail)
	case accountedAboveInitial:
		return fmt.Errorf("%w: 未销毁样品 %q 的剩余量 %s 毫升与直接分出总量 %s 毫升之和为 %s 毫升，超过自身初始量 %s 毫升，剩余量与直接分出总量之和不得大于初始量%s",
			ErrInvalid, sampleID,
			formatUnits(rec.Remaining), formatUnits(directChildrenTotal), formatUnits(res.total),
			formatUnits(rec.Initial), msgTail)
	case accountedEqual, accountedBelowInitial:
		// 恰好守恒正常恢复；合计小于初始量是旧记录保留的差额，恢复阶段不
		// 强行补齐（这类记录仍不能销毁，销毁前核对走 requireExact 分支）。
	}
	return nil
}

// validateDestroyedTime 核对一份带销毁信息的样品记录其销毁时间是否成立，
// 与正常销毁功能（Destroy）的时间规则保持一致：
//   - 销毁时间必须存在且非零值：缺失或零值一律拒绝，即使该样品没有任何
//     保管历史也一样；
//   - 销毁时间按实际时刻不得早于该样品自身任何一条保管历史的发生时刻
//     （用 Before 判断而非比较时区偏移后的字面值）：恰好等于自身历史中
//     最晚的时刻合法，同一时刻用不同时区表示也得到相同结果。
//
// 保管历史按操作发生顺序保存、不保证时间递增（例如分装记录可能最后追加
// 却带着更早的时刻），因此逐条核对全部历史，而不是只比较末尾一条。这里
// 只看该编号样品自己的历史：已经分出的子样或同一份数据中其他样品后来
// 发生的交接，都不延后本样品可以销毁的时间；分装子样也按自己的历史判断。
// 核对只读取历史，绝不因核对而重排或改写记录。任一不符都返回包装了
// ErrInvalid 的错误，信息写明样品编号；发生时间倒置时同时给出销毁时间
// 与冲突历史的时间，便于定位记录。
func validateDestroyedTime(sampleID string, rec *sampleRecord) error {
	at := rec.Destroyed.At
	if at.IsZero() {
		return fmt.Errorf("%w: 样品 %q 的销毁时间缺失或为零值，无法恢复",
			ErrInvalid, sampleID)
	}
	for _, h := range rec.History {
		if at.Before(h.Time) {
			return fmt.Errorf("%w: 样品 %q 的销毁时间 %s 早于已有保管历史（%s）的时间 %s，无法恢复",
				ErrInvalid, sampleID,
				at.Format(time.RFC3339), h.Kind, h.Time.Format(time.RFC3339))
		}
	}
	return nil
}

// childRelationshipProblemKind 标识直接子样关系矛盾的具体类型。
type childRelationshipProblemKind int

const (
	// childDuplicate：同一编号在子样列表中重复列入。
	childDuplicate childRelationshipProblemKind = iota
	// childMissing：子样列表列入的编号在数据中不存在（含 null 条目）。
	childMissing
	// childFromRoot：列入的样品是没有来源编号的原样。
	childFromRoot
	// childFromOther：列入的样品来源编号指向另一份样品（父样、兄弟子样
	// 或其他原样）。
	childFromOther
	// childOmitted：来源编号指向本样品的真正直接子样没有列在列表中。
	childOmitted
)

// childRelationshipProblem 描述一份样品的直接子样列表（Children）与全数据
// 中子样记录来源编号（ParentID）之间的一处关系矛盾，是
// checkDirectChildRelationship 的唯一结果形式。关系规则只有这一份实现，
// 由发起销毁前的守恒核对与重新打开已销毁记录时的恢复核对共用，两个入口
// 再按各自的失败语义把它渲染成具体错误（见 asDestroyConflictError 与
// asRestoreError），规则本身不再在两处分别维护。
type childRelationshipProblem struct {
	kind childRelationshipProblemKind
	// childID 为涉及矛盾的子样编号。
	childID string
	// actualSource 仅在来源指向其他样品（childFromOther）时使用，为该
	// 子样记录实际写着的来源编号。
	actualSource string
}

// checkDirectChildRelationship 核对“一份样品列出的直接子样”与“全数据中
// 来源编号指向它的子样”是否严格是同一个集合：
//   - 列表中每个编号都对应一份现存（非 nil）样品；
//   - 每个编号在列表中只出现一次；
//   - 每个列入子样的来源编号都必须正好指向本样品（没有来源编号的原样、
//     指向父样/兄弟子样/其他原样的子样都不合格）；
//   - 反过来，全数据中任何来源编号指向本样品的子样都必须列在列表中。
//
// 重复列入、列入不存在的编号、来源不符或漏列真正的直接子样都属于关系
// 矛盾，即使数量合计碰巧等于初始量也不能接受。核对只读不写，绝不靠
// 去重、补列或修改来源编号消除矛盾。原样与分装子样遵守同一规则：分装
// 子样核对的是它自己直接分出的子样，父样、兄弟子样、孙样与其他原样都
// 不参与。没有矛盾时返回 nil。
func checkDirectChildRelationship(sampleID string, listedChildren []string, l *ledger) *childRelationshipProblem {
	listed := make(map[string]struct{}, len(listedChildren))
	for _, childID := range listedChildren {
		if _, dup := listed[childID]; dup {
			return &childRelationshipProblem{kind: childDuplicate, childID: childID}
		}
		listed[childID] = struct{}{}
		child, ok := l.Samples[childID]
		if !ok || child == nil {
			return &childRelationshipProblem{kind: childMissing, childID: childID}
		}
		if child.ParentID != sampleID {
			if child.ParentID == "" {
				return &childRelationshipProblem{kind: childFromRoot, childID: childID}
			}
			return &childRelationshipProblem{kind: childFromOther, childID: childID, actualSource: child.ParentID}
		}
	}
	// 反向核对：任何真正由本样品分出的子样都必须列在子样列表中，漏列会
	// 让查询出的来源关系互相矛盾，不能因数量等式成立而接受。
	for otherID, other := range l.Samples {
		if other == nil || other.ParentID != sampleID {
			continue
		}
		if _, ok := listed[otherID]; !ok {
			return &childRelationshipProblem{kind: childOmitted, childID: otherID}
		}
	}
	return nil
}

// asRestoreError 按“重新打开已销毁记录失败”的语义把关系矛盾渲染为包装了
// ErrInvalid 的错误：信息写明有问题的样品、涉及的子样与重复列入/不存在/
// 来源不符/漏列的具体原因，末尾统一带“，无法恢复”，不能只剩一条笼统的
// 失败提示。措辞与历史可观察结果保持一致。
func (p *childRelationshipProblem) asRestoreError(sampleID string) error {
	const msgTail = "，无法恢复"
	switch p.kind {
	case childDuplicate:
		return fmt.Errorf("%w: 样品 %q 的销毁数量核对中，直接子样编号 %q 在子样列表中重复列入，同一子样只能计入一次%s",
			ErrInvalid, sampleID, p.childID, msgTail)
	case childMissing:
		return fmt.Errorf("%w: 样品 %q 的销毁数量核对中，列入的直接子样 %q 不存在%s",
			ErrInvalid, sampleID, p.childID, msgTail)
	case childFromRoot:
		return fmt.Errorf("%w: 样品 %q 的销毁数量核对中，列入的子样 %q 是原样、没有来源编号，不是由样品 %q 直接分出的子样%s",
			ErrInvalid, sampleID, p.childID, sampleID, msgTail)
	case childFromOther:
		return fmt.Errorf("%w: 样品 %q 的销毁数量核对中，列入的子样 %q 来源编号为 %q，不是由样品 %q 直接分出的子样%s",
			ErrInvalid, sampleID, p.childID, p.actualSource, sampleID, msgTail)
	default: // childOmitted
		return fmt.Errorf("%w: 样品 %q 的销毁数量核对中，子样 %q 的来源编号指向样品 %q，却没有列在样品 %q 的直接子样列表中，属于漏列%s",
			ErrInvalid, sampleID, p.childID, sampleID, sampleID, msgTail)
	}
}

// asDestroyConflictError 按“发起销毁时状态冲突”的语义把关系矛盾渲染为
// 包装了 ErrConflict 的错误：信息写明有问题的样品、涉及的子样与具体原因，
// 并说明记录保持原样、需要先核对子样列表。销毁被拒绝时不返回销毁结果，
// 样品和文件保持原样。措辞与历史可观察结果保持一致。
func (p *childRelationshipProblem) asDestroyConflictError(sampleID string) error {
	const msgTail = "；记录保持原样，请先核对子样列表"
	switch p.kind {
	case childDuplicate:
		return fmt.Errorf("%w: 样品 %q 数量来源关系自相矛盾，不能销毁：直接子样编号 %q 在子样列表中重复列入%s",
			ErrConflict, sampleID, p.childID, msgTail)
	case childMissing:
		return fmt.Errorf("%w: 样品 %q 数量来源关系自相矛盾，不能销毁：子样列表中的直接子样 %q 不存在%s",
			ErrConflict, sampleID, p.childID, msgTail)
	case childFromRoot:
		return fmt.Errorf("%w: 样品 %q 数量来源关系自相矛盾，不能销毁：列入的子样 %q 是没有来源编号的原样，不是由样品 %q 直接分出的子样%s",
			ErrConflict, sampleID, p.childID, sampleID, msgTail)
	case childFromOther:
		return fmt.Errorf("%w: 样品 %q 数量来源关系自相矛盾，不能销毁：列入的子样 %q 来源编号为 %q，不是由样品 %q 直接分出的子样%s",
			ErrConflict, sampleID, p.childID, p.actualSource, sampleID, msgTail)
	default: // childOmitted
		return fmt.Errorf("%w: 样品 %q 数量来源关系自相矛盾，不能销毁：子样 %q 的来源编号指向样品 %q，却没有列在它的直接子样列表中%s",
			ErrConflict, sampleID, p.childID, sampleID, msgTail)
	}
}

// validateDestroyedQuantity 核对一份带销毁信息的样品记录其数量是否守恒。
// 销毁处理的是样品当时的全部剩余量，因此恢复时必须满足：
//   - 当前剩余量为 0.000；
//   - 自身初始量与实际销毁量都大于零（参与核对的数量必须为正）；
//   - 参与数量核对的子样关系必须一致。关系规则与发起销毁前的核对共用同
//     一份实现（见 checkDirectChildRelationship），两处不再分别维护：
//     Children 里每个编号都对应一份现存样品、其来源编号正好指向本样品且
//     不重复；反过来，全数据中任何 ParentID 指向本样品的子样也必须列在
//     Children 里。同一编号重复列入、列入的子样来源不符（含来源为空或
//     指向另一份原样/样品）、漏列真正的直接子样都使来源关系自相矛盾，
//     即使合计碰巧等于初始量也必须拒绝；不能靠删除重复项、忽略漏列项或
//     改写子样来源来接受文件；
//   - 实际销毁量加上它直接分出的各子样创建时取得的初始量之和，恰好等于
//     该样品自身的初始量。
//
// 直接子样总量只在关系一致的集合上统计，取各子样记录自身的 Initial（创建
// 时取得的量）：子样后来继续分装、转交或销毁都不改变原样当时已经分出的
// 量，不能改用子样当前剩余量，孙样也不重复计入。没有分出子样时，直接
// 子样总量为 0，实际销毁量就应等于自身初始量。同一份数据里的其他原样
// 不参与本次核对。合计超过 int64 可表示范围时按无效数据拒绝，不能因数量
// 过大把回绕后的差额当作合法结果接受。任一不符都返回包装了 ErrInvalid
// 的错误，信息写明样品编号、涉及的子样编号与重复/来源不符/漏列或数量
// 不符的具体原因；涉及合计不一致时同时展示初始量、销毁量与直接子样
// 总量，统一按三位小数毫升展示。
func validateDestroyedQuantity(sampleID string, rec *sampleRecord, l *ledger) error {
	const msgTail = "，无法恢复"
	destroyedQty := rec.Destroyed.Qty
	switch {
	case rec.Remaining != 0:
		return fmt.Errorf("%w: 样品 %q 已销毁但当前剩余量为 %s 毫升，已销毁样品剩余量必须为 0.000 毫升%s",
			ErrInvalid, sampleID, formatUnits(rec.Remaining), msgTail)
	case rec.Initial <= 0:
		return fmt.Errorf("%w: 样品 %q 的销毁记录无法核对：样品初始量 %s 毫升必须大于零%s",
			ErrInvalid, sampleID, formatUnits(rec.Initial), msgTail)
	case destroyedQty <= 0:
		return fmt.Errorf("%w: 样品 %q 的实际销毁量 %s 毫升必须大于零%s",
			ErrInvalid, sampleID, formatUnits(destroyedQty), msgTail)
	}

	// 先确认参与数量核对的子样集合关系一致，再统计它们创建时取得的初始
	// 量：数量等式成立也不能掩盖重复列入、来源不符或漏列。关系规则只有
	// 一份实现（checkDirectChildRelationship），这里按恢复失败的语义渲染
	// 为 ErrInvalid。
	if prob := checkDirectChildRelationship(sampleID, rec.Children, l); prob != nil {
		return prob.asRestoreError(sampleID)
	}

	// 只统计关系一致的直接子样创建时取得的初始量；子样后续变化与孙样都
	// 不计入。枚举、累计与拒绝规则由三个场景共用同一份实现（见
	// sumDirectChildrenInitial），这里按“已销毁数据恢复失败”的语义把共同
	// 问题渲染为 ErrInvalid。
	directChildrenTotal, prob := sumDirectChildrenInitial(l, sampleID)
	if prob != nil {
		return prob.asDestroyedRestoreError(sampleID, destroyedQty)
	}

	// 参与核对数量（恢复时取保存的实际销毁量，而非已归零的当前剩余量）与
	// 直接分出总量相加、合计溢出判断及与自身初始量的精确比较，与发起销毁前
	// 的核对共用同一份实现（见 checkConservationTotal），同一条守恒规则不
	// 再分别维护；这里按“已销毁数据恢复失败”的语义渲染为 ErrInvalid。
	res := checkConservationTotal(destroyedQty, directChildrenTotal, rec.Initial, true)
	switch res.outcome {
	case accountedOverflow:
		return fmt.Errorf("%w: 样品 %q 的销毁数量核对中，实际销毁量 %s 毫升与直接子样总量 %s 毫升合计超出可表示范围%s",
			ErrInvalid, sampleID, formatUnits(destroyedQty),
			formatUnits(directChildrenTotal), msgTail)
	case accountedNotEqual:
		return fmt.Errorf("%w: 样品 %q 的销毁数量与分装记录不符：初始量 %s 毫升，实际销毁量 %s 毫升，直接子样总量 %s 毫升，实际销毁量与直接子样总量之和应为 %s 毫升%s",
			ErrInvalid, sampleID,
			formatUnits(rec.Initial), formatUnits(destroyedQty),
			formatUnits(directChildrenTotal), formatUnits(rec.Initial), msgTail)
	}
	return nil
}

// validateDestroyedCustody 核对一份带销毁信息的样品，其销毁操作人、销毁地点
// 是否与该样品保留的最后持有人、当前地点一致。销毁处理的是样品当时的全部
// 剩余量，销毁成功后样品的持有人和地点保留为销毁前的最后记录（见 Destroy），
// 因此落盘的销毁信息写着的操作人、地点必须分别与样品记录的当前持有人、
// 当前地点完全相同；两者互相矛盾时，查询会同时返回冲突信息，重新打开必须
// 按损坏数据拒绝。
//
// 核对只针对同一编号样品自身的最后记录（rec.Holder/rec.Location）：父样与
// 分装子样可以由不同人员在不同地点分别保管或销毁，子样后来换人、移动不
// 影响父样的销毁信息；该样品早期登记、交接历史里的人员、地点也允许与最后
// 记录不同，这些差异都不是拒绝理由。
//
// 销毁操作人、地点缺失、为空或只有空白一律无效：不能因为样品对应的持有人
// 或地点也为空就把空值视为一致。两边都有非空文本时按原文逐字比较，绝不
// 通过去掉首尾空白来接受 " 李四 " 与 "李四" 之类的不同内容；任一项缺失/
// 空白或不一致都返回包装了 ErrInvalid 的错误，信息写明样品编号以及有问题
// 的是操作人还是地点——两边都有值时同时给出销毁信息与样品记录各自的值，
// 缺失或只有空白时说明具体原因。先核对操作人再核对地点，任意一项不一致都
// 足以拒绝整份文件。
func validateDestroyedCustody(sampleID string, rec *sampleRecord) error {
	const msgTail = "，无法恢复"
	d := rec.Destroyed
	switch {
	case d.Operator == "":
		return fmt.Errorf("%w: 样品 %q 的销毁信息缺少操作人，销毁操作人必须与样品保留的最后持有人 %q 完全一致%s",
			ErrInvalid, sampleID, rec.Holder, msgTail)
	case strings.TrimSpace(d.Operator) == "":
		return fmt.Errorf("%w: 样品 %q 的销毁操作人只有空白，必须与样品保留的最后持有人 %q 完全一致%s",
			ErrInvalid, sampleID, rec.Holder, msgTail)
	case d.Operator != rec.Holder:
		return fmt.Errorf("%w: 样品 %q 的销毁操作人 %q 与样品保留的最后持有人 %q 不一致，销毁操作人必须与最后持有人完全一致%s",
			ErrInvalid, sampleID, d.Operator, rec.Holder, msgTail)
	}
	switch {
	case d.Location == "":
		return fmt.Errorf("%w: 样品 %q 的销毁信息缺少销毁地点，销毁地点必须与样品保留的当前地点 %q 完全一致%s",
			ErrInvalid, sampleID, rec.Location, msgTail)
	case strings.TrimSpace(d.Location) == "":
		return fmt.Errorf("%w: 样品 %q 的销毁地点只有空白，必须与样品保留的当前地点 %q 完全一致%s",
			ErrInvalid, sampleID, rec.Location, msgTail)
	case d.Location != rec.Location:
		return fmt.Errorf("%w: 样品 %q 的销毁地点 %q 与样品保留的当前地点 %q 不一致，销毁地点必须与当前地点完全一致%s",
			ErrInvalid, sampleID, d.Location, rec.Location, msgTail)
	}
	return nil
}

// directChildrenSumProblemKind 标识统计直接分出总量时共同规则发现的问题
// 类型。枚举、累计与这两类拒绝规则只有一份实现（见
// sumDirectChildrenInitial），由打开未销毁数据、发起销毁、打开已销毁数据
// 三个场景共用；各场景再按自己的失败语义渲染成具体错误。
type directChildrenSumProblemKind int

const (
	// childInitialNotPositive：某个直接子样创建时取得的初始量不大于零。
	childInitialNotPositive directChildrenSumProblemKind = iota
	// directChildrenSumOverflow：各直接子样初始量累加超出 int64 可表示范围。
	directChildrenSumOverflow
)

// directChildrenSumProblem 描述统计直接分出总量时发现的一处共同问题，是
// sumDirectChildrenInitial 的唯一失败结果形式。childID 与 childInitial 仅在
// childInitialNotPositive 时使用，指出是哪份直接子样、它非法的初始量是多少。
type directChildrenSumProblem struct {
	kind         directChildrenSumProblemKind
	childID      string
	childInitial int64
}

// sumDirectChildrenInitial 按子样记录的来源编号（ParentID）统计全部直接
// 子样创建时取得的初始量之和。这是“直接分出量”共同规则的唯一实现，三个
// 使用场景都只维护这一份：
//   - 只统计来源编号直接指向本样品的子样（按编号排序后逐份累计，保证错误
//     指出的子样稳定）；每份只计入它创建时取得的初始量 child.Initial。
//     子样后续继续分装、换持有人、换地点或独立销毁都不改变来源样品已经分
//     出的量，因此绝不改用其当前剩余量；孙样（ParentID 指向其他样品）与
//     同一份数据里的其他原样、兄弟子样、父样都不参与；
//   - 子样列表是否漏列不影响本统计：只要来源编号指向本样品就计入，不要求
//     调用方先通过来源关系一致性核对（未销毁样品恢复本就不做关系核对）；
//   - 任一直接子样初始量不大于零、或各初始量累加超出 int64 支持范围时，
//     返回对应的 directChildrenSumProblem，由调用方按场景渲染为原有分类与
//     说明，绝不回绕成一个小数量继续核对。
func sumDirectChildrenInitial(l *ledger, sampleID string) (int64, *directChildrenSumProblem) {
	childIDs := make([]string, 0)
	for otherID, other := range l.Samples {
		if other != nil && other.ParentID == sampleID {
			childIDs = append(childIDs, otherID)
		}
	}
	sort.Strings(childIDs)
	var total int64
	for _, childID := range childIDs {
		childInitial := l.Samples[childID].Initial
		if childInitial <= 0 {
			return 0, &directChildrenSumProblem{
				kind:         childInitialNotPositive,
				childID:      childID,
				childInitial: childInitial,
			}
		}
		next := total + childInitial
		if next < 0 || next < total {
			return 0, &directChildrenSumProblem{kind: directChildrenSumOverflow}
		}
		total = next
	}
	return total, nil
}

// asActiveRestoreError 按“重新打开未销毁数据失败”的语义把统计问题渲染为
// 包装了 ErrInvalid 的错误：信息写明未销毁样品编号，子样初始量非法时指出
// 该直接子样编号与其初始量，数量统一按三位小数毫升展示，末尾带“，无法
// 恢复”。措辞与历史可观察结果保持一致。
func (p *directChildrenSumProblem) asActiveRestoreError(sampleID string, remaining int64) error {
	const msgTail = "，无法恢复"
	switch p.kind {
	case childInitialNotPositive:
		return fmt.Errorf("%w: 未销毁样品 %q 的直接子样 %q 初始量 %s 毫升必须大于零，无法核对分出数量%s",
			ErrInvalid, sampleID, p.childID, formatUnits(p.childInitial), msgTail)
	default: // directChildrenSumOverflow
		return fmt.Errorf("%w: 未销毁样品 %q 的剩余量 %s 毫升与直接分出总量合计超出可表示范围%s",
			ErrInvalid, sampleID, formatUnits(remaining), msgTail)
	}
}

// asDestroyedRestoreError 按“重新打开已销毁数据失败”的语义把统计问题渲染
// 为包装了 ErrInvalid 的错误：信息写明样品编号，子样初始量非法时指出该
// 直接子样编号与其初始量，累加溢出时给出实际销毁量，数量统一按三位小数
// 毫升展示，末尾带“，无法恢复”。措辞与历史可观察结果保持一致。
func (p *directChildrenSumProblem) asDestroyedRestoreError(sampleID string, destroyedQty int64) error {
	const msgTail = "，无法恢复"
	switch p.kind {
	case childInitialNotPositive:
		return fmt.Errorf("%w: 样品 %q 的直接子样 %q 初始量 %s 毫升必须大于零，无法核对销毁数量%s",
			ErrInvalid, sampleID, p.childID, formatUnits(p.childInitial), msgTail)
	default: // directChildrenSumOverflow
		return fmt.Errorf("%w: 样品 %q 的销毁数量核对中，实际销毁量 %s 毫升与直接子样总量合计超出可表示范围%s",
			ErrInvalid, sampleID, formatUnits(destroyedQty), msgTail)
	}
}

// asDestroyCheckError 按“发起销毁前核对”的语义把统计问题渲染为错误。子样
// 初始量非法与累加溢出沿用原分类，包装 ErrInvalid（数量本身不成立，而非
// 状态冲突）；数量有缺口、来源关系矛盾等状态冲突仍由
// validateDestroyConservation 与 checkDirectChildRelationship 各自按
// ErrConflict 渲染。措辞与历史可观察结果保持一致。
func (p *directChildrenSumProblem) asDestroyCheckError(sampleID string) error {
	switch p.kind {
	case childInitialNotPositive:
		return fmt.Errorf("%w: 样品 %q 的直接子样 %q 初始量 %s 毫升必须大于零，无法核对销毁数量",
			ErrInvalid, sampleID, p.childID, formatUnits(p.childInitial))
	default: // directChildrenSumOverflow
		return fmt.Errorf("%w: 样品 %q 的剩余量与直接分出总量合计超出可表示范围，无法销毁",
			ErrInvalid, sampleID)
	}
}

// accountedOutcomeKind 标识“参与核对数量 + 直接分出总量”这一合计相对样品
// 自身初始量的核对结果。相加、溢出判断与这份比较只有一处实现（见
// checkConservationTotal），由打开未销毁数据、发起销毁、打开已销毁数据
// 三个场景共用；各场景再按自己的失败语义把结果渲染成原有分类与说明。
type accountedOutcomeKind int

const (
	// accountedEqual：合计恰好等于自身初始量（数量守恒）。
	accountedEqual accountedOutcomeKind = iota
	// accountedBelowInitial：合计小于自身初始量（数量有缺口）。这只在允许
	// 保留差额的未销毁旧记录恢复场景合法；发起销毁与已销毁记录恢复都要求
	// 恰好守恒，会把它按各自语义拒绝。
	accountedBelowInitial
	// accountedAboveInitial：合计大于自身初始量。
	accountedAboveInitial
	// accountedNotEqual：在要求“恰好守恒”的场景（发起销毁、已销毁记录
	// 恢复）里，合计不等于自身初始量，即 accountedBelowInitial 或
	// accountedAboveInitial 二者之一；调用方无需区分方向。
	accountedNotEqual
	// accountedOverflow：参与量与直接分出总量相加超出 int64 可表示范围，
	// 绝不能把回绕后的较小数量当作合法合计继续比较。
	accountedOverflow
)

// conservationTotalResult 是 checkConservationTotal 的核对结果。outcome 为
// 结论；total 仅在未溢出时有意义，保存参与量与直接分出总量的精确合计，供
// 调用方在错误说明中展示合计或计算缺口，避免相加逻辑在各处重复。
type conservationTotalResult struct {
	outcome accountedOutcomeKind
	total   int64
}

// checkConservationTotal 核对“参与守恒核对的数量 accountedQty 加上已经直接
// 分出的总量 directChildrenTotal”相对样品自身初始量 initial 的关系，是这条
// 数量守恒规则中“最终合计、超出支持范围判断、与初始量比较”的唯一实现，
// 三个使用场景都只维护这一份：
//   - 发起销毁（见 validateDestroyConservation）：accountedQty 取样品当前
//     全部剩余量，要求合计恰好等于自身初始量；
//   - 重新打开已销毁记录（见 validateDestroyedQuantity）：accountedQty 取
//     保存的实际销毁量，绝不能拿已经归零的当前剩余量代替，同样要求恰好
//     守恒；
//   - 重新打开未销毁旧记录（见 validateActiveQuantities）：accountedQty 取
//     当前剩余量，沿用旧约定允许合计小于初始量，只拒绝合计大于初始量。
//
// directChildrenTotal 必须来自共同统计规则 sumDirectChildrenInitial：每份
// 直接子样只计创建时取得的初始量，子样后续分装、交接或销毁都不改变这笔
// 量，孙样与其他样品不参与；分装子样核对时它自身的初始量就是 initial。
//
// 相加严格检测 int64 溢出（任一操作数可能为负，因此同时检查结果变负或相对
// 两个加数变小），一旦溢出就返回 accountedOverflow，不能把回绕后的较小
// 数量当作合法合计；上限附近恰好守恒（合计 == math.MaxInt64 == initial）
// 必须准确判为守恒。requireExact 为 true 时要求合计恰好等于初始量，小于或
// 大于都归为 accountedNotEqual；为 false 时小于初始量判为
// accountedBelowInitial（调用方据此放行有缺口的未销毁旧记录），大于判为
// accountedAboveInitial。本函数只做计算与判定，不带任何错误分类或措辞，
// 由调用方按场景渲染。
func checkConservationTotal(accountedQty, directChildrenTotal, initial int64, requireExact bool) conservationTotalResult {
	total := accountedQty + directChildrenTotal
	if total < 0 || total < accountedQty || total < directChildrenTotal {
		return conservationTotalResult{outcome: accountedOverflow}
	}
	switch {
	case total == initial:
		return conservationTotalResult{outcome: accountedEqual, total: total}
	case requireExact:
		return conservationTotalResult{outcome: accountedNotEqual, total: total}
	case total < initial:
		return conservationTotalResult{outcome: accountedBelowInitial, total: total}
	default:
		return conservationTotalResult{outcome: accountedAboveInitial, total: total}
	}
}

// validateDestroyConservation 是销毁前的数量守恒核对。销毁只能处理样品
// 当时的全部剩余量，落盘后的销毁记录在重新打开时必须满足“实际销毁量 +
// 直接分出总量 == 自身初始量”，而实际销毁量就是当前剩余量。因此发起销毁
// 时就必须满足：当前剩余量加上已经直接分出的总量恰好等于该样品自身的
// 初始量；数量有缺口（合计小于初始量）时必须拒绝本次销毁，不能先记下
// 销毁量再让文件无法重新打开，也不能把缺口计入销毁量、补建子样或调整
// 初始量来凑守恒。
//
// 这与未销毁旧记录的恢复约定不同：重新打开时，未销毁样品允许合计小于
// 初始量（旧数据的差额不强行补齐，见 validateActiveQuantities），旧记录
// 因此仍能打开和查询；但一旦销毁，记录就必须恰好守恒，所以有缺口的
// 旧样品只能继续作为未销毁记录保留，不能通过销毁“结清”。
//
// 直接分出总量按子样记录的来源编号认定、只计各直接子样创建时取得的初始
// 量（共同统计规则见 sumDirectChildrenInitial）：分装子样申请销毁时按它
// 自身创建时取得的初始量核对，孙样不重复计算，其他原样、兄弟子样和父样
// 都不参与；没有直接子样时，剩余量必须等于自身初始量，哪怕只缺 0.001
// 毫升也拒绝。
// 子样列表与来源编号的关系还必须一致：关系规则只有一份实现（见
// checkDirectChildRelationship），与重新打开已销毁记录时的恢复核对共用，
// 这里按销毁冲突的语义渲染为 ErrConflict。公开操作维护的列表始终一致，
// 但旧文件可能带着互相矛盾的列表，放行会让销毁后的文件在重新打开时因
// 来源关系不符而失败，因此同样拒绝，且不替记录增删或改写子样。调用方
// 已保证样品未销毁、仍有正剩余量且没有待确认交接。任一不符都返回包装了
// ErrConflict 的错误；数量有缺口时信息写明样品编号以及三位小数毫升的
// 初始量、剩余量、直接分出总量和缺口。
func validateDestroyConservation(sampleID string, rec *sampleRecord, l *ledger) error {
	// 先核对子样列表与来源编号的关系一致，再统计直接分出总量，保证销毁
	// 落盘后的记录能通过重新打开时的来源关系与数量守恒核对。核对本身在
	// checkDirectChildRelationship 中只有一份实现，此处按销毁冲突渲染。
	if prob := checkDirectChildRelationship(sampleID, rec.Children, l); prob != nil {
		return prob.asDestroyConflictError(sampleID)
	}

	// 直接分出总量的枚举、累计与拒绝规则与另外两个场景共用同一份实现（见
	// sumDirectChildrenInitial），这里按销毁前核对的语义渲染：子样初始量
	// 非法、子样初始量累加溢出沿用原 ErrInvalid 分类。
	directChildrenTotal, prob := sumDirectChildrenInitial(l, sampleID)
	if prob != nil {
		return prob.asDestroyCheckError(sampleID)
	}

	// 参与核对数量（发起销毁时取样品当前全部剩余量）与直接分出总量相加、
	// 合计溢出判断及与自身初始量的精确比较，与重新打开已销毁记录时的恢复
	// 核对共用同一份实现（见 checkConservationTotal），同一条守恒规则只维护
	// 这一份：相加再溢出按状态冲突 ErrConflict 处理，合计与初始量不相等
	// （数量有缺口或超出）同样按 ErrConflict 拒绝，并给出缺口。
	res := checkConservationTotal(rec.Remaining, directChildrenTotal, rec.Initial, true)
	switch res.outcome {
	case accountedOverflow:
		return fmt.Errorf("%w: 样品 %q 的剩余量 %s 毫升与直接分出总量 %s 毫升合计超出可表示范围，不能销毁",
			ErrConflict, sampleID,
			formatUnits(rec.Remaining), formatUnits(directChildrenTotal))
	case accountedNotEqual:
		gap := rec.Initial - res.total
		return fmt.Errorf("%w: 样品 %q 数量不守恒，不能销毁：自身初始量 %s 毫升，当前剩余量 %s 毫升，已直接分出总量 %s 毫升，剩余量与直接分出总量之和为 %s 毫升，距初始量尚有 %s 毫升缺口；请先核对数量后再销毁",
			ErrConflict, sampleID,
			formatUnits(rec.Initial), formatUnits(rec.Remaining),
			formatUnits(directChildrenTotal), formatUnits(res.total), formatUnits(gap))
	}
	return nil
}

// validatePendingContent 核对一条待确认交接的内容是否与样品当前记录一致。
// 待确认交接表示样品当前全部剩余量尚待指定人员接收：样品必须未销毁且
// 剩余量大于零，交接的交出人、交出地点和交接量必须分别精确等于样品的
// 当前持有人、当前地点和当前剩余量。任一不符都返回包装了 ErrInvalid 的
// 错误，并在信息中写明样品编号、交接编号以及冲突项（人员、地点、数量
// 或销毁状态）；数量冲突同时列出双方数量，统一按三位小数毫升展示。
func validatePendingContent(sampleID string, sample *sampleRecord, tr *transferRecord) error {
	switch {
	case sample.Destroyed != nil:
		return fmt.Errorf("%w: 样品 %q 的待确认交接 %q 仍存在，但样品已销毁，无法恢复",
			ErrInvalid, sampleID, tr.ID)
	case sample.Remaining <= 0:
		return fmt.Errorf("%w: 样品 %q 的待确认交接 %q 仍存在，但样品当前剩余量为 %s 毫升（未销毁），无法恢复",
			ErrInvalid, sampleID, tr.ID, formatUnits(sample.Remaining))
	case tr.FromHolder != sample.Holder:
		return fmt.Errorf("%w: 样品 %q 的待确认交接 %q 的交出人 %q 与样品当前持有人 %q 不一致，无法恢复",
			ErrInvalid, sampleID, tr.ID, tr.FromHolder, sample.Holder)
	case tr.FromLocation != sample.Location:
		return fmt.Errorf("%w: 样品 %q 的待确认交接 %q 的交出地点 %q 与样品当前地点 %q 不一致，无法恢复",
			ErrInvalid, sampleID, tr.ID, tr.FromLocation, sample.Location)
	case tr.Qty != sample.Remaining:
		return fmt.Errorf("%w: 样品 %q 的待确认交接 %q 的交接量 %s 毫升与样品当前剩余量 %s 毫升不一致，无法恢复",
			ErrInvalid, sampleID, tr.ID, formatUnits(tr.Qty), formatUnits(sample.Remaining))
	}
	return nil
}

// validateConfirmedReceipt 核对一条已确认交接是否记录了一次真实成立的转手。
// 已确认交接保存的是当时发生的事实，因此它首先必须关联一份实际存在的样品
// （由调用方保证 sample 非 nil），且交接量本身成立：
//   - 交接量必须大于零：零量或负量的“转手”不可能发生，不能只凭确认人和
//     接收时间齐全就恢复成已确认记录；
//   - 交接量不得超过该样品自身的初始量。这里的上限是样品登记/创建时取得
//     的初始量：原样取登记量，分装子样取它创建时分得的量，不能借用父样、
//     兄弟子样或其他原样的量（子样初始 3.250 毫升却记着 4.000 毫升的已
//     确认交接，即使父样初始量更大也必须拒绝）；交接量恰好等于自身初始量
//     是合法边界。上限取样品自身初始量而非当前剩余量：原样曾以 10.000
//     毫升完成交接，之后分出 3.250 毫升再以剩余的 6.750 毫升转交时，旧
//     交接仍保留当时的 10.000 毫升，不能拿当前剩余量或多次转手量之和
//     限制它。
//
// 此外确认人必须非空（去首尾空白后）且与该交接原先指定的接收人完全一致，
// 接收时间必须存在且不是零时间，并按实际时刻不早于交出时间（恰好相等
// 合法；不同时区表示同一时刻也算相等，时间倒置用 Before 判断而非比较时区
// 偏移后的字面值）。任一不符都返回包装了 ErrInvalid 的错误，信息写明交接
// 编号、关联样品编号以及具体问题（样品缺失由调用方先行处理；交接量为零/
// 负、交接量超过样品自身初始量、缺少/空白确认人、确认人不符、缺少或零值
// 接收时间、时间倒置）；数量不符时同时以三位小数毫升展示交接量与样品
// 自身初始量。绝不靠删除问题交接、补登记样品或调整数量来接受异常记录。
func validateConfirmedReceipt(tr *transferRecord, sample *sampleRecord) error {
	switch {
	case tr.Qty == 0:
		return fmt.Errorf("%w: 已确认交接 %q（样品 %q）的交接量为 0.000 毫升，交接量必须大于零，无法恢复",
			ErrInvalid, tr.ID, tr.SampleID)
	case tr.Qty < 0:
		return fmt.Errorf("%w: 已确认交接 %q（样品 %q）的交接量 %s 毫升不能为负，交接量必须大于零，无法恢复",
			ErrInvalid, tr.ID, tr.SampleID, formatUnits(tr.Qty))
	case sample.Initial <= 0:
		return fmt.Errorf("%w: 已确认交接 %q 关联的样品 %q 自身初始量 %s 毫升不大于零，无法核对交接量，无法恢复",
			ErrInvalid, tr.ID, tr.SampleID, formatUnits(sample.Initial))
	case tr.Qty > sample.Initial:
		return fmt.Errorf("%w: 已确认交接 %q（样品 %q）的交接量 %s 毫升超过样品自身初始量 %s 毫升，交接量不得大于样品自身初始量，无法恢复",
			ErrInvalid, tr.ID, tr.SampleID, formatUnits(tr.Qty), formatUnits(sample.Initial))
	}
	switch {
	case tr.ConfirmedBy == "":
		return fmt.Errorf("%w: 已确认交接 %q（样品 %q）缺少确认人，无法恢复",
			ErrInvalid, tr.ID, tr.SampleID)
	case strings.TrimSpace(tr.ConfirmedBy) == "":
		return fmt.Errorf("%w: 已确认交接 %q（样品 %q）的确认人不能只有空白，无法恢复",
			ErrInvalid, tr.ID, tr.SampleID)
	case tr.ConfirmedBy != tr.ToHolder:
		return fmt.Errorf("%w: 已确认交接 %q（样品 %q）的确认人 %q 与原定接收人 %q 不一致，无法恢复",
			ErrInvalid, tr.ID, tr.SampleID, tr.ConfirmedBy, tr.ToHolder)
	}
	switch {
	case tr.ReceivedAt == nil:
		return fmt.Errorf("%w: 已确认交接 %q（样品 %q）缺少接收时间，无法恢复",
			ErrInvalid, tr.ID, tr.SampleID)
	case tr.ReceivedAt.IsZero():
		return fmt.Errorf("%w: 已确认交接 %q（样品 %q）的接收时间为零值，无法恢复",
			ErrInvalid, tr.ID, tr.SampleID)
	case tr.ReceivedAt.Before(tr.HandedOverAt):
		return fmt.Errorf("%w: 已确认交接 %q（样品 %q）的接收时间 %s 早于交出时间 %s，无法恢复",
			ErrInvalid, tr.ID, tr.SampleID,
			tr.ReceivedAt.Format(time.RFC3339), tr.HandedOverAt.Format(time.RFC3339))
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
// 销毁要求数量恰好守恒：当前剩余量加上已经直接分出的总量（按子样记录的
// 来源编号认定，每份只计子样创建时取得的初始量）必须恰好等于该样品自身
// 的初始量；没有直接子样时剩余量必须等于自身初始量。数量有缺口（哪怕只
// 缺 0.001 毫升）时返回包装了 ErrConflict 的错误且不返回样品结果——这类
// 未销毁旧记录重新打开时仍按旧约定允许合计小于初始量、可继续查询，但不
// 能销毁，因为销毁记录在重新打开时必须数量守恒；不会把缺口计入销毁量、
// 补建子样或调整初始量来让销毁成功，被拒绝的样品仍为未销毁，数量、持有人、
// 地点、来源关系、子样列表、保管历史与文件内容全部保持原样。
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
	if err := validateDestroyConservation(sampleID, rec, s.data); err != nil {
		return nil, err
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
