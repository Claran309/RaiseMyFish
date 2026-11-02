package reasoning

// 本文件实现 ReAct 循环：模型每步返回 {thought, action, args}，
// action 是工具名就执行并回填结果，action=final 就收尾。
//
// 预算纪律（派发单硬要求）：
//   - MaxSteps 默认 3，超了立刻降级；
//   - 总超时默认 8 秒，覆盖整个循环 + 降级生成；
//   - 解析失败/步数耗尽/超时/模型报错 → 用已有观察降级生成，绝不让调用方等成 30 秒。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// 降级原因（日志与测试都用这几个常量，避免拼写漂移）。
const (
	DegradeSteps    = "steps"
	DegradeTimeout  = "timeout"
	DegradeParse    = "parse"
	DegradeLLMError = "llm_error"
	DegradeEmpty    = "empty"
)

// LLM 是 ReAct 需要的单次文本补全；由 provider 适配，测试用 mock。
type LLM interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// Options 是 ReAct 预算。
type Options struct {
	MaxSteps int
	Timeout  time.Duration
}

// DefaultOptions 与派发单一致：3 步 / 8 秒。
func DefaultOptions() Options {
	return Options{MaxSteps: 3, Timeout: 8 * time.Second}
}

// Input 是一次 ReAct 的输入。
type Input struct {
	// System 是人设与协议（调用方传人格摘要）。
	System string
	// Question 是这次要回答的事件正文。
	Question string
}

// Step 是模型返回的一步决策。
type Step struct {
	Thought string `json:"thought"`
	Action  string `json:"action"`
	Args    string `json:"args"`
	// Final 是 action=final 时的回复正文（也兼容把回复塞进 args 的写法）。
	Final string `json:"final"`
}

// Result 是一次 ReAct 的结果。
type Result struct {
	// Text 是最终回复；降级且来不及生成时为空（调用方回落单步生成）。
	Text string
	// Steps 是实际消耗的模型步数（含降级那次）。
	Steps int
	// Tools 是按顺序调用过的工具名。
	Tools []string
	// Degraded 表示走了降级路径。
	Degraded bool
	// Reason 是降级原因，取自 Degrade* 常量。
	Reason string
}

// Agent 是 ReAct 执行器。
type Agent struct {
	llm   LLM
	tools *Toolbox
	opts  Options
	log   *zap.Logger
}

// New 装配 ReAct 执行器。
func New(llm LLM, tools *Toolbox, o Options, log *zap.Logger) (*Agent, error) {
	if llm == nil {
		return nil, errors.New("ReAct 缺少模型调用器")
	}
	if tools == nil || len(tools.Names()) == 0 {
		return nil, errors.New("ReAct 缺少可用工具")
	}
	if o.MaxSteps < 1 || o.MaxSteps > 10 {
		return nil, fmt.Errorf("ReAct 步数需为1至10：%d", o.MaxSteps)
	}
	if o.Timeout <= 0 {
		return nil, errors.New("ReAct 超时必须为正")
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Agent{llm: llm, tools: tools, opts: o, log: log}, nil
}

// Run 执行一次 ReAct。
//
// 返回值约定：即使降级也返回 nil error——调用方（serve.go）在 Text 为空时回落单步生成；
// 只有入参非法才返回错误。
func (a *Agent) Run(ctx context.Context, in Input) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("ReAct ctx 不能为空")
	}
	if strings.TrimSpace(in.Question) == "" {
		return Result{}, errors.New("ReAct 缺少问题正文")
	}

	runCtx, cancel := context.WithTimeout(ctx, a.opts.Timeout)
	defer cancel()

	var (
		observations []string
		used         []string
		steps        int
	)

	for step := 1; step <= a.opts.MaxSteps; step++ {
		if runCtx.Err() != nil {
			return a.degrade(runCtx, in, observations, used, steps, DegradeTimeout)
		}
		out, err := a.llm.Complete(runCtx, a.systemPrompt(in), a.userPrompt(in, observations))
		steps++
		if err != nil {
			if runCtx.Err() != nil {
				return a.degrade(runCtx, in, observations, used, steps, DegradeTimeout)
			}
			return a.degrade(runCtx, in, observations, used, steps, DegradeLLMError)
		}

		decision, perr := ParseStep(out)
		if perr != nil {
			return a.degrade(runCtx, in, observations, used, steps, DegradeParse)
		}
		if decision.Action == "final" {
			text := strings.TrimSpace(decision.Final)
			if text == "" {
				text = strings.TrimSpace(decision.Args)
			}
			if text == "" {
				// 说 final 却没给正文：当成解析失败降级，别回空串
				return a.degrade(runCtx, in, observations, used, steps, DegradeEmpty)
			}
			return Result{Text: text, Steps: steps, Tools: used}, nil
		}

		// action 是工具名：执行并把结果回填进下一轮
		out2, toolErr := a.tools.Run(runCtx, decision.Action, decision.Args)
		if toolErr != nil {
			// 工具失败不终止循环：把失败也当成一条观察，让模型自己决定换招
			out2 = "工具失败：" + toolErr.Error()
		}
		used = append(used, decision.Action)
		observations = append(observations, fmt.Sprintf("%s(%s) → %s", decision.Action, clip(decision.Args, 60), clip(out2, maxObservationRunes)))
	}

	return a.degrade(runCtx, in, observations, used, steps, DegradeSteps)
}

// degrade 用已有观察再要一次回复；预算已耗尽则直接返回空，交给调用方回落。
func (a *Agent) degrade(ctx context.Context, in Input, observations, used []string, steps int, reason string) (Result, error) {
	result := Result{Steps: steps, Tools: used, Degraded: true, Reason: reason}

	// 超时已经把预算花光：不再调用模型，避免把 8 秒拖成 16 秒
	if ctx.Err() != nil {
		return result, nil //nolint:nilerr // 超时是预期内的降级路径，不是要上抛的错误
	}

	system := a.systemPrompt(in) + "\n现在不要再调用工具，直接给出最终回复。"
	out, err := a.llm.Complete(ctx, system, a.userPrompt(in, observations))
	result.Steps = steps + 1
	if err != nil {
		// 降级本身失败不向上抛：调用方会回落单步生成，这里报错只会多一条噪音
		return result, nil //nolint:nilerr // 有意吞掉：降级失败交给调用方兜底
	}
	// 降级那次模型仍可能回 JSON，尽量把正文捞出来
	if decision, perr := ParseStep(out); perr == nil {
		if text := firstNonEmpty(decision.Final, decision.Args, decision.Thought); text != "" {
			result.Text = text
			return result, nil
		}
	}
	result.Text = strings.TrimSpace(out)
	return result, nil
}

// systemPrompt 拼协议说明。
func (a *Agent) systemPrompt(in Input) string {
	var sb strings.Builder
	if s := strings.TrimSpace(in.System); s != "" {
		sb.WriteString(s)
		sb.WriteString("\n\n")
	}
	sb.WriteString("你在回答一条直播弹幕。需要事实就先查，查到够了再回答。\n")
	sb.WriteString("可用工具：\n")
	sb.WriteString(a.tools.Descriptions())
	sb.WriteString("每一步只输出一行 JSON，不要多余文字：\n")
	sb.WriteString(`{"thought":"你在想什么","action":"工具名或 final","args":"工具参数；action=final 时这里或 final 字段放回复正文"}`)
	return sb.String()
}

// userPrompt 拼问题与已获得的观察。
func (a *Agent) userPrompt(in Input, observations []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "弹幕：%s\n", clip(in.Question, 300))
	if len(observations) == 0 {
		sb.WriteString("（还没有查证）")
		return sb.String()
	}
	sb.WriteString("已经查到：\n")
	for _, line := range observations {
		fmt.Fprintf(&sb, "- %s\n", line)
	}
	return sb.String()
}

// ParseStep 解析模型输出的一行 JSON，容忍代码块包裹。
func ParseStep(raw string) (Step, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return Step{}, errors.New("模型输出为空")
	}
	// 去掉 ```json ... ``` 包裹
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```")
		text = strings.TrimPrefix(text, "json")
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	// 只取第一个 { 到最后一个 }，容忍前后废话
	if start := strings.Index(text, "{"); start >= 0 {
		if end := strings.LastIndex(text, "}"); end > start {
			text = text[start : end+1]
		}
	}
	var step Step
	if err := json.Unmarshal([]byte(text), &step); err != nil {
		return Step{}, err
	}
	if strings.TrimSpace(step.Action) == "" {
		return Step{}, errors.New("缺少 action")
	}
	return step, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
