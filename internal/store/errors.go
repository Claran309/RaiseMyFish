package store

import "errors"

// repository错误不携带用户正文，调用方通过errors.Is分支处理。
var (
	ErrInvalid  = errors.New("存储入参无效")
	ErrNotFound = errors.New("存储记录不存在")
	ErrConflict = errors.New("存储状态或唯一键冲突")
)
