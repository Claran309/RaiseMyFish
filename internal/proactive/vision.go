package proactive

// 本文件定义「详细识图」：主动弹幕先把画面讲清楚，再交给 Brain 决定说什么。
//
// 与旧流程的区别：旧 prompt 直接要「一句弹幕」，视觉模型被迫既看又写；
// 现在识图只负责描述画面（人物/动作/环境/文字/氛围），弹幕交给复用的人设管线写。

import "context"

// VisionPrompt 是固定的识图提示词（派发单指定，不要随意改）。
//
// 它是「上下文生产者」而不是「弹幕作者」：输出可以多句、可以长，
// 15 字限制在发送前才施加。
const VisionPrompt = "你是视图模型，专门用于识图，请你详细观察图片，并详细说明解释图中内容"

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
func VisionFromFrames(f FrameDescriber) VisionDescriber {
	if f == nil {
		return nil
	}
	return frameVision{inner: f}
}

type frameVision struct{ inner FrameDescriber }

func (v frameVision) Describe(ctx context.Context, framePath string) (string, error) {
	return v.inner.Describe(ctx, []string{framePath}, VisionPrompt)
}
