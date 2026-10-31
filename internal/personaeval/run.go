package personaeval

import (
	"RaiseMyFish/internal/event"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Input 刻意不含expect/forbid/kind，不能把评分答案传给模型。
type Input struct {
	ID      string
	Channel event.Channel
	Private bool
	Text    string
}

// Attempt 是一次真实Provider调用的审计投影，不包含完整prompt/请求头。
type Attempt struct {
	Number         int       `json:"number"`
	At             time.Time `json:"at"`
	Milliseconds   int       `json:"ms"`
	RequestedModel string    `json:"requested_model"`
	ServedModel    string    `json:"served_model,omitempty"`
	SystemSHA256   string    `json:"system_sha256"`
	UserSHA256     string    `json:"user_sha256"`
	PromptEstimate int       `json:"prompt_estimate"`
	RawOutput      string    `json:"raw_output"`
	InputTokens    *int      `json:"in_tokens"`
	OutputTokens   *int      `json:"out_tokens"`
	CostUSD        *float64  `json:"cost_usd"`
	LedgerStored   bool      `json:"ledger_stored"`
	ErrorKind      string    `json:"error_kind,omitempty"`
	HTTPStatus     int       `json:"http_status,omitempty"`
}

// Generation 同时保留未截断原文和Brain投递文本。
type Generation struct {
	RawOutput       string
	DeliveredOutput string
	ReplyMeta       json.RawMessage
	Attempts        []Attempt
	Fatal           bool
}

// Generator 只能看到合成输入，真实CLI只构造Provider/Brain适配器。
type Generator interface {
	Name() string
	Generate(context.Context, Input) (Generation, error)
}

// Configuration 是不含凭据的生效参数快照。
type Configuration struct {
	Route               string   `json:"route"`
	EndpointHost        string   `json:"endpoint_host"`
	MaxPromptTokens     int      `json:"max_prompt_tokens"`
	MaxReplyTokens      int      `json:"max_reply_tokens"`
	AttemptTimeoutMS    int      `json:"attempt_timeout_ms"`
	TotalTimeoutMS      int      `json:"total_timeout_ms"`
	BackoffMS           int      `json:"backoff_ms"`
	Temperature         float64  `json:"temperature"`
	InputUSDPerMillion  *float64 `json:"input_usd_per_million"`
	OutputUSDPerMillion *float64 `json:"output_usd_per_million"`
}

// RunOptions 不将Secrets序列化到任何产物。
type RunOptions struct {
	RunID         string
	OutputDir     string
	SourceKind    string
	CodeCommit    string
	CodeFiles     []FileSnapshot
	Configuration Configuration
	Secrets       []string
	Progress      io.Writer
}

// CaseResult 保留原始和投递两组独立评分。
type CaseResult struct {
	Case              Case            `json:"case"`
	StartedAt         time.Time       `json:"started_at"`
	FinishedAt        time.Time       `json:"finished_at"`
	RawOutput         string          `json:"raw_output"`
	RawSHA256         string          `json:"raw_sha256"`
	DeliveredOutput   string          `json:"delivered_output"`
	DeliveredSHA256   string          `json:"delivered_sha256"`
	Redacted          bool            `json:"redacted"`
	RawFailures       []string        `json:"raw_failures"`
	DeliveredFailures []string        `json:"delivered_failures"`
	RawPass           bool            `json:"raw_pass"`
	DeliveredPass     bool            `json:"delivered_pass"`
	Error             string          `json:"error,omitempty"`
	ReplyMeta         json.RawMessage `json:"reply_meta,omitempty"`
	Attempts          []Attempt       `json:"attempts"`
}

// Report 不把失败或未完成用例从分母删除。
type Report struct {
	RunID             string         `json:"run_id"`
	SourceKind        string         `json:"source_kind"`
	BaselineUsed      bool           `json:"baseline_used"`
	ProviderModel     string         `json:"provider_model"`
	PackName          string         `json:"pack_name"`
	PackVersion       string         `json:"pack_version"`
	CodeCommit        string         `json:"code_commit"`
	StartedAt         time.Time      `json:"started_at"`
	FinishedAt        time.Time      `json:"finished_at"`
	Configuration     Configuration  `json:"configuration"`
	Files             []FileSnapshot `json:"fixture_files"`
	CodeFiles         []FileSnapshot `json:"code_files"`
	RulesSHA256       string         `json:"rules_sha256"`
	Total             int            `json:"total"`
	Completed         int            `json:"completed"`
	RawPassed         int            `json:"raw_passed"`
	DeliveredPassed   int            `json:"delivered_passed"`
	RawPassRate       float64        `json:"raw_pass_rate"`
	DeliveredPassRate float64        `json:"delivered_pass_rate"`
	Complete          bool           `json:"complete"`
	Fatal             bool           `json:"fatal"`
	Interrupted       bool           `json:"interrupted"`
	Reached90         bool           `json:"reached_90"`
	StopReason        string         `json:"stop_reason,omitempty"`
	HTTPAttempts      int            `json:"provider_attempts"`
	InputTokens       *int           `json:"in_tokens"`
	OutputTokens      *int           `json:"out_tokens"`
	CostUSD           *float64       `json:"cost_usd"`
	Results           []CaseResult   `json:"results"`
}

// Redactor 只保存非空实际敏感值，永不输出它们。
type Redactor struct{ secrets []string }

// NewRedactor 在进程内接收已配置密钥等敏感值。
func NewRedactor(values []string) *Redactor {
	r := &Redactor{}
	for _, s := range values {
		if s != "" {
			r.secrets = append(r.secrets, s)
		}
	}
	return r
}

// Contains 用于把凭据出现升级成一票否决。
func (r *Redactor) Contains(s string) bool {
	for _, secret := range r.secrets {
		if strings.Contains(s, secret) {
			return true
		}
	}
	return false
}

// Text 对报告中的敏感值脱敏。
func (r *Redactor) Text(s string) string {
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, "[REDACTED]")
	}
	return s
}

func writeJSON(path string, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("编码评估产物：%w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		return fmt.Errorf("写评估产物：%w", err)
	}
	return nil
}
func increment(total **int, value *int) {
	if *total == nil {
		return
	}
	if value == nil {
		*total = nil
		return
	}
	**total += *value
}

// Run 先保存冻结规则/快照，逐条同步写检查点，最后给出真实分母的报告。
func Run(ctx context.Context, fixture Fixture, scorer *Scorer, generator Generator, o RunOptions) (report Report, result error) {
	if ctx == nil || scorer == nil || generator == nil || o.RunID == "" || (o.SourceKind != "model_provider" && o.SourceKind != "test") || strings.TrimSpace(generator.Name()) == "" || strings.Contains(strings.ToLower(generator.Name()), "baseline") {
		return report, errors.New("评估运行参数或生成器无效")
	}
	if err := scorer.ValidateCases(fixture.Cases); err != nil {
		return report, err
	}
	if Digest(fixture.Rules) != Digest(scorer.Snapshot()) {
		return report, errors.New("评分规则与冻结快照不符")
	}
	if err := fixture.VerifySources(); err != nil {
		return report, err
	}
	if err := os.MkdirAll(filepath.Dir(o.OutputDir), 0700); err != nil {
		return report, err
	}
	if err := os.Mkdir(o.OutputDir, 0700); err != nil {
		return report, fmt.Errorf("必须使用新的评估目录：%w", err)
	}
	redactor := NewRedactor(o.Secrets)
	for _, file := range fixture.Files {
		if file.Name == "cases.jsonl" {
			if redactor.Contains(string(file.Data)) {
				return report, errors.New("用例快照包含已配置凭据，拒绝落盘")
			}
			if err := os.WriteFile(filepath.Join(o.OutputDir, "cases.jsonl"), file.Data, 0600); err != nil {
				return report, err
			}
		}
	}
	if err := os.WriteFile(filepath.Join(o.OutputDir, "rules.json"), fixture.Rules, 0600); err != nil {
		return report, err
	}
	in, out, cost := 0, 0, 0.
	report = Report{RunID: o.RunID, SourceKind: o.SourceKind, ProviderModel: generator.Name(), PackName: fixture.Pack.Meta.Name, PackVersion: fixture.Pack.Meta.Version, CodeCommit: o.CodeCommit, StartedAt: time.Now().UTC(), Configuration: o.Configuration, Files: fixture.Files, CodeFiles: o.CodeFiles, RulesSHA256: Digest(fixture.Rules), Total: len(fixture.Cases), InputTokens: &in, OutputTokens: &out, CostUSD: &cost}
	if err := writeJSON(filepath.Join(o.OutputDir, "manifest.json"), report); err != nil {
		return report, err
	}
	checkpoint, err := os.OpenFile(filepath.Join(o.OutputDir, "results.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return report, err
	}
	defer func() { result = errors.Join(result, checkpoint.Close()) }()
	for _, c := range fixture.Cases {
		if err := ctx.Err(); err != nil {
			report.Interrupted = true
			report.StopReason = "评估被取消或超时"
			break
		}
		row := CaseResult{Case: c, StartedAt: time.Now().UTC()}
		generation, err := generator.Generate(ctx, Input{ID: c.ID, Channel: c.Channel, Private: c.Private, Text: c.Input})
		row.FinishedAt = time.Now().UTC()
		row.RawSHA256 = Digest([]byte(generation.RawOutput))
		row.DeliveredSHA256 = Digest([]byte(generation.DeliveredOutput))
		row.RawFailures = scorer.Evaluate(c, generation.RawOutput)
		row.DeliveredFailures = scorer.Evaluate(c, generation.DeliveredOutput)
		if err != nil {
			row.Error = redactor.Text(err.Error())
			row.RawFailures = append(row.RawFailures, "generation_error")
			row.DeliveredFailures = append(row.DeliveredFailures, "generation_error")
		}
		if redactor.Contains(generation.RawOutput) || redactor.Contains(generation.DeliveredOutput) {
			row.Redacted = true
			row.RawFailures = append(row.RawFailures, "configured_credential_detected")
			row.DeliveredFailures = append(row.DeliveredFailures, "configured_credential_detected")
		}
		row.RawOutput = redactor.Text(generation.RawOutput)
		row.DeliveredOutput = redactor.Text(generation.DeliveredOutput)
		row.ReplyMeta = generation.ReplyMeta
		row.Attempts = generation.Attempts
		for i := range row.Attempts {
			a := &row.Attempts[i]
			a.RawOutput = redactor.Text(a.RawOutput)
			a.ServedModel = redactor.Text(a.ServedModel)
			report.HTTPAttempts++
			increment(&report.InputTokens, a.InputTokens)
			increment(&report.OutputTokens, a.OutputTokens)
			if report.CostUSD != nil {
				if a.CostUSD == nil {
					report.CostUSD = nil
				} else {
					*report.CostUSD += *a.CostUSD
				}
			}
		}
		row.RawPass = len(row.RawFailures) == 0
		row.DeliveredPass = len(row.DeliveredFailures) == 0
		if row.RawPass {
			report.RawPassed++
		}
		if row.DeliveredPass {
			report.DeliveredPassed++
		}
		report.Completed++
		report.Results = append(report.Results, row)
		line, marshalErr := json.Marshal(row)
		if marshalErr != nil {
			return report, marshalErr
		}
		if _, err := checkpoint.Write(append(line, '\n')); err != nil {
			return report, err
		}
		if err := checkpoint.Sync(); err != nil {
			return report, err
		}
		if o.Progress != nil {
			if _, err := fmt.Fprintf(o.Progress, "[%d/%d] %s raw=%t delivered=%t attempts=%d\n", report.Completed, report.Total, c.ID, row.RawPass, row.DeliveredPass, len(row.Attempts)); err != nil {
				return report, err
			}
		}
		if generation.Fatal {
			report.Fatal = true
			report.StopReason = "鉴权或记账等致命错误，停止进一步费用"
			break
		}
		if ctx.Err() != nil {
			report.Interrupted = true
			report.StopReason = "评估被取消或总时限到期"
			break
		}
	}
	report.FinishedAt = time.Now().UTC()
	report.Complete = report.Completed == report.Total && !report.Fatal && !report.Interrupted
	report.RawPassRate = float64(report.RawPassed) / float64(report.Total)
	report.DeliveredPassRate = float64(report.DeliveredPassed) / float64(report.Total)
	report.Reached90 = report.Complete && report.SourceKind == "model_provider" && report.RawPassed*10 >= report.Total*9
	if err := writeJSON(filepath.Join(o.OutputDir, "report.json"), report); err != nil {
		return report, err
	}
	if err := os.WriteFile(filepath.Join(o.OutputDir, "report.md"), []byte(markdown(report)), 0600); err != nil {
		return report, err
	}
	return report, nil
}

func fenced(s string) string {
	fence := "```"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	return fence + "text\n" + s + "\n" + fence + "\n"
}
func markdown(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 人格真实模型评估 · %s\n\nSource: %s；baseline_used=false。\n\n模型路由：%s；人格：%s %s；代码基线：%s。\n\n完成 %d/%d；主分数（原始输出）%d/%d = %.1f%%；投递文本 %d/%d = %.1f%%。\n\n完整运行：%t；原始输出达到90%%：%t。未知token/费用以report.json的null为准，词法评分不等于完整语义判断。\n\n", r.RunID, r.SourceKind, r.ProviderModel, r.PackName, r.PackVersion, r.CodeCommit, r.Completed, r.Total, r.RawPassed, r.Total, r.RawPassRate*100, r.DeliveredPassed, r.Total, r.DeliveredPassRate*100, r.Complete, r.Reached90)
	if r.StopReason != "" {
		fmt.Fprintf(&b, "停止原因：%s\n\n", r.StopReason)
	}
	for _, row := range r.Results {
		fmt.Fprintf(&b, "## %s · raw=%t delivered=%t\n\n输入：\n\n%s\n原始模型输出：\n\n%s\nBrain投递文本：\n\n%s\n失败标签：raw=%v；delivered=%v；error=%s\n\n", row.Case.ID, row.RawPass, row.DeliveredPass, fenced(row.Case.Input), fenced(row.RawOutput), fenced(row.DeliveredOutput), row.RawFailures, row.DeliveredFailures, row.Error)
	}
	return b.String()
}
