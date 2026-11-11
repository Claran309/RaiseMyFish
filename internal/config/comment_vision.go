package config

// 本文件定义 `[comment_vision]` 段：把评论附图送给视觉模型转述后再注入上下文。

import (
	"errors"
	"strings"
)

// CommentVision 是评论图片识别配置。
//
// 默认关闭，避免未配置视觉模型时每条带图评论都产生一次失败调用。
// 开启后单条评论最多看 max_images 张图，整体限时 timeout_ms；失败只降级为没有图。
type CommentVision struct {
	Enabled   bool   `toml:"enabled"`
	LLMRoute  string `toml:"llm_route"`
	TimeoutMS int    `toml:"timeout_ms"`
	MaxImages int    `toml:"max_images"`
}

func defaultCommentVision() CommentVision {
	return CommentVision{LLMRoute: "default", TimeoutMS: 20000, MaxImages: 2}
}

// ApplyDefaults 只补零值。
func (v *CommentVision) ApplyDefaults() {
	if v == nil {
		return
	}
	if strings.TrimSpace(v.LLMRoute) == "" {
		v.LLMRoute = "default"
	}
	if v.TimeoutMS == 0 {
		v.TimeoutMS = 20000
	}
	if v.MaxImages == 0 {
		v.MaxImages = 2
	}
}

// Validate 只校验开启后的静态配置与预算；路由可用性在 Config 里结合 LLM 校验。
func (v CommentVision) Validate() error {
	if !v.Enabled {
		return nil
	}
	if strings.TrimSpace(v.LLMRoute) == "" {
		return errors.New("评论图片识别需要配置 llm_route")
	}
	if v.TimeoutMS < 1000 || v.TimeoutMS > 60000 {
		return errors.New("评论图片识别 timeout_ms 需在1000至60000之间")
	}
	if v.MaxImages < 1 || v.MaxImages > 4 {
		return errors.New("评论图片识别 max_images 需在1至4之间")
	}
	return nil
}
