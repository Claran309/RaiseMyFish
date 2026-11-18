package proactive

// 本文件定义「详细识图」：主动弹幕先把画面讲清楚，再交给 Brain 决定说什么。
//
// 与旧流程的区别：旧 prompt 直接要「一句弹幕」，视觉模型被迫既看又写；
// 现在识图只负责描述画面（人物/动作/环境/文字/氛围），弹幕交给复用的人设管线写。

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// VisionPrompt 是固定的识图提示词（派发单指定，不要随意改）。
//
// 它是「上下文生产者」而不是「弹幕作者」：输出可以多句、可以长，
// 15 字限制在发送前才施加。
const VisionPrompt = "只描述画面里能直接确认的事实：" +
	"① 有谁、在做什么动作；② 画面上能看清的文字/界面元素；③ 场景与画面风格（像素/3D/真人等）。" +
	"看不出来的东西不要写、不要猜。" +
	"禁止推断玩法或游戏类型——蹲着、走动、点鼠标、摆东西都不等于在种地/钓鱼/打架/做饭。" +
	"禁止编造作品名：画面里没有写出名字时，只能写「具体作品名看不清」。" +
	"不要用观众印象、以往直播内容或常识补充当前画面里没有的信息。"

// VisionDescriber 详细识图：给定一帧，返回多句画面描述。
type VisionDescriber interface {
	Describe(ctx context.Context, framePath string) (string, error)
}

// FrameDescriber 是既有的「多帧 + 自定义提示词」视觉接口（watch.OpenAIDescriber 满足）。
type FrameDescriber interface {
	Describe(ctx context.Context, frames []string, prompt string) (string, error)
}

// VisionFromFrames 把多帧描述器适配成单帧详细识图，提示词固定为 VisionPrompt。
//
// 这样 cmd 侧不用改视觉客户端，只需换一层适配。
func VisionFromFrames(f FrameDescriber, log *zap.Logger) VisionDescriber {
	if f == nil {
		return nil
	}
	return frameVision{inner: f, log: log}
}

type frameVision struct {
	inner FrameDescriber
	log   *zap.Logger
}

func (v frameVision) Describe(ctx context.Context, framePath string) (string, error) {
	started := time.Now()
	text, err := v.inner.Describe(ctx, []string{framePath}, VisionPrompt)
	// 视觉描述落日志：复盘「鱼看到了什么」的唯一依据（主动弹幕路径）。
	if v.log != nil {
		if err != nil {
			v.log.Warn("视觉描述失败", zap.String("action", "vision_desc"),
				zap.Int64("ms", time.Since(started).Milliseconds()), zap.Error(err))
		} else {
			v.log.Info("视觉描述", zap.String("action", "vision_desc"),
				zap.Int("runes", len([]rune(text))),
				zap.Int64("ms", time.Since(started).Milliseconds()),
				zap.String("text", truncLog(text, 200)))
		}
	}
	return text, err
}

// truncLog 按 rune 截断，日志摘要用。
func truncLog(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
