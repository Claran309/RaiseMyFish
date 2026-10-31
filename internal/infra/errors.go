package infra

import "fmt"

// privateCause保留错误链用于errors.Is/As，展示时不回显连接参数。
type privateCause struct{ cause error }

func (e *privateCause) Error() string { return "底层连接错误（详情已隐藏）" }
func (e *privateCause) Unwrap() error { return e.cause }

func connectionError(message string, cause error) error {
	return fmt.Errorf("%s：%w", message, &privateCause{cause: cause})
}
