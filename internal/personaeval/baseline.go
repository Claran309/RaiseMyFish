package personaeval

// 本文件提供两条「无模型」评测能力：
//   - LoadBaselineReplies：解析 baseline.md 的人工基准回复；
//   - FixtureGenerator：把基准回复回放成 Generator，供 CI 在无密钥时跑通评测链路。
//
// 回放结果只用于验证「评分器 ↔ 基准回复」自洽，不代表真实模型通过率；
// SourceKind 必须保持 test，报告里也不允许冒充 model_provider。

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
)

// LoadBaselineReplies 解析 `## case:<id>` 段，返回 id → 正文。
//
// 段内允许空行与多行正文；遇到下一个 `## ` 标题即结束。
// 没有正文的用例不进入结果，由调用方决定是否视为缺失。
func LoadBaselineReplies(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取基准回复失败：%w", err)
	}
	defer file.Close()

	replies := map[string]string{}
	var current string
	var body []string
	flush := func() {
		if current == "" {
			return
		}
		text := strings.TrimSpace(strings.Join(body, "\n"))
		if text != "" {
			replies[current] = text
		}
		body = body[:0]
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.HasPrefix(line, "## case:") {
			flush()
			current = strings.TrimSpace(strings.TrimPrefix(line, "## case:"))
			continue
		}
		if strings.HasPrefix(line, "## ") {
			flush()
			current = ""
			continue
		}
		if current != "" {
			body = append(body, line)
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取基准回复：%w", err)
	}
	if len(replies) == 0 {
		return nil, fmt.Errorf("基准回复为空：%s", path)
	}
	return replies, nil
}

// FixtureGenerator 回放 baseline.md；缺回复时返回错误，不静默跳过。
type FixtureGenerator struct {
	Replies map[string]string
}

// NewFixtureGenerator 读取基准文件并返回回放生成器。
func NewFixtureGenerator(path string) (*FixtureGenerator, error) {
	replies, err := LoadBaselineReplies(path)
	if err != nil {
		return nil, err
	}
	return &FixtureGenerator{Replies: replies}, nil
}

// Name 不能包含 baseline：personaeval.Run 会拒绝冒充基线的生成器。
func (g *FixtureGenerator) Name() string { return "test:fixture-replay" }

// Generate 返回基准回复；用例缺基准时明确报错，不伪造空回复。
func (g *FixtureGenerator) Generate(_ context.Context, in Input) (Generation, error) {
	reply, ok := g.Replies[in.ID]
	if !ok || strings.TrimSpace(reply) == "" {
		return Generation{}, fmt.Errorf("基准回复缺少用例 %s", in.ID)
	}
	return Generation{RawOutput: reply, DeliveredOutput: reply}, nil
}
