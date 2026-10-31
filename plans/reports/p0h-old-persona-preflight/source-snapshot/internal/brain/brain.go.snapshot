package brain

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"time"

	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// OpenAI 使用注入Provider/UsageRepo，不依赖具体HTTP或数据库实现。
type OpenAI struct {
	provider provider.Provider
	usage    store.UsageRepo
	options  Options
	logger   *zap.Logger
	slots    chan struct{}
}

// New 固定生成参数；调用期不修改配置或共享状态。
func New(p provider.Provider, usage store.UsageRepo, o Options, logger *zap.Logger) (*OpenAI, error) {
	if p == nil || usage == nil || logger == nil {
		return nil, &Error{Kind: KindInput}
	}
	if (reflect.ValueOf(p).Kind() == reflect.Pointer && reflect.ValueOf(p).IsNil()) || (reflect.ValueOf(usage).Kind() == reflect.Pointer && reflect.ValueOf(usage).IsNil()) {
		return nil, &Error{Kind: KindInput}
	}
	if strings.TrimSpace(p.Name()) == "" || strings.ContainsAny(p.Name(), ":\r\n") || strings.TrimSpace(o.Model) == "" || o.MaxPromptTokens < 1 || o.MaxPromptTokens > 1000000 || o.MaxReplyTokens < 1 || o.MaxReplyTokens > 8192 || o.AttemptTimeout <= 0 || o.AttemptTimeout > 60*time.Second || o.TotalTimeout <= 0 || o.TotalTimeout > 120*time.Second || o.Backoff <= 0 || o.Backoff > 2*time.Second || o.MaxConcurrent < 1 || o.MaxConcurrent > 64 || math.IsNaN(o.Temperature) || math.IsInf(o.Temperature, 0) || o.Temperature < 0 || o.Temperature > 2 {
		return nil, &Error{Kind: KindInput}
	}
	if (o.InputUSDPerMillion == nil) != (o.OutputUSDPerMillion == nil) {
		return nil, &Error{Kind: KindInput}
	}
	for _, price := range []*float64{o.InputUSDPerMillion, o.OutputUSDPerMillion} {
		if price != nil && (math.IsNaN(*price) || math.IsInf(*price, 0) || *price < 0) {
			return nil, &Error{Kind: KindInput}
		}
	}
	// 价格指针复制，调用方改配置对象不会改变运行中的计费。
	if o.InputUSDPerMillion != nil {
		in, out := *o.InputUSDPerMillion, *o.OutputUSDPerMillion
		o.InputUSDPerMillion = &in
		o.OutputUSDPerMillion = &out
	}
	return &OpenAI{provider: p, usage: usage, options: o, logger: logger, slots: make(chan struct{}, o.MaxConcurrent)}, nil
}

// Name 是实际provider:model路由，便于对账。
func (b *OpenAI) Name() string { return b.provider.Name() + ":" + b.options.Model }

func contextFailure(err error) *Error {
	kind := KindCanceled
	if errors.Is(err, context.DeadlineExceeded) {
		kind = KindTimeout
	}
	return &Error{Kind: kind, Retryable: kind == KindTimeout, Cause: err}
}
func cost(u provider.Usage, o Options) *float64 {
	if u.InputTokens == nil || u.OutputTokens == nil || o.InputUSDPerMillion == nil || o.OutputUSDPerMillion == nil {
		return nil
	}
	v := math.Round((float64(*u.InputTokens)**o.InputUSDPerMillion + float64(*u.OutputTokens)**o.OutputUSDPerMillion)) / 1e6
	return &v
}

type replyMeta struct {
	Provider     string   `json:"provider"`
	Model        string   `json:"model"`
	InputTokens  *int     `json:"in_tokens"`
	OutputTokens *int     `json:"out_tokens"`
	CostUSD      *float64 `json:"cost_usd"`
	Milliseconds int      `json:"ms"`
	Attempts     int      `json:"attempts"`
}

func addCount(total **int, value *int) {
	if *total == nil {
		return
	}
	if value == nil {
		*total = nil
		return
	}
	**total += *value
}

// Reply 只返回文本；失败、取消与记账都有明确的有界退出路径。
func (b *OpenAI) Reply(ctx context.Context, task Task, in ContextInput) (Reply, error) {
	if ctx == nil {
		return Reply{}, &Error{Kind: KindInput}
	}
	started := time.Now()
	totalCtx, cancel := context.WithTimeout(ctx, b.options.TotalTimeout)
	defer cancel()
	if err := pendingError(totalCtx); err != nil {
		return Reply{}, contextFailure(err)
	}
	select {
	case b.slots <- struct{}{}:
		defer func() { <-b.slots }()
	case <-totalCtx.Done():
		return Reply{}, contextFailure(totalCtx.Err())
	}
	if err := inputBounds(totalCtx, task, in); err != nil {
		return Reply{}, err
	}
	if err := validateInput(task, in); err != nil {
		return Reply{}, err
	}
	messages, err := buildMessages(totalCtx, in, b.options.MaxPromptTokens)
	if err != nil {
		return Reply{}, err
	}
	zeroIn, zeroOut, zeroCost := 0, 0, 0.
	meta := replyMeta{Provider: b.provider.Name(), Model: b.options.Model, InputTokens: &zeroIn, OutputTokens: &zeroOut, CostUSD: &zeroCost}
	emptyRetries := 0
	for attempt := 1; attempt <= 3; attempt++ {
		if err := pendingError(totalCtx); err != nil {
			return Reply{}, contextFailure(err)
		}
		callCtx, stop := context.WithTimeout(totalCtx, b.options.AttemptTimeout)
		at := time.Now()
		response, callErr := b.provider.Chat(callCtx, provider.Request{Model: b.options.Model, Messages: messages, MaxTokens: b.options.MaxReplyTokens, Temperature: b.options.Temperature})
		deadlineErr := pendingError(callCtx)
		stop()
		if callErr == nil && deadlineErr != nil {
			callErr = &provider.Error{Kind: provider.KindTimeout, Retryable: true, Cause: deadlineErr}
		}
		text := strings.TrimSpace(response.Text)
		if callErr == nil && text == "" {
			callErr = &Error{Kind: KindEmpty, Retryable: emptyRetries == 0}
			emptyRetries++
		}
		for _, n := range []*int{response.Usage.InputTokens, response.Usage.OutputTokens} {
			if n != nil && (*n < 0 || int64(*n) > 2147483647) {
				response.Usage = provider.Usage{}
				callErr = &provider.Error{Kind: provider.KindProtocol}
				break
			}
		}
		row := store.Usage{At: at.UTC(), Channel: task.Channel, Model: b.Name(), InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens, CostUSD: cost(response.Usage, b.options), Milliseconds: int(time.Since(at).Milliseconds()), OK: callErr == nil}
		accountCtx, finish := context.WithTimeout(context.WithoutCancel(totalCtx), 5*time.Second)
		accountErr := b.usage.RecordUsage(accountCtx, row)
		finish()
		if accountErr != nil {
			b.logger.Error("LLM调用记账失败", zap.String("event_id", task.EventID), zap.String("ch", string(task.Channel)), zap.String("session", task.Session), zap.String("action", "usage"), zap.String("result", "failed"), zap.Int("cost_ms", row.Milliseconds))
			return Reply{}, &Error{Kind: KindUsage, Cause: errors.Join(accountErr, callErr)}
		}
		meta.Attempts = attempt
		addCount(&meta.InputTokens, row.InputTokens)
		addCount(&meta.OutputTokens, row.OutputTokens)
		if meta.CostUSD != nil {
			if row.CostUSD == nil {
				meta.CostUSD = nil
			} else {
				*meta.CostUSD = math.Round((*meta.CostUSD+*row.CostUSD)*1e6) / 1e6
			}
		}
		result := "ok"
		if callErr != nil {
			result = "failed"
		}
		b.logger.Info("LLM调用完成", zap.String("event_id", task.EventID), zap.String("ch", string(task.Channel)), zap.String("session", task.Session), zap.String("action", "llm"), zap.String("result", result), zap.Int("cost_ms", row.Milliseconds), zap.String("model", b.Name()))
		if err := pendingError(totalCtx); err != nil {
			return Reply{}, contextFailure(err)
		}
		if callErr == nil {
			runes := []rune(text)
			if len(runes) > replyLimit(task.Channel) {
				text = string(runes[:replyLimit(task.Channel)])
			}
			meta.Milliseconds = int(time.Since(started).Milliseconds())
			data, err := json.Marshal(meta)
			if err != nil {
				return Reply{}, &Error{Kind: KindUsage, Cause: err}
			}
			return Reply{Text: text, Meta: data}, nil
		}
		failure := &Error{Kind: KindProvider, Cause: callErr}
		var providerErr *provider.Error
		var brainErr *Error
		if errors.As(callErr, &providerErr) {
			failure.Retryable = providerErr.Retryable
			if providerErr.Kind == provider.KindTimeout {
				failure.Kind = KindTimeout
			}
			if providerErr.Kind == provider.KindCanceled {
				failure.Kind = KindCanceled
			}
		}
		if errors.As(callErr, &brainErr) {
			failure.Kind = brainErr.Kind
			failure.Retryable = brainErr.Retryable
		}
		if !failure.Retryable || attempt == 3 {
			return Reply{}, failure
		}
		delay := b.options.Backoff * time.Duration(1<<(attempt-1))
		if delay > 2*time.Second {
			delay = 2 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-totalCtx.Done():
			timer.Stop()
			return Reply{}, contextFailure(totalCtx.Err())
		case <-timer.C:
		}
	}
	return Reply{}, &Error{Kind: KindProvider}
}
