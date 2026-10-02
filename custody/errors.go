package custody

import "errors"

// 操作中可能返回的错误。调用方可以通过 errors.Is 判定错误类别。
var (
	// ErrNotFound 表示请求的样品或交接编号在本地数据中不存在。
	ErrNotFound = errors.New("custody: not found")

	// ErrDuplicateID 表示样品编号或交接编号重复。
	ErrDuplicateID = errors.New("custody: duplicate id")

	// ErrInvalidQuantity 表示数量不是大于零、最多三位小数的十进制值。
	ErrInvalidQuantity = errors.New("custody: invalid quantity")

	// ErrInvalidInput 表示传入的人员、地点等参数不合法。
	ErrInvalidInput = errors.New("custody: invalid input")

	// ErrConflict 表示操作与当前状态冲突（例如有待确认交接、余量为零）。
	ErrConflict = errors.New("custody: operation conflict")

	// ErrClosed 表示 Store 已经关闭。
	ErrClosed = errors.New("custody: store closed")
)
