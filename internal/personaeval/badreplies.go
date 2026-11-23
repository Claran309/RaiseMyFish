package personaeval

// 本文件把 docs/eval/2026-11-18-bad-replies.md 的真实坏回复固化成可自动判分的用例。
// 判分口径来自文档三条：不编造 / 不重复 / 不装傻。
// 词法评分只能卡住明显违规（反问装傻、编造断言、重复口头禅），
// 不冒充语义判断；不通过的条目会逐条列出，便于人工复核。

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// BadReplyCase 是一条坏回复回归用例。
type BadReplyCase struct {
	ID       string   `json:"id"`
	Channel  string   `json:"channel"`
	Input    string   `json:"input"`
	BadReply string   `json:"bad_reply"`
	Problems []string `json:"problems"`
}

// BadReplyReport 是坏回复检测结果；只给统计与 ID，不打印正文。
type BadReplyReport struct {
	Total    int      `json:"total"`
	Detected int      `json:"detected"`
	Missed   []string `json:"missed,omitempty"`
	Passed   bool     `json:"passed"`
}

var badReplyPatterns = map[string][]*regexp.Regexp{
	"装傻":   {regexp.MustCompile(`\?\?|你倒是说清|没听过|不知道你说什么`)},
	"嘴硬":   {regexp.MustCompile(`鱼自己就能|才不用|根本没事`)},
	"画面认错": {regexp.MustCompile(`种地怎么了|明明就是种地`)},
	"公式化":  {regexp.MustCompile(`只认一个缸|一个缸`)},
	"编造断言": {regexp.MustCompile(`就是种地|肯定在种地|绝对在种地|真种地`)},
}

// LoadBadReplyCases 读取坏回复用例 JSONL。
func LoadBadReplyCases(path string) ([]BadReplyCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取坏回复用例失败：%w", err)
	}
	var cases []BadReplyCase
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" {
			continue
		}
		var c BadReplyCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("坏回复用例第%d行无效：%w", i+1, err)
		}
		if c.ID == "" || c.Input == "" || c.BadReply == "" {
			return nil, fmt.Errorf("坏回复用例第%d行缺字段", i+1)
		}
		if len(c.Problems) == 0 {
			return nil, fmt.Errorf("坏回复用例%s未标问题", c.ID)
		}
		cases = append(cases, c)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("坏回复用例为空：%s", path)
	}
	return cases, nil
}

// CheckBadReplies 验证检测器能识别每条坏回复；漏检逐条列出，不静默通过。
func CheckBadReplies(cases []BadReplyCase) BadReplyReport {
	report := BadReplyReport{Total: len(cases)}
	for _, c := range cases {
		if detectBadReply(c.BadReply, c.Problems) {
			report.Detected++
			continue
		}
		report.Missed = append(report.Missed, c.ID)
	}
	report.Passed = report.Detected == report.Total
	return report
}

// detectBadReply 命中任一问题标签即视为检出。
func detectBadReply(reply string, problems []string) bool {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return true
	}
	for _, problem := range problems {
		for _, pattern := range badReplyPatterns[problem] {
			if pattern.MatchString(reply) {
				return true
			}
		}
	}
	return false
}
