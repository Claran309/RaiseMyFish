package knowledge

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
)

func textValid(s string) bool { return strings.TrimSpace(s) != "" && utf8.ValidString(s) }

// ValidateScope 校验范围形状，不从名称、正文或 private 标记推导身份。
func ValidateScope(scope Scope) error {
	if !scope.Channel.Valid() || !textValid(scope.Session) {
		return errors.New("知识范围的通道或会话无效")
	}
	if (scope.Channel == event.ChLive || scope.Channel == event.ChComment) && scope.Private || scope.Channel == event.ChDM && !scope.Private {
		return errors.New("知识范围的公开私密标记无效")
	}
	return nil
}

func sceneFor(scope Scope) string {
	if scope.Channel == event.ChQQ {
		if scope.Private {
			return "qq_private"
		}
		return "qq_group"
	}
	return string(scope.Channel)
}

func validateScenes(scenes []string) error {
	if len(scenes) == 0 || len(scenes) > 5 {
		return errors.New("知识适用场景数量无效")
	}
	seen := make(map[string]bool, len(scenes))
	for _, s := range scenes {
		switch s {
		case "live", "comment", "dm", "qq_private", "qq_group":
		default:
			return errors.New("知识适用场景无效")
		}
		if seen[s] {
			return errors.New("知识适用场景重复")
		}
		seen[s] = true
	}
	return nil
}

func allowedScene(scenes []string, scene string) bool {
	for _, s := range scenes {
		if s == scene {
			return true
		}
	}
	return false
}

func sourceValid(source string) bool {
	if !textValid(source) || source != strings.TrimSpace(source) || strings.HasPrefix(source, "/") || strings.HasPrefix(source, `\`) || strings.HasPrefix(strings.ToLower(source), "file:") {
		return false
	}
	if len(source) > 2 && source[1] == ':' && (source[2] == '/' || source[2] == '\\') {
		return false
	}
	return true
}

func validateItem(item Item) error {
	if !textValid(item.ID) || !sourceValid(item.Source) || !textValid(item.Text) || item.Priority < 0 || item.Priority > 100 {
		return errors.New("知识条目的标识、来源、正文或优先级无效")
	}
	if err := validateScenes(item.Scenes); err != nil {
		return err
	}
	if !item.Private {
		if item.Origin != nil {
			return errors.New("公开知识不能携带私密来源范围")
		}
		return nil
	}
	if item.Origin == nil || !item.Origin.Private {
		return errors.New("私密知识缺少私密来源范围")
	}
	if err := ValidateScope(*item.Origin); err != nil {
		return fmt.Errorf("私密知识来源范围：%w", err)
	}
	return nil
}

func inScope(item Item, scope Scope) bool {
	if !allowedScene(item.Scenes, sceneFor(scope)) {
		return false
	}
	return !item.Private || item.Origin != nil && scope.Private && *item.Origin == scope
}

// Validate 是额外防线，不能代替 Reader 的查询层过滤与来源审核。
func (c Context) Validate() error {
	if err := ValidateScope(c.Scope); err != nil {
		return err
	}
	if len(c.Revision) != 64 {
		return errors.New("知识版本必须为小写SHA256")
	}
	for _, b := range c.Revision {
		if (b < '0' || b > '9') && (b < 'a' || b > 'f') {
			return errors.New("知识版本必须为小写SHA256")
		}
	}
	seen := make(map[string]bool, len(c.Items))
	for _, item := range c.Items {
		if err := validateItem(item); err != nil {
			return err
		}
		if seen[item.ID] {
			return errors.New("知识条目标识重复")
		}
		seen[item.ID] = true
		if !inScope(item, c.Scope) {
			return errors.New("知识条目不在目标范围内")
		}
	}
	return nil
}
