package provider

// 本文件是 Brain 与 ReAct 共用的保守字节预算口径。
//
// 估计器版本要随日志一起记录：它回答的是「按哪个口径算出来的预算」，
// 不是模型精确 token 数；服务端真实用量仍以账本里的 usage 为准。

// EstimatorVersion 是当前预算估算口径的版本号。
// 改变估算公式（字节口径、消息框架余量）时必须递增，否则历史日志不可比。
const EstimatorVersion = "utf8-bytes-v1"

// MessageOverhead 是一次请求的固定消息框架余量（与 contracts/brain.md 一致）。
const MessageOverhead = 64

// EstimateMessages 返回一次请求的保守字节估计：固定框架余量 + 全部消息正文 UTF-8 字节数。
//
// 刻意保持简单可复现：不引入 tokenizer，不按模型差异变化；
// 调用方只能在「多留余量」的方向上做调整，不能把它当成精确 token 数。
func EstimateMessages(messages []Message) int {
	n := MessageOverhead
	for _, m := range messages {
		n += len(m.Content)
	}
	return n
}
