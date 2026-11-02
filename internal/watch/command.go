package watch

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Commander 执行外部命令并返回 stdout；测试用假实现避免碰真实 ffmpeg。
type Commander interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecCommander 返回真实进程执行器。
func ExecCommander() Commander { return execCommander{} }

type execCommander struct{}

func (execCommander) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s 被中止：%w", name, ctx.Err())
		}
		return nil, fmt.Errorf("%s 执行失败（%s）：%w", name, sanitizeOutput(stderr.String()), err)
	}
	return stdout.Bytes(), nil
}

// URL 与 Cookie 都可能出现在外部命令的 stderr 里（例如带 token 的流地址），
// 日志只保留脱敏后的短摘要。
var (
	outputURLPattern    = regexp.MustCompile(`(?i)https?://\S+`)
	outputCookiePattern = regexp.MustCompile(`(?i)cookie:\s*[^\r\n]+`)
)

func sanitizeOutput(raw string) string {
	text := outputURLPattern.ReplaceAllString(raw, "[url]")
	text = outputCookiePattern.ReplaceAllString(text, "Cookie: [redacted]")
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > 300 {
		text = string(runes[:300]) + "…"
	}
	if text == "" {
		return "无输出"
	}
	return text
}
