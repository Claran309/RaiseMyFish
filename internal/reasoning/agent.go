package reasoning

// 本文件实现 ReAct 循环：模型每步返回 {thought, action, args}，
// action 是工具名就执行并回填结果，action=final 就收尾。
//
// 预算纪律（派发单硬要求）：
//   - MaxSteps 默认 3，超了立刻降级；
//   - 总超时默认 8 秒，覆盖整个循环 + 降级生成；
//   - 解析失败/步数耗尽/超时/模型报错 → 用已有观察降级生成，绝不让调用方等成 30 秒。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/brain/provider"
	"go.uber.org/zap"
)

// 降级原因（日志与测试都用这几个常量，避免拼写漂移）。
const (
	DegradeSteps    = "steps"
	DegradeTimeout  = "timeout"
	DegradeParse    = "parse"
	DegradeLLMError = "llm_error"
	DegradeEmpty    = "empty"
	// DegradeBudget 表示连「当前问题 + 资料」都装不进调用预算，本轮不发模型请求。
	DegradeBudget = "budget"
)

// 证据复用上限：回落单步时只带最近几条工具结果，避免多轮查证撑爆 prompt。
const (
	maxEvidenceItems     = 6
	maxEvidenceArgsRunes = 200
	// maxObservationArgsRunes 是观察行里工具参数的最大长度（与 Run 的 clip 一致）。
	maxObservationArgsRunes = 60
	// observationHeader 是观察段固定开头；预算是按最坏情况预留的，必须计入。
	observationHeader = "已经查到：\n"
	// observationReserveLines 是 Prepare 阶段预留的整条观察数：
	// 至少保证第一次查证的结果能原样带进下一轮；更多观察由调用前裁剪兜底。
	observationReserveLines = 1
)

// appendEvidence 追加一条证据并只保留最近几条。
func appendEvidence(list []Evidence, e Evidence) []Evidence {
	list = append(list, e)
	if len(list) > maxEvidenceItems {
		list = append([]Evidence(nil), list[len(list)-maxEvidenceItems:]...)
	}
	return list
}

// 反思预算：只在步数与剩余时间都够时追加一次自检，不能把 8 秒拖长。
const (
	reflectMinRemaining  = 1500 * time.Millisecond
	maxReflectDraftRunes = 300
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
	// Context 是统一上下文快照（档案/双方对话/状态/长期设定/场景资料），
	// 与单步 Brain 使用同一份快照；进入 user 段，不作为系统指令。
	Context string
	// Question 是这次要回答的事件正文。
	Question string
	// Label 是回复对象称呼（弹幕/评论/私信）；空串等价于「弹幕」。
	Label string
	// PromptBudget 是单次模型调用的保守字节上限（含消息框架）；0 表示不限制。
	// 每次实际调用前都按它裁剪观察段，超限时从最旧的整条观察开始丢，
	// 当前问题与 Context（含本轮焦点）不参与裁剪。
	PromptBudget int
	// ThinkDeep 要求 thought 先过三层（字面/关联/表达）再决定 action；
	// 弹幕通道开启，评论/私信保持原协议。
	ThinkDeep bool
	// Reflect 在收尾后追加一次自检，预算内才执行；失败保留原稿。
	Reflect bool
}

// Usage 是底层模型调用累计的实际用量；Known* 分别表示服务端是否给出对应统计。
type Usage struct {
	Calls        int
	InputTokens  int
	OutputTokens int
	InputKnown   bool
	OutputKnown  bool
}

// UsageReporter 由能累计服务端用量的 LLM 实现；不实现时调用方只能记估计值。
type UsageReporter interface {
	Usage() Usage
}

// Step 是模型返回的一步决策。
type Step struct {
	Thought string `json:"thought"`
	Action  string `json:"action"`
	Args    string `json:"args"`
	// Final 是 action=final 时的回复正文（也兼容把回复塞进 args 的写法）。
	Final string `json:"final"`
}

// Evidence 是一次成功工具查证的可复用结果。
//
// 只含工具名、参数与结果，不含 thought：思维文本属于内部推理，任何情况下不外发。
// 回落单步生成时作为「已查证的资料」注入 prompt，避免「查对了但回落又不知道」。
type Evidence struct {
	Tool   string
	Args   string
	Result string
}

// Result 是一次 ReAct 的结果。
type Result struct {
	// Text 是最终回复；降级且来不及生成时为空（调用方回落单步生成）。
	Text string
	// Steps 是实际消耗的模型步数（含降级那次）。
	Steps int
	// Tools 是按顺序调用过的工具名。
	Tools []string
	// Evidence 是本轮成功的工具查证结果；降级/超时也保留，供回落单步复用。
	Evidence []Evidence
	// Danmaku 是本轮通过 send_danmaku 成功发出的弹幕正文（按发送顺序）。
	// 非空表示回复已经投递出去，调用方不应再发一条普通回复。
	Danmaku []string
	// Degraded 表示走了降级路径。
	Degraded bool
	// Reason 是降级原因，取自 Degrade* 常量。
	Reason string
	// PromptBytes 是最后一次实际模型调用的保守字节估计（预算口径见 provider.EstimatorVersion）。
	// 预算不足未发起调用时为 0。
	PromptBytes int
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

// HasTool 判断本轮是否注册了某个工具；调用方据此选择主动查证还是被动注入。
func (a *Agent) HasTool(name string) bool {
	if a == nil || a.tools == nil {
		return false
	}
	return a.tools.Has(name)
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
		evidence     []Evidence
		danmaku      []string
		used         []string
		steps        int
	)

	for step := 1; step <= a.opts.MaxSteps; step++ {
		if runCtx.Err() != nil {
			return a.degrade(runCtx, in, observations, evidence, danmaku, used, steps, DegradeTimeout)
		}
		// 每次实际调用前统一按预算裁剪：观察按整条证据丢，问题与资料段不动。
		system, user, kept, ok := a.fitPrompt(a.systemPrompt(in), in, observations)
		if !ok {
			return budgetResult(evidence, danmaku, used, steps), nil
		}
		observations = kept
		out, err := a.llm.Complete(runCtx, system, user)
		steps++
		if err != nil {
			if runCtx.Err() != nil {
				return a.degrade(runCtx, in, observations, evidence, danmaku, used, steps, DegradeTimeout)
			}
			return a.degrade(runCtx, in, observations, evidence, danmaku, used, steps, DegradeLLMError)
		}

		decision, perr := ParseStep(out)
		if perr != nil {
			// 模型有时直接给自然语言回复（没按协议包 JSON）：能过正文校验就当作 final，
			// 不再白花一次降级调用；JSON 形状/内部字段仍会被 replyBody 拒绝。
			if text := replyBody(out); text != "" {
				a.log.Info("ReAct 未按 JSON 协议输出，已按自然语言采用", zap.String("action", "react_plain_reply"))
				return a.finish(runCtx, in, text, steps, observations, evidence, danmaku, used, promptBytes(system, user))
			}
			return a.degrade(runCtx, in, observations, evidence, danmaku, used, steps, DegradeParse)
		}
		if decision.Action == "final" {
			// action=final 时正文可放 final 或 args（协议约定），但必须过正文校验
			text := replyBody(finalBody(decision))
			if text == "" {
				// 说 final 却没给可用正文：走降级，绝不把内部内容当回复
				return a.degrade(runCtx, in, observations, evidence, danmaku, used, steps, DegradeEmpty)
			}
			return a.finish(runCtx, in, text, steps, observations, evidence, danmaku, used, promptBytes(system, user))
		}

		// action 是工具名：执行并把结果回填进下一轮
		out2, toolErr := a.tools.Run(runCtx, decision.Action, decision.Args)
		if toolErr != nil {
			// 工具失败不终止循环：把失败也当成一条观察，让模型自己决定换招
			out2 = "工具失败：" + toolErr.Error()
		} else if decision.Action == ToolSendDanmaku {
			// 发送弹幕是动作不是查证：记进 Result 供调用方抑制重复回复，不进证据。
			if text := danmakuText(decision.Args); text != "" {
				danmaku = append(danmaku, text)
			}
		} else if observed := strings.TrimSpace(out2); observed != "" {
			// 只有成功的查证才是可复用证据；失败结果只进观察，不进证据。
			evidence = appendEvidence(evidence, Evidence{Tool: decision.Action, Args: clip(decision.Args, maxEvidenceArgsRunes), Result: observed})
		}
		used = append(used, decision.Action)
		observations = append(observations, fmt.Sprintf("%s(%s) → %s", decision.Action, clip(decision.Args, maxObservationArgsRunes), clip(out2, maxObservationRunes)))
	}

	return a.degrade(runCtx, in, observations, evidence, danmaku, used, steps, DegradeSteps)
}

// finish 收尾：Reflect 开启且预算允许时追加一次自检，任何失败都保留原稿。
func (a *Agent) finish(ctx context.Context, in Input, draft string, steps int, observations []string, evidence []Evidence, danmaku []string, used []string, bytes int) (Result, error) {
	result := Result{Text: draft, Steps: steps, Tools: used, Evidence: evidence, Danmaku: danmaku, PromptBytes: bytes}
	if !in.Reflect || draft == "" {
		return result, nil
	}
	text, called, promptSize := a.reflect(ctx, in, draft, steps, observations)
	if !called {
		return result, nil
	}
	result.Steps = steps + 1
	result.PromptBytes = promptSize
	if text != "" {
		result.Text = text
	}
	return result, nil
}

// reflect 用一次自检改写草稿；只在步数与剩余时间都够时调用。
//
// 反思是锦上添花：装不进预算、模型报错、回空都保留原稿，绝不因此降级或外发内部字段。
// called 表示是否真的发了请求（发了就算一步），promptSize 是该次调用的字节估计。
func (a *Agent) reflect(ctx context.Context, in Input, draft string, steps int, observations []string) (text string, called bool, promptSize int) {
	if ctx.Err() != nil {
		return "", false, 0
	}
	if steps >= a.opts.MaxSteps {
		a.log.Info("ReAct 反思跳过：步数预算已用尽", zap.String("action", "react_reflect_skip"))
		return "", false, 0
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < reflectMinRemaining {
		a.log.Info("ReAct 反思跳过：剩余时间不足", zap.String("action", "react_reflect_skip"))
		return "", false, 0
	}
	system := a.reflectPrompt(in)
	user := a.reflectUserPrompt(in, observations, draft)
	promptSize = promptBytes(system, user)
	if in.PromptBudget > 0 && promptSize > in.PromptBudget {
		a.log.Info("ReAct 反思跳过：prompt 超预算", zap.String("action", "react_reflect_skip"))
		return "", false, 0
	}
	out, err := a.llm.Complete(ctx, system, user)
	if err != nil {
		a.log.Info("ReAct 反思失败，保留原稿", zap.String("action", "react_reflect_error"))
		return "", true, promptSize
	}
	return replyBody(out), true, promptSize
}

// reflectPrompt 是自检调用的系统提示：保留人设，只要求检查并改写草稿。
func (a *Agent) reflectPrompt(in Input) string {
	var sb strings.Builder
	if s := strings.TrimSpace(in.System); s != "" {
		sb.WriteString(s)
		sb.WriteString("\n\n")
	}
	fmt.Fprintf(&sb, "你在自检一条即将发出的%s回复。\n", inputLabel(in))
	sb.WriteString("检查三件事：有没有答非所问、有没有编造没查到的事实、有没有把内部推理或格式写进正文。\n")
	sb.WriteString("只输出改写后的回复正文：不要 JSON，不要 thought/action/args 字段，不要代码块，不要解释你改了什么。\n")
	sb.WriteString("草稿已经合适就原样输出草稿。")
	return sb.String()
}

// reflectUserPrompt 在原有上下文与观察后附上草稿，供模型自检。
func (a *Agent) reflectUserPrompt(in Input, observations []string, draft string) string {
	var sb strings.Builder
	sb.WriteString(a.userPrompt(in, observations))
	sb.WriteString("\n草稿：\n")
	sb.WriteString(clip(draft, maxReflectDraftRunes))
	return sb.String()
}

// budgetResult 表示预算不足、本轮没发模型请求：调用方看到空正文会回落单步；
// 已发出的弹幕仍然保留，调用方据此跳过重复回复。
func budgetResult(evidence []Evidence, danmaku []string, used []string, steps int) Result {
	return Result{Steps: steps, Tools: used, Evidence: evidence, Danmaku: danmaku, Degraded: true, Reason: DegradeBudget}
}

// promptBytes 按 provider.EstimatorVersion 口径估算一次调用的 prompt 大小。
func promptBytes(system, user string) int {
	return provider.EstimateMessages([]provider.Message{{Role: "system", Content: system}, {Role: "user", Content: user}})
}

// Estimate 估算一次无观察调用的 prompt 字节数（首步基准）。
//
// 调用方先 Prepare，再用它与 ObservationReserve 判断：
// Prepare 的预算结果之外，还有多少固定开销必须提前留出来。
func (a *Agent) Estimate(in Input) int {
	return promptBytes(a.systemPrompt(in), a.userPrompt(in, nil))
}

// ObservationReserve 是 Prepare 之外必须提前留出的观察段字节数。
//
// 每个工具步最多追加一行「工具名(参数) → 结果」：参数 60 字符、结果 400 字符，
// 都按 UTF-8 最坏 4 字节/字符计。这里预留一条整观察（第一次查证用），
// 更多观察由每次调用前的 fitPrompt 按证据单元从旧到新裁剪兜底。
func (a *Agent) ObservationReserve() int {
	maxName := 0
	for _, name := range a.tools.Names() {
		if len(name) > maxName {
			maxName = len(name)
		}
	}
	line := len("- ") + maxName + len("(") + maxObservationArgsRunes*utf8.UTFMax + len("…") +
		len(") → ") + maxObservationRunes*utf8.UTFMax + len("…") + len("\n")
	return len(observationHeader) + observationReserveLines*line
}

// fitPrompt 按调用预算裁剪观察段：从最旧的一条整条丢，绝不切半条证据。
//
// 当前问题与 Context（含本轮焦点）不参与裁剪；连它们都放不下时返回 ok=false，
// 本轮不发模型请求，由调用方回落单步。
func (a *Agent) fitPrompt(system string, in Input, observations []string) (string, string, []string, bool) {
	if in.PromptBudget <= 0 {
		return system, a.userPrompt(in, observations), observations, true
	}
	for {
		user := a.userPrompt(in, observations)
		if promptBytes(system, user) <= in.PromptBudget {
			return system, user, observations, true
		}
		if len(observations) == 0 {
			return system, user, nil, false
		}
		observations = observations[1:]
	}
}

// Usage 返回底层模型调用累计的真实用量；LLM 不支持累计时为 ok=false。
//
// 预算日志用它区分「估计值」与「服务端实际 usage」，两者不能混为一谈。
func (a *Agent) Usage() (Usage, bool) {
	reporter, ok := a.llm.(UsageReporter)
	if !ok {
		return Usage{}, false
	}
	return reporter.Usage(), true
}

// degrade 用已有观察再要一次回复；预算已耗尽则直接返回空，交给调用方回落。
// 已发出的弹幕始终保留在 Result.Danmaku 里，降级不能把它抹掉。
func (a *Agent) degrade(ctx context.Context, in Input, observations []string, evidence []Evidence, danmaku []string, used []string, steps int, reason string) (Result, error) {
	result := Result{Steps: steps, Tools: used, Degraded: true, Reason: reason, Evidence: evidence, Danmaku: danmaku}

	// 超时已经把预算花光：不再调用模型，避免把 8 秒拖成 16 秒
	if ctx.Err() != nil {
		return result, nil //nolint:nilerr // 超时是预期内的降级路径，不是要上抛的错误
	}

	// 降级不再要求 ReAct 协议：去掉工具清单与 JSON 约束，直接要一句人话。
	system := a.finalPrompt(in)
	system, user, _, ok := a.fitPrompt(system, in, observations)
	if !ok {
		// 连降级 prompt 都装不进预算：不发请求，交给调用方回落单步
		result.Reason = DegradeBudget
		return result, nil
	}
	out, err := a.llm.Complete(ctx, system, user)
	result.Steps = steps + 1
	result.PromptBytes = promptBytes(system, user)
	if err != nil {
		// 降级本身失败不向上抛：调用方会回落单步生成，这里报错只会多一条噪音
		return result, nil //nolint:nilerr // 有意吞掉：降级失败交给调用方兜底
	}
	// 降级那次模型仍可能回 JSON：只认 action=final 的正文，
	// thought/args 属于内部推理与工具参数，任何情况下都不外发。
	if decision, perr := ParseStep(out); perr == nil {
		if decision.Action == "final" {
			result.Text = replyBody(finalBody(decision))
		}
		return result, nil
	}
	// 不是合法 JSON：残缺的协议 JSON 同样视为内部内容丢弃；
	// 纯文本才可能是「直接给出最终回复」的正文。
	result.Text = replyBody(out)
	return result, nil
}

// finalBody 只从 action=final 的步里取正文；工具步一律返回空。
func finalBody(step Step) string {
	if step.Action != "final" {
		return ""
	}
	return firstNonEmpty(step.Final, step.Args)
}

// replyBody 校验并清洗最终正文。
//
// 拒绝：空串、代码块、JSON 形状（完整或残缺的协议输出）、
// 以及带 thought/action/args 字段的内部推理。坏内容宁可不发，
// 由调用方回落单步生成，也不能把内部内容当弹幕发出去。
func replyBody(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" || strings.HasPrefix(text, "```") {
		return ""
	}
	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		return ""
	}
	for _, field := range []string{`"action"`, `"thought"`, `"args"`} {
		if strings.Contains(text, field) {
			return ""
		}
	}
	return text
}

// systemPrompt 拼协议说明。
func (a *Agent) systemPrompt(in Input) string {
	var sb strings.Builder
	if s := strings.TrimSpace(in.System); s != "" {
		sb.WriteString(s)
		sb.WriteString("\n\n")
	}
	fmt.Fprintf(&sb, "你在回答一条%s。需要事实就先查，查到够了再回答。\n", inputLabel(in))
	sb.WriteString("「XX 是谁」不一定是在问观众：先看 current_event.question_subject 的 kind，别一律查观众，也别一律解释成本鱼自己。\n")
	sb.WriteString("kind=room_candidate 才优先用 viewer_search 按昵称/uid 查档案；kind=unresolved 不要假定是观众，先查记忆/知识库（只用本轮工具清单里注册的召回工具，必要时 web_search），viewer_search 没查到只表示档案里没有，不代表「世上没这个人」；kind=self 是问本鱼自己；kind=speaker 是问当前发言人。只有「你是谁」才是问你自己。\n")
	sb.WriteString("question_subject.check=true 表示这是「记得XX吗/认识XX/跟XX聊过/XX喂过饭」类回忆核查：先 viewer_search 查该观众档案与侧写，再 memory_search 查本会话互动记忆；昵称不在当前房间窗口里也要先查档案。\n")
	sb.WriteString("context 里有 corrections 时，先承认纠正，再按纠正后的归属回答；superseded_event_ids 里的旧回复和 retracted_record_ids 里的摘要已失效，不得复用。disputed=true 表示还没定位到具体旧内容，不要据此否定该人的历史发言或最近回复。\n")
	sb.WriteString("搜索状态：只有工具结果以「搜索状态：已完成（已联网）」开头时才能说联网查到了；未配置或失败时不许声称已联网，也不许把失败说成「查无此事」，只能说没查到或无法确认。\n")
	sb.WriteString("查记忆时 memory_search 的 args 传昵称或关键词（如「弓长三益」），不要传整句问题。\n")
	sb.WriteString("可用工具：\n")
	sb.WriteString(a.tools.Descriptions())
	if in.ThinkDeep {
		sb.WriteString("thought 必须写完三层再决定 action：①字面（这句话在说什么、在问谁）｜②关联（和当前直播画面、主播声音、最近弹幕、长期设定有什么关系）｜③表达（这一条该用什么语气、说什么才自然）。三层写在同一行，用「｜」分隔。\n")
	}
	sb.WriteString("每一步只输出一个 JSON 对象，第一个字符必须是 {，最后一个字符必须是 }，前后不要任何文字或代码块标记。\n")
	sb.WriteString(`正确示例：{"thought":"先查历史","action":"history","args":"10"}`)
	sb.WriteString("\n")
	sb.WriteString(`正确示例（收尾）：{"thought":"够了","action":"final","final":"记得，你说想吃鱼"}`)
	sb.WriteString("\n")
	sb.WriteString("错误示例：先查一下 {\"action\":\"history\",\"args\":\"10\"}（JSON 前面不能有废话）\n")
	sb.WriteString("错误示例：{\"thought\":\"t\",\"action\":\"history\",\"args\":{\"count\":10}}（args 必须是字符串）\n")
	sb.WriteString("错误示例：```json {\"action\":\"final\",\"final\":\"好\"} ```（不要代码块包裹）\n")
	sb.WriteString(`action=final 时，final 字段（或 args）直接写要发出去的自然语言正文。`)
	return sb.String()
}

// finalPrompt 是降级调用的系统提示：保留人设，去掉 ReAct 协议，只要求正常文本。
func (a *Agent) finalPrompt(in Input) string {
	var sb strings.Builder
	if s := strings.TrimSpace(in.System); s != "" {
		sb.WriteString(s)
		sb.WriteString("\n\n")
	}
	fmt.Fprintf(&sb, "直接回答这条%s，用一句自然语言。\n", inputLabel(in))
	sb.WriteString("只输出要发出去的回复正文：不要 JSON，不要 thought/action/args 字段，不要代码块，不要解释你在做什么。")
	return sb.String()
}

// userPrompt 拼上下文、问题与已获得的观察。
//
// 统一上下文快照放在 user 段：资料不是新指令，不能混进 system。
func (a *Agent) userPrompt(in Input, observations []string) string {
	var sb strings.Builder
	if contextText := strings.TrimSpace(in.Context); contextText != "" {
		sb.WriteString(contextText)
		sb.WriteString("\n")
	}
	fmt.Fprintf(&sb, "%s：%s\n", inputLabel(in), clip(in.Question, 300))
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

// ParseStep 宽松解析模型输出的一步决策。
//
// 容错范围（都只做确定性修复，不猜缺失内容）：
//   - 代码块包裹与 JSON 前后废话；
//   - 对象内尾逗号、字符串里的裸换行/制表符等控制字符；
//   - args 写成对象/数组（摊平成紧凑 JSON 字符串交给工具）；
//   - final 写成 text 别名，或漏了 action 但给了 final（按收尾处理）。
func ParseStep(raw string) (Step, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return Step{}, errors.New("模型输出为空")
	}
	obj := extractStepObject(text)
	if obj == "" {
		return Step{}, errors.New("找不到完整的 JSON 对象")
	}
	var payload struct {
		Thought json.RawMessage `json:"thought"`
		Action  json.RawMessage `json:"action"`
		Args    json.RawMessage `json:"args"`
		Final   json.RawMessage `json:"final"`
		Text    json.RawMessage `json:"text"`
	}
	if err := json.Unmarshal([]byte(obj), &payload); err != nil {
		fixed := repairStepJSON(obj)
		if fixed == obj || json.Unmarshal([]byte(fixed), &payload) != nil {
			return Step{}, err
		}
	}
	step := Step{
		Thought: rawText(payload.Thought),
		Action:  strings.TrimSpace(rawText(payload.Action)),
		Args:    rawText(payload.Args),
		Final:   rawText(payload.Final),
	}
	if step.Final == "" {
		step.Final = rawText(payload.Text)
	}
	if step.Action == "" && step.Final != "" {
		// 直接给了正文却漏写 action：按 final 收尾，不为省一个字段降级。
		step.Action = "final"
	}
	if strings.EqualFold(step.Action, "final") {
		step.Action = "final"
	}
	if step.Action == "" {
		return Step{}, errors.New("缺少 action")
	}
	return step, nil
}

// extractStepObject 提取第一个完整的 JSON 对象：
// 先剥代码块围栏，再按字符串/转义感知的括号配平截取，避免被前后废话里的 } 带偏。
func extractStepObject(text string) string {
	text = stripFence(text)
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
}

// stripFence 去掉代码块围栏，返回围栏内的内容；没有围栏时原样返回。
func stripFence(text string) string {
	open := strings.Index(text, "```")
	if open < 0 {
		return text
	}
	body := strings.TrimSpace(text[open+3:])
	if len(body) >= 4 && strings.EqualFold(body[:4], "json") {
		body = strings.TrimSpace(body[4:])
	}
	if end := strings.Index(body, "```"); end >= 0 {
		body = body[:end]
	}
	return strings.TrimSpace(body)
}

// repairStepJSON 修复常见格式问题：对象/数组收尾前的多余逗号、
// 字符串里的裸换行/制表符等控制字符。修完仍不合法则按解析失败处理。
func repairStepJSON(text string) string {
	out := make([]byte, 0, len(text))
	inString := false
	escaped := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			if escaped {
				escaped = false
				out = append(out, c)
				continue
			}
			switch c {
			case '\\':
				escaped = true
				out = append(out, c)
			case '"':
				inString = false
				out = append(out, c)
			case '\n':
				out = append(out, '\\', 'n')
			case '\r':
				out = append(out, '\\', 'r')
			case '\t':
				out = append(out, '\\', 't')
			default:
				if c < 0x20 {
					out = append(out, []byte(fmt.Sprintf(`\u%04x`, c))...)
				} else {
					out = append(out, c)
				}
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '}', ']':
			for len(out) > 0 && (out[len(out)-1] == ' ' || out[len(out)-1] == '\t' || out[len(out)-1] == '\r' || out[len(out)-1] == '\n') {
				out = out[:len(out)-1]
			}
			if len(out) > 0 && out[len(out)-1] == ',' {
				out = out[:len(out)-1]
			}
		}
		out = append(out, c)
	}
	return string(out)
}

// rawText 把任意 JSON 值摊平成字符串：字符串取值，对象/数组保留紧凑 JSON，
// 数字/布尔取字面量；缺失或 null 返回空串。
func rawText(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return ""
	}
	if value[0] == '"' {
		var s string
		if json.Unmarshal([]byte(value), &s) == nil {
			return s
		}
		return ""
	}
	if value[0] == '{' || value[0] == '[' {
		var buf bytes.Buffer
		if json.Compact(&buf, []byte(value)) == nil {
			return buf.String()
		}
	}
	return value
}

// inputLabel 返回提示词里的回复对象称呼；未指定时按弹幕处理。
func inputLabel(in Input) string {
	if label := strings.TrimSpace(in.Label); label != "" {
		return label
	}
	return "弹幕"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
