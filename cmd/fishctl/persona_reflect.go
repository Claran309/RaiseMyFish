package main

// 本文件实现 R3 的真实录制对照：同一输入分别 Reflect=off/on 各生成一次，
// 由 LLM judge 判定 improved/harmed/neutral，产出可被 `cmd/eval --reflection-runs`
// 直接复算的 JSONL。
//
// 边界：
//   - 只调用配置里的 OpenAI 兼容 Provider；不读 .env 内容到日志；
//   - 不写发送通道、不投递平台消息；
//   - judge 与生成使用同一模型，但温度更低；结论只作为待人工复核的自动判定。

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/personaeval"
	"RaiseMyFish/internal/reasoning"
	"go.uber.org/zap"
)

// reflectLLM 把 Provider 适配成 reasoning.LLM，并累计真实用量。
type reflectLLM struct {
	client    provider.Provider
	model     string
	maxTokens int
	temp      float64
	mu        sync.Mutex
	calls     int
	inTokens  int
	outTokens int
}

func (l *reflectLLM) Complete(ctx context.Context, system, user string) (string, error) {
	resp, err := l.client.Chat(ctx, provider.Request{
		Model:       l.model,
		Messages:    []provider.Message{{Role: "system", Content: system}, {Role: "user", Content: user}},
		MaxTokens:   l.maxTokens,
		Temperature: l.temp,
	})
	l.mu.Lock()
	l.calls++
	if resp.Usage.InputTokens != nil {
		l.inTokens += *resp.Usage.InputTokens
	}
	if resp.Usage.OutputTokens != nil {
		l.outTokens += *resp.Usage.OutputTokens
	}
	l.mu.Unlock()
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

func (l *reflectLLM) snapshot() (calls, in, out int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls, l.inTokens, l.outTokens
}

// reflectRun 是一条录制结果，字段与 personaeval.ReflectionRun 对齐。
type reflectRun struct {
	CaseID    string  `json:"case_id"`
	Reflect   bool    `json:"reflect"`
	Changed   bool    `json:"changed"`
	Skipped   string  `json:"skipped_reason,omitempty"`
	Verdict   string  `json:"verdict,omitempty"`
	Draft     string  `json:"draft,omitempty"`
	Reflected string  `json:"reflected,omitempty"`
	OffMS     int64   `json:"off_ms"`
	OnMS      int64   `json:"on_ms"`
	OffIn     int     `json:"off_in_tokens"`
	OnIn      int     `json:"on_in_tokens"`
	OffCost   float64 `json:"-"`
	OnCost    float64 `json:"-"`
}

// reflectReport 是录制文件旁的汇总。
type reflectReport struct {
	Cases           int     `json:"cases"`
	Evaluated       int     `json:"evaluated"`
	Improved        int     `json:"improved"`
	Harmed          int     `json:"harmed"`
	Neutral         int     `json:"neutral"`
	ImproveRate     float64 `json:"improve_rate"`
	HarmRate        float64 `json:"harm_rate"`
	ChangedRate     float64 `json:"changed_rate"`
	SkipRate        float64 `json:"skip_rate"`
	AvgLatencyDelta float64 `json:"avg_latency_delta_ms"`
	AvgTokenDelta   float64 `json:"avg_token_delta"`
	JudgePending    bool    `json:"judge_pending"`
}

func runPersonaReflect(ctx context.Context, configPath, envPath string, args []string, stdout, stderr io.Writer) (result error) {
	fs := flag.NewFlagSet("persona reflect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	packFlag := fs.String("pack", "", "人格目录或 personas 下包名")
	casesFlag := fs.String("cases", "", "反思用例 JSONL；默认包内 eval/reflection-cases.jsonl")
	route := fs.String("route", "default", "模型配置路由")
	output := fs.String("output", "", "新建证据目录（必须不存在）")
	timeout := fs.Duration("timeout", 40*time.Minute, "整轮时间上限")
	limit := fs.Int("limit", 0, "最多跑多少条（0=全部）")
	if err := fs.Parse(args); err != nil {
		return errors.New("反思对照参数无效")
	}
	if fs.NArg() != 0 || *output == "" || *timeout <= 0 || *timeout > time.Hour*2 {
		return errors.New("用法：fishctl persona reflect --output <新目录> [--limit N]")
	}
	cfg, err := config.Load(configPath, envPath)
	if err != nil {
		return err
	}
	if !cfg.LLM.Enabled {
		return errors.New("反思对照需要启用 llm")
	}
	if *packFlag != "" {
		cfg.LLM.PersonaDir = *packFlag
		if !strings.ContainsAny(*packFlag, `/\`) {
			cfg.LLM.PersonaDir = filepath.Join("personas", *packFlag)
		}
	}
	casesPath := *casesFlag
	if casesPath == "" {
		casesPath = filepath.Join(cfg.LLM.PersonaDir, "eval", "reflection-cases.jsonl")
	}
	cases, err := personaeval.LoadReflectionCases(casesPath)
	if err != nil {
		return err
	}
	if *limit > 0 && *limit < len(cases) {
		cases = cases[:*limit]
	}
	pack, err := persona.Load(ctx, cfg.LLM.PersonaDir)
	if err != nil {
		return err
	}
	name, p, model, err := cfg.LLM.Resolve(*route)
	if err != nil {
		return err
	}
	client, err := provider.NewOpenAI(name, p.BaseURL, p.APIKey, &http.Client{}, time.Duration(cfg.LLM.AttemptTimeoutMS)*time.Millisecond)
	if err != nil {
		return err
	}
	box, err := reasoning.NewToolbox(reasoning.Tool{
		Name: "noop", Desc: "无需查资料；args 忽略，直接返回空",
		Run: func(context.Context, string) (string, error) { return "无可用资料", nil },
	})
	if err != nil {
		return err
	}
	llm := &reflectLLM{client: client, model: model, maxTokens: cfg.LLM.MaxReplyTokens, temp: cfg.LLM.Temperature}
	agent, err := reasoning.New(llm, box, reasoning.Options{MaxSteps: 2, Timeout: 20 * time.Second, ReflectBudget: 1}, zap.NewNop())
	if err != nil {
		return err
	}
	if err := os.Mkdir(*output, 0o700); err != nil {
		return fmt.Errorf("必须使用新的输出目录：%w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	report := reflectReport{Cases: len(cases)}
	var latencySum, tokenSum float64
	var changedCount, judged int
	runsPath := filepath.Join(*output, "reflection-runs.jsonl")
	runsFile, err := os.OpenFile(runsPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer runsFile.Close()
	enc := json.NewEncoder(runsFile)
	for i, c := range cases {
		if err := runCtx.Err(); err != nil {
			return err
		}
		run, err := reflectOne(runCtx, agent, pack, llm, client, model, cfg, c)
		if err != nil {
			return fmt.Errorf("用例 %s 失败：%w", c.ID, err)
		}
		if err := enc.Encode(run); err != nil {
			return err
		}
		report.Evaluated++
		latencySum += float64(run.OnMS - run.OffMS)
		tokenSum += float64(run.OnIn - run.OffIn)
		switch run.Verdict {
		case "improved":
			report.Improved++
			judged++
		case "harmed":
			report.Harmed++
			judged++
		case "neutral":
			report.Neutral++
			judged++
		}
		if run.Changed {
			changedCount++
		}
		fmt.Fprintf(stderr, "[%d/%d] %s changed=%v verdict=%s\n", i+1, len(cases), c.ID, run.Changed, run.Verdict)
	}
	if report.Evaluated > 0 {
		report.ChangedRate = float64(changedCount) / float64(report.Evaluated)
		report.SkipRate = 1 - report.ChangedRate
		report.AvgLatencyDelta = latencySum / float64(report.Evaluated)
		report.AvgTokenDelta = tokenSum / float64(report.Evaluated)
	}
	if judged > 0 {
		report.ImproveRate = float64(report.Improved) / float64(judged)
		report.HarmRate = float64(report.Harmed) / float64(judged)
	}
	report.JudgePending = judged == 0
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*output, "report.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	out, err := json.Marshal(report)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(out))
	if report.HarmRate > 0.05 {
		return errors.New("反思伤害率超过5%，建议关闭评论/私信反思")
	}
	return nil
}

func reflectOne(ctx context.Context, agent *reasoning.Agent, pack persona.Pack, llm *reflectLLM, client provider.Provider, model string, cfg config.Config, c personaeval.ReflectionCase) (reflectRun, error) {
	run := reflectRun{CaseID: c.ID}
	channel := event.Channel(c.Channel)
	// DM 必须声明 private：memory.ValidateScope 会拒绝公开的私信范围。
	private := channel == event.ChDM
	scope := memory.Scope{Channel: channel, Session: "reflect-" + c.ID, Private: private}
	ev := event.Event{
		ID: string(channel) + ":reflect:" + c.ID, Channel: channel, Dir: event.DirIn,
		Session: scope.Session, FromID: "eval-user", FromName: "回归用户",
		Text: c.Input, Private: private, At: time.Now().UTC(),
	}
	task := brain.Task{ID: "reflect:" + c.ID, EventID: ev.ID, Channel: channel, Session: scope.Session}
	input := brain.ContextInput{Event: ev, Persona: pack, Memory: memory.Context{Scope: scope}, Shared: memory.SharedState{Scope: scope}}
	prepared, err := brain.Prepare(ctx, task, input, brain.Budget{MaxPromptTokens: cfg.LLM.MaxPromptTokens})
	if err != nil {
		return run, err
	}
	reactIn := reasoning.Input{
		System: prepared.Messages[0].Content, Context: prepared.Context, Question: prepared.Question,
		Label: "弹幕", PromptBudget: cfg.LLM.MaxPromptTokens - 512, ThinkDeep: channel == event.ChLive,
	}
	off, err := runReflectOnce(ctx, agent, llm, reactIn, false)
	if err != nil {
		return run, err
	}
	on, err := runReflectOnce(ctx, agent, llm, reactIn, true)
	if err != nil {
		return run, err
	}
	run.Draft, run.Reflected = off.text, on.text
	run.Reflect = on.called
	run.Changed = on.called && strings.TrimSpace(on.text) != "" && on.text != off.text
	run.Skipped = on.skipped
	run.OffMS, run.OnMS = off.ms, on.ms
	run.OffIn, run.OnIn = off.inTokens, on.inTokens
	if !run.Changed {
		run.Verdict = "neutral"
		return run, nil
	}
	verdict, err := judgeReflection(ctx, client, model, c, off.text, on.text)
	if err != nil {
		return run, err
	}
	run.Verdict = verdict
	return run, nil
}

type oneShot struct {
	text     string
	called   bool
	changed  bool
	skipped  string
	ms       int64
	inTokens int
}

func runReflectOnce(ctx context.Context, agent *reasoning.Agent, llm *reflectLLM, in reasoning.Input, reflect bool) (oneShot, error) {
	_, inBefore, _ := llm.snapshot()
	in.Reflect = reflect
	started := time.Now()
	result, err := agent.Run(ctx, in)
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		return oneShot{}, err
	}
	_, inAfter, _ := llm.snapshot()
	return oneShot{
		text: result.Text, called: result.ReflectCalled, changed: result.ReflectChanged,
		skipped: result.ReflectSkippedReason, ms: elapsed, inTokens: inAfter - inBefore,
	}, nil
}

func judgeReflection(ctx context.Context, client provider.Provider, model string, c personaeval.ReflectionCase, draft, reflected string) (string, error) {
	system := `你是严格的回复质检员。比较同一输入下的两版回复，判断「反思后」是否更好。
只输出一个词：improved（更好/修正了问题）、harmed（更差/引入新问题）、neutral（差不多）。
判据：是否回答了问题、是否编造、是否越界泄露、语气是否自然、长度是否合适。`
	user := fmt.Sprintf("输入：%s\n已知问题：%s\n\n【反思前】\n%s\n\n【反思后】\n%s\n\n只输出一个词。", c.Input, c.Problem, draft, reflected)
	resp, err := client.Chat(ctx, provider.Request{
		Model: model, Messages: []provider.Message{{Role: "system", Content: system}, {Role: "user", Content: user}},
		MaxTokens: 8, Temperature: 0.0,
	})
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(resp.Text)) {
	case "improved":
		return "improved", nil
	case "harmed":
		return "harmed", nil
	case "neutral":
		return "neutral", nil
	default:
		return "neutral", nil
	}
}
