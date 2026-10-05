package custody

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// mlScale 是数量的定点刻度：所有数量以千分之一毫升为单位的整数保存。
// 合法输入最多三位小数，因此登记、分装扣减与汇总都是精确的整数运算，
// 不会因浮点舍入多出或丢失样品。
const mlScale int64 = 1000

// 数量只接受整数部分至少一位、小数部分零到三位的十进制写法，不接受符号、
// 小数点开头或科学计数法。调用方仍需单独判断数值必须大于零。
var quantityPattern = regexp.MustCompile(`^\d+(?:\.\d{1,3})?$`)

// parseQuantity 把三位小数量（单位：毫升）解析为千分之一毫升的整数。
func parseQuantity(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if !quantityPattern.MatchString(s) {
		return 0, fmt.Errorf("%w: 数量 %q 必须是大于零且最多三位小数的十进制毫升数", ErrInvalid, raw)
	}

	wholePart := s
	fracPart := ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		wholePart = s[:dot]
		fracPart = s[dot+1:]
	}

	whole, err := strconv.ParseInt(wholePart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: 数量 %q 超出可表示范围", ErrInvalid, raw)
	}
	for len(fracPart) < 3 {
		fracPart += "0"
	}
	frac, err := strconv.ParseInt(fracPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: 数量 %q 超出可表示范围", ErrInvalid, raw)
	}

	units := whole*mlScale + frac
	if units <= 0 {
		return 0, fmt.Errorf("%w: 数量 %q 必须大于零", ErrInvalid, raw)
	}
	if units/mlScale != whole {
		return 0, fmt.Errorf("%w: 数量 %q 超出可表示范围", ErrInvalid, raw)
	}
	return units, nil
}

// addUnits 返回 a+b；结果超出 int64 可表示范围时 ok 为 false，
// 调用方据此把过大的合计按无效数据拒绝，而不是让溢出悄悄回绕。
func addUnits(a, b int64) (sum int64, ok bool) {
	sum = a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, false
	}
	return sum, true
}

// formatUnits 把内部整数数量统一渲染为三位小数的毫升字符串。
func formatUnits(units int64) string {
	if units < 0 {
		return "-" + formatUnits(-units)
	}
	return fmt.Sprintf("%d.%03d", units/mlScale, units%mlScale)
}

// trimRequired 去掉首尾空白，并要求结果非空。
func trimRequired(field, raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", fmt.Errorf("%w: %s去掉首尾空白后不能为空", ErrInvalid, field)
	}
	return v, nil
}
