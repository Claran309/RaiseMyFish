package persona

// 本文件只负责 meta.yaml：类型定义、严格结构化解码、元信息字段校验。
// 契约：contracts/persona.md（P0-PACK-1）§1、§2。

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Meta 是人格包元信息，字段与 YAML 键一一对应（契约 §1，不得增删字段）。
//
// 这里只放「稳定元信息」。会变的运行时事实（余额、礼物账本、房间号、mid）
// 一律不进人格包（docs/08-persona.md §6），走配置或记忆。
type Meta struct {
	Name        string   `yaml:"name"`
	DisplayName string   `yaml:"display_name"`
	Version     string   `yaml:"version"`
	Channels    []string `yaml:"channels"`
	TTSVoice    string   `yaml:"tts_voice"`
	Description string   `yaml:"description"`
}

// reQuotedSpan 用于遮蔽 YAML 错误信息里被引号包住的原始标量。
//
// 契约 §2 要求错误「不回显文件正文」，而 yaml.v3 的类型错误会把出错的原始值
// 直接写进消息（形如 cannot unmarshal !!str `xxx` into ...），所以统一替换掉。
var reQuotedSpan = regexp.MustCompile("`[^`]*`")

// decodeMeta 严格解析 meta.yaml 内容。
//
// 拒绝：空文件、未知字段、重复字段、类型错误、多个 YAML 文档、任何语法错误。
// 允许：正常 YAML 引号、中文、行内数组与块状数组——都交给 yaml.v3，不自维护方言。
func decodeMeta(data []byte) (Meta, error) {
	var m Meta

	dec := yaml.NewDecoder(bytes.NewReader(data))
	// KnownFields 让未知字段直接报错，避免「多写一个键也不吭声」
	dec.KnownFields(true)

	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			// 空文件或只有注释：没有可解码的文档
			return Meta{}, errors.New("内容为空，没有可解码的 YAML 文档")
		}
		return Meta{}, fmt.Errorf("YAML 解码失败：%w", sanitizeYAMLError(err))
	}

	// 再解一次：能读出第二个文档就说明是 `---` 分隔的多文档，契约要求拒绝
	var extra any
	switch err := dec.Decode(&extra); {
	case err == nil:
		return Meta{}, errors.New("包含多个 YAML 文档，只允许一个")
	case errors.Is(err, io.EOF):
		// 正常结束
	default:
		return Meta{}, fmt.Errorf("YAML 解码失败：%w", sanitizeYAMLError(err))
	}

	return m, nil
}

// sanitizeYAMLError 丢弃 yaml 错误里可能夹带的原始标量，只保留结构信息（行号、字段名）。
func sanitizeYAMLError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(reQuotedSpan.ReplaceAllString(err.Error(), "`…`"))
}

// validateMeta 校验元信息字段（契约 §2）。
//
// Name / Version 必填；Channels 至少一个、不重复、只允许五种场景键；
// DisplayName / TTSVoice / Description 可选（TTSVoice 只是元数据，不实现语音能力）。
// 不强制 SemVer：既有 version: v1 必须继续有效。
func validateMeta(m Meta) error {
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("字段 name 不能为空")
	}
	if strings.TrimSpace(m.Version) == "" {
		return errors.New("字段 version 不能为空")
	}
	if len(m.Channels) == 0 {
		return errors.New("字段 channels 至少要声明一个场景键")
	}

	seen := make(map[string]struct{}, len(m.Channels))
	for _, c := range m.Channels {
		if !isContextKey(c) {
			return fmt.Errorf("字段 channels 含未知场景键 %q（允许 %s）",
				c, strings.Join(ContextKeys(), " / "))
		}
		if _, dup := seen[c]; dup {
			return fmt.Errorf("字段 channels 含重复场景键 %q", c)
		}
		seen[c] = struct{}{}
	}
	return nil
}
