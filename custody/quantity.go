package custody

import (
	"fmt"
	"math"
	"strings"
)

// 内部以“千分之一毫升”为单位的整数表示数量，避免浮点舍入造成样品多出或丢失。
// 对外查询时统一格式化为三位小数的十进制字符串。

// parseQuantity 解析一个十进制数量，只接受大于零、最多三位小数的值，
// 返回千分之一毫升的整数。
func parseQuantity(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("%w: 数量为空", ErrInvalidQuantity)
	}
	if strings.ContainsAny(s, " \t") {
		return 0, fmt.Errorf("%w: 数量不能包含空白: %q", ErrInvalidQuantity, s)
	}
	parts := strings.SplitN(s, ".", 2)
	intPart := parts[0]
	if intPart == "" {
		return 0, fmt.Errorf("%w: 数量缺少整数部分: %q", ErrInvalidQuantity, s)
	}

	whole := int64(0)
	for _, r := range intPart {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%w: 数量必须是正十进制值: %q", ErrInvalidQuantity, s)
		}
		d := int64(r - '0')
		if whole > (math.MaxInt64-d)/10 {
			return 0, fmt.Errorf("%w: 数量过大: %q", ErrInvalidQuantity, s)
		}
		whole = whole*10 + d
	}

	frac := int64(0)
	if len(parts) == 2 {
		f := parts[1]
		if f == "" {
			return 0, fmt.Errorf("%w: 数量缺少小数部分: %q", ErrInvalidQuantity, s)
		}
		if len(f) > 3 {
			return 0, fmt.Errorf("%w: 数量最多三位小数: %q", ErrInvalidQuantity, s)
		}
		for _, r := range f {
			if r < '0' || r > '9' {
				return 0, fmt.Errorf("%w: 数量必须是正十进制值: %q", ErrInvalidQuantity, s)
			}
			frac = frac*10 + int64(r-'0')
		}
		for i := len(f); i < 3; i++ {
			frac *= 10
		}
	}

	if whole > math.MaxInt64/1000 {
		return 0, fmt.Errorf("%w: 数量过大: %q", ErrInvalidQuantity, s)
	}
	qty := whole*1000 + frac
	if qty <= 0 {
		return 0, fmt.Errorf("%w: 数量必须大于零: %q", ErrInvalidQuantity, s)
	}
	return qty, nil
}

// formatQuantity 把千分之一毫升的整数格式化为三位小数的十进制字符串。
func formatQuantity(q int64) string {
	if q < 0 {
		return fmt.Sprintf("-%d.%03d", -q/1000, -q%1000)
	}
	return fmt.Sprintf("%d.%03d", q/1000, q%1000)
}
