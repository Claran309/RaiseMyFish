package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/personaeval"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// evalProvider 只记录哈希/模型输出/用量，不记录prompt或HTTP头。
type evalProvider struct {
	inner    provider.Provider
	mu       sync.Mutex
	attempts []personaeval.Attempt
}

func (p *evalProvider) Name() string { return p.inner.Name() }
func (p *evalProvider) Chat(ctx context.Context, r provider.Request) (provider.Response, error) {
	started := time.Now()
	response, err := p.inner.Chat(ctx, r)
	a := personaeval.Attempt{At: started.UTC(), Milliseconds: int(time.Since(started).Milliseconds()), RequestedModel: r.Model, ServedModel: response.Model, RawOutput: response.Text, InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens}
	if len(r.Messages) == 2 {
		a.SystemSHA256 = personaeval.Digest([]byte(r.Messages[0].Content))
		a.UserSHA256 = personaeval.Digest([]byte(r.Messages[1].Content))
		a.PromptEstimate = len(r.Messages[0].Content) + len(r.Messages[1].Content) + 64
	}
	if err != nil {
		a.ErrorKind = "provider"
		var typed *provider.Error
		if errors.As(err, &typed) {
			a.ErrorKind = string(typed.Kind)
			a.HTTPStatus = typed.StatusCode
		}
	}
	p.mu.Lock()
	a.Number = len(p.attempts) + 1
	p.attempts = append(p.attempts, a)
	p.mu.Unlock()
	return response, err
}

type evalUsage struct {
	inner store.UsageRepo
	mu    sync.Mutex
	rows  []store.Usage
}

func (u *evalUsage) RecordUsage(ctx context.Context, row store.Usage) error {
	if err := u.inner.RecordUsage(ctx, row); err != nil {
		return err
	}
	u.mu.Lock()
	u.rows = append(u.rows, row)
	u.mu.Unlock()
	return nil
}

type evalGenerator struct {
	runtime  brain.Brain
	pack     persona.Pack
	provider *evalProvider
	usage    *evalUsage
	runID    string
}

func (g *evalGenerator) Name() string { return g.runtime.Name() }
func (g *evalGenerator) Generate(ctx context.Context, in personaeval.Input) (personaeval.Generation, error) {
	g.provider.mu.Lock()
	g.provider.attempts = nil
	g.provider.mu.Unlock()
	g.usage.mu.Lock()
	g.usage.rows = nil
	g.usage.mu.Unlock()
	private := in.Channel == event.ChDM || (in.Channel == event.ChQQ && in.Private)
	scope := memory.Scope{Channel: in.Channel, Session: "eval-" + g.runID + "-" + in.ID, Private: private}
	id := string(in.Channel) + ":eval-" + g.runID + ":" + in.ID
	task := brain.Task{ID: "eval:" + g.runID + ":" + in.ID, EventID: id, Channel: in.Channel, Session: scope.Session}
	input := brain.ContextInput{Event: event.Event{ID: id, Channel: in.Channel, Session: scope.Session, Dir: event.DirIn, Private: private, Text: in.Text, FromID: "eval-user", FromName: "回归用户", At: time.Now().UTC()}, Persona: g.pack, Memory: memory.Context{Scope: scope}, Shared: memory.SharedState{Scope: scope}}
	reply, err := g.runtime.Reply(ctx, task, input)
	g.provider.mu.Lock()
	attempts := append([]personaeval.Attempt(nil), g.provider.attempts...)
	g.provider.mu.Unlock()
	g.usage.mu.Lock()
	rows := append([]store.Usage(nil), g.usage.rows...)
	g.usage.mu.Unlock()
	for i := range attempts {
		if i < len(rows) {
			attempts[i].LedgerStored = true
			attempts[i].CostUSD = rows[i].CostUSD
		}
	}
	result := personaeval.Generation{DeliveredOutput: reply.Text, ReplyMeta: reply.Meta, Attempts: attempts}
	if len(attempts) > 0 {
		result.RawOutput = attempts[len(attempts)-1].RawOutput
	}
	var pErr *provider.Error
	var bErr *brain.Error
	if errors.As(err, &pErr) && (pErr.StatusCode == 401 || pErr.StatusCode == 402 || pErr.StatusCode == 403) {
		result.Fatal = true
	}
	if errors.As(err, &bErr) && bErr.Kind == brain.KindUsage {
		result.Fatal = true
	}
	return result, err
}

func runPersonaEval(ctx context.Context, configPath, envPath string, args []string, stdout, stderr io.Writer) error {
	return personaEval(ctx, configPath, envPath, args, stdout, stderr, "model_provider")
}

func personaEval(ctx context.Context, configPath, envPath string, args []string, stdout, stderr io.Writer, sourceKind string) (result error) {
	fs := flag.NewFlagSet("persona eval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	packFlag := fs.String("pack", "", "人格目录或personas下包名")
	casesFlag := fs.String("cases", "", "JSONL用例，默认包内eval/cases.jsonl")
	route := fs.String("route", "default", "模型配置路由")
	backend := fs.String("brain", "openai", "仅支持真实openai兼容Provider；无baseline模式")
	output := fs.String("output", "", "新建证据目录，不能覆盖")
	timeout := fs.Duration("timeout", 20*time.Minute, "整轮时间上限")
	rulesVersion := fs.String("rules-version", "persona-lexical-v1", "主评分规则版本")
	compareVersion := fs.String("compare-rules", "", "同一新输出的离线对照规则版本")
	if err := fs.Parse(args); err != nil {
		return errors.New("人格评估参数无效；不支持baseline回放")
	}
	if fs.NArg() != 0 || *backend != "openai" || *timeout <= 0 || *timeout > time.Hour {
		return errors.New("人格评估参数或后端无效")
	}
	cfg, err := config.Load(configPath, envPath)
	if err != nil {
		return err
	}
	if !cfg.LLM.Enabled {
		return errors.New("真实人格评估需要在本地配置明确启用llm")
	}
	if *packFlag != "" {
		cfg.LLM.PersonaDir = *packFlag
		if !strings.ContainsAny(*packFlag, `/\`) {
			cfg.LLM.PersonaDir = filepath.Join("personas", *packFlag)
		}
	}
	casesPath := *casesFlag
	if casesPath == "" {
		casesPath = filepath.Join(cfg.LLM.PersonaDir, "eval", "cases.jsonl")
	}
	scorer, err := personaeval.NewScorerVersion(*rulesVersion)
	if err != nil {
		return err
	}
	fixture, err := personaeval.Prepare(ctx, cfg.LLM.PersonaDir, casesPath, scorer)
	if err != nil {
		return err
	}
	if len(fixture.Cases) != 35 {
		return errors.New("本轮真实评估要求既有35条用例，拒绝子集美化")
	}
	var comparison *personaeval.Scorer
	if *compareVersion != "" {
		comparison, err = personaeval.NewScorerVersion(*compareVersion)
		if err != nil {
			return err
		}
		if err := comparison.ValidateCases(fixture.Cases); err != nil {
			return err
		}
	}
	name, p, model, err := cfg.LLM.Resolve(*route)
	if err != nil {
		return err
	}
	resources, err := infra.Setup(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, resources.Close(ctx)) }()
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = database.Ready(check, resources.DB)
	cancel()
	if err != nil {
		return err
	}
	repo, err := database.NewUsageRepo(resources.DB)
	if err != nil {
		return err
	}
	client, err := provider.NewOpenAI(name, p.BaseURL, p.APIKey, &http.Client{}, time.Duration(cfg.LLM.AttemptTimeoutMS)*time.Millisecond)
	if err != nil {
		return err
	}
	audit := &evalProvider{inner: client}
	usage := &evalUsage{inner: repo}
	o := brain.DefaultOptions()
	o.Model = model
	o.MaxPromptTokens = cfg.LLM.MaxPromptTokens
	o.MaxReplyTokens = cfg.LLM.MaxReplyTokens
	o.AttemptTimeout = time.Duration(cfg.LLM.AttemptTimeoutMS) * time.Millisecond
	o.TotalTimeout = time.Duration(cfg.LLM.TotalTimeoutMS) * time.Millisecond
	o.Backoff = time.Duration(cfg.LLM.BackoffMS) * time.Millisecond
	o.MaxConcurrent = 1
	o.Temperature = cfg.LLM.Temperature
	if price, ok := p.Prices[model]; ok {
		o.InputUSDPerMillion = price.InputUSDPerMillion
		o.OutputUSDPerMillion = price.OutputUSDPerMillion
	}
	runtime, err := brain.New(audit, usage, o, zap.NewNop())
	if err != nil {
		return err
	}
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	runID := time.Now().UTC().Format("20060102T150405Z") + fmt.Sprintf("-%x", random)
	outputDir := *output
	if outputDir == "" {
		outputDir = filepath.Join("plans", "reports", "persona-eval", runID)
	}
	outputDir, err = filepath.Abs(outputDir)
	if err != nil {
		return err
	}
	root, err := evalRoot()
	if err != nil {
		return err
	}
	codeFiles, err := evalCodeFiles(root)
	if err != nil {
		return err
	}
	gitCtx, gitStop := context.WithTimeout(ctx, 5*time.Second)
	gitCmd := exec.CommandContext(gitCtx, "git", "rev-parse", "HEAD")
	gitCmd.Dir = root
	commit, err := gitCmd.Output()
	gitStop()
	if err != nil {
		return errors.New("读取评估代码基线失败")
	}
	baseCommit := strings.TrimSpace(string(commit))
	if len(baseCommit) != 40 {
		return errors.New("评估代码基线格式无效")
	}
	endpoint, _ := url.Parse(p.BaseURL)
	settings := personaeval.Configuration{Route: *route, EndpointHost: endpoint.Hostname(), MaxPromptTokens: o.MaxPromptTokens, MaxReplyTokens: o.MaxReplyTokens, AttemptTimeoutMS: cfg.LLM.AttemptTimeoutMS, TotalTimeoutMS: cfg.LLM.TotalTimeoutMS, BackoffMS: cfg.LLM.BackoffMS, Temperature: o.Temperature, InputUSDPerMillion: o.InputUSDPerMillion, OutputUSDPerMillion: o.OutputUSDPerMillion}
	gen := &evalGenerator{runtime: runtime, pack: fixture.Pack, provider: audit, usage: usage, runID: runID}
	runCtx, stop := context.WithTimeout(ctx, *timeout)
	defer stop()
	runOptions := personaeval.RunOptions{RunID: runID, OutputDir: outputDir, SourceKind: sourceKind, CodeCommit: baseCommit, CodeFiles: codeFiles, Configuration: settings, Secrets: []string{p.APIKey, cfg.Database.DSN}, Progress: stderr}
	if comparison != nil {
		runOptions.ComparisonVersion = comparison.Version()
		runOptions.ComparisonRules = comparison.Snapshot()
	}
	report, err := personaeval.Run(runCtx, fixture, scorer, gen, runOptions)
	if err != nil {
		return err
	}
	if comparison != nil && report.Complete {
		comparisonDir := outputDir + "-compare-" + comparison.Version()
		other, err := personaeval.RescoreExisting(outputDir, comparisonDir, comparison)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "comparison_rules=%s raw=%d/%d (%.1f%%) delivered=%d/%d (%.1f%%) actual_model_calls=0 report=%s\n", other.RulesVersion, other.RawPassed, other.Total, other.RawPassRate*100, other.DeliveredPassed, other.Total, other.DeliveredPassRate*100, filepath.Join(comparisonDir, "report.md")); err != nil {
			return err
		}
	}
	if report.PassRateBasis == "automated_safety" {
		if _, err := fmt.Fprintf(stdout, "run=%s model=%s persona=%s@%s basis=automated_safety raw=%d/%d (%.1f%%) delivered=%d/%d (%.1f%%) unassessable=%d complete=%t automated90=%t manual=%s report=%s\n", report.RunID, report.ProviderModel, report.PackName, report.PackVersion, report.RawPassed, report.RawSafetySummary.Assessed, report.RawPassRate*100, report.DeliveredPassed, report.DeliveredSafetySummary.Assessed, report.DeliveredPassRate*100, report.RawSafetySummary.Unassessable, report.Complete, report.AutomatedReached90, report.ManualReviewStatus, filepath.Join(outputDir, "report.md")); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(stdout, "run=%s model=%s persona=%s@%s raw=%d/%d (%.1f%%) delivered=%d/%d (%.1f%%) complete=%t reached90=%t report=%s\n", report.RunID, report.ProviderModel, report.PackName, report.PackVersion, report.RawPassed, report.Total, report.RawPassRate*100, report.DeliveredPassed, report.Total, report.DeliveredPassRate*100, report.Complete, report.Reached90, filepath.Join(outputDir, "report.md")); err != nil {
		return err
	}
	if !report.Complete {
		return errors.New("真实评估未完成，不能宣称35条模型结果")
	}
	if report.ManualReviewRequired {
		return errors.New("自动安全评估已保存，须独立人工硬核查；不据风格分数或自动安全率宣称最终验收")
	}
	if !report.Reached90 {
		return errors.New("真实模型原始输出通过率未达到90%，实际结果已保存")
	}
	return nil
}

func evalRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && strings.Contains(string(data), "module RaiseMyFish") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("找不到RaiseMyFish源码根目录")
}
func evalCodeFiles(root string) (files []personaeval.FileSnapshot, result error) {
	codeRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { result = errors.Join(result, codeRoot.Close()) }()
	for _, dir := range []string{"internal/personaeval", "internal/brain", "internal/persona"} {
		if err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			file, err := codeRoot.Open(rel)
			if err != nil {
				return err
			}
			data, readErr := io.ReadAll(file)
			closeErr := file.Close()
			if err := errors.Join(readErr, closeErr); err != nil {
				return err
			}
			files = append(files, personaeval.FileSnapshot{Name: filepath.ToSlash(rel), Bytes: len(data), SHA256: personaeval.Digest(data)})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"cmd/fishctl/persona_eval.go", "cmd/fishctl/main.go", "internal/config/config.go", "internal/config/llm.go"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return nil, err
		}
		files = append(files, personaeval.FileSnapshot{Name: name, Bytes: len(data), SHA256: personaeval.Digest(data)})
	}
	return files, nil
}
