package main

// 本文件覆盖评论附图识别：白名单下载、逐图转述、失败降级，以及 Generate 层把
// 描述注入 Brain/ReAct 共用的 ContextInput.Visual，不阻塞评论回复。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// testPNG 只需要能被 http.DetectContentType 认成 image/png。
var testPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// imageClient 按 URL 决定状态码与正文，并记录被请求的地址。
func imageClient(t *testing.T, handle func(string) (int, []byte)) (*http.Client, *[]string) {
	t.Helper()
	seen := &[]string{}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		*seen = append(*seen, req.URL.String())
		status, body := handle(req.URL.String())
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
	return client, seen
}

// stubImageDescriber 记录调用与提示词，按脚本返回描述。
type stubImageDescriber struct {
	calls   int
	prompts []string
	paths   []string
	results []string
	err     error
}

func (s *stubImageDescriber) Describe(_ context.Context, frames []string, prompt string) (string, error) {
	s.calls++
	s.prompts = append(s.prompts, prompt)
	if len(frames) > 0 {
		s.paths = append(s.paths, frames[0])
	}
	if s.err != nil {
		return "", s.err
	}
	if s.calls <= len(s.results) {
		return s.results[s.calls-1], nil
	}
	return "描述", nil
}

func commentMeta(pics ...string) json.RawMessage {
	raw, _ := json.Marshal(struct {
		Pics []string `json:"pics"`
	}{pics})
	return raw
}

func newTestCommentVision(t *testing.T, sd *stubImageDescriber, maxImages int, client *http.Client) *commentVision {
	t.Helper()
	v, err := newCommentVision(sd, 2*time.Second, maxImages, zap.NewNop())
	if err != nil {
		t.Fatalf("构造评论识图失败：%v", err)
	}
	if client != nil {
		v.client = client
	}
	return v
}

// TestCommentVisionDescribeInjectsImages 多图逐张转述，输出带图序并清理临时文件。
func TestCommentVisionDescribeInjectsImages(t *testing.T) {
	client, seen := imageClient(t, func(string) (int, []byte) { return http.StatusOK, testPNG })
	sd := &stubImageDescriber{results: []string{"一只橘猫在啃鱼干", "屏幕上写着「摸鱼中」"}}
	v := newTestCommentVision(t, sd, 2, client)
	ev := event.Event{ID: "comment:123:1", Channel: event.ChComment, Meta: commentMeta(
		"https://i0.hdslb.com/bfs/a.png", "https://i1.hdslb.com/bfs/b.png",
	)}

	got := v.Describe(context.Background(), ev)
	for _, want := range []string{"评论附图（视觉模型转述）：", "图1：一只橘猫在啃鱼干", "图2：屏幕上写着「摸鱼中」"} {
		if !strings.Contains(got, want) {
			t.Errorf("描述缺少 %q：\n%s", want, got)
		}
	}
	if sd.calls != 2 || len(*seen) != 2 {
		t.Fatalf("下载/识别次数 = %d/%d，期望 2/2", len(*seen), sd.calls)
	}
	for _, prompt := range sd.prompts {
		if prompt != commentVisionPrompt {
			t.Errorf("视觉提示词被改动：%q", prompt)
		}
	}
	for _, path := range sd.paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("临时图片未清理：%s (%v)", path, err)
		}
	}
}

// TestCommentVisionPartialFailureSkipsImage 单图失败只丢这张，其他图仍进上下文。
func TestCommentVisionPartialFailureSkipsImage(t *testing.T) {
	client, _ := imageClient(t, func(raw string) (int, []byte) {
		if strings.Contains(raw, "bad") {
			return http.StatusInternalServerError, nil
		}
		return http.StatusOK, testPNG
	})
	sd := &stubImageDescriber{results: []string{"能看"}}
	v := newTestCommentVision(t, sd, 2, client)
	ev := event.Event{ID: "comment:123:2", Channel: event.ChComment, Meta: commentMeta(
		"https://i0.hdslb.com/bad.png", "https://i0.hdslb.com/ok.png",
	)}

	got := v.Describe(context.Background(), ev)
	if !strings.Contains(got, "图2：能看") || strings.Contains(got, "图1") {
		t.Errorf("部分失败处理不符（应保留原图序号）：\n%s", got)
	}
	if sd.calls != 1 {
		t.Errorf("只应识别成功的一张，实际 %d 次", sd.calls)
	}
}

// TestCommentVisionAllFailReturnsEmpty 全部失败等于无图，不返回半截占位文本。
func TestCommentVisionAllFailReturnsEmpty(t *testing.T) {
	client, _ := imageClient(t, func(string) (int, []byte) { return http.StatusBadGateway, nil })
	sd := &stubImageDescriber{results: []string{"不该被调用"}}
	v := newTestCommentVision(t, sd, 2, client)
	ev := event.Event{ID: "comment:123:3", Channel: event.ChComment, Meta: commentMeta(
		"https://i0.hdslb.com/a.png", "https://i0.hdslb.com/b.png",
	)}

	if got := v.Describe(context.Background(), ev); got != "" {
		t.Errorf("全部失败应返回空串，实际：%q", got)
	}
	if sd.calls != 0 {
		t.Errorf("下载失败不应调用视觉模型，实际 %d 次", sd.calls)
	}
}

// TestCommentVisionRejectsUntrustedHost 非 https / 非 B站图床不发起任何请求。
func TestCommentVisionRejectsUntrustedHost(t *testing.T) {
	client, seen := imageClient(t, func(string) (int, []byte) { return http.StatusOK, testPNG })
	sd := &stubImageDescriber{}
	v := newTestCommentVision(t, sd, 2, client)
	ev := event.Event{ID: "comment:123:4", Channel: event.ChComment, Meta: commentMeta(
		"https://evil.example.com/a.png",
		"http://i0.hdslb.com/b.png",
		"https://user:pass@i0.hdslb.com/c.png",
	)}

	if got := v.Describe(context.Background(), ev); got != "" {
		t.Errorf("非法域名应返回空串，实际：%q", got)
	}
	if len(*seen) != 0 || sd.calls != 0 {
		t.Errorf("非法域名不应发起请求：download=%d describer=%d", len(*seen), sd.calls)
	}
}

// TestCommentVisionLimitsImages 只处理配置张数，多余图片不下载。
func TestCommentVisionLimitsImages(t *testing.T) {
	client, seen := imageClient(t, func(string) (int, []byte) { return http.StatusOK, testPNG })
	sd := &stubImageDescriber{results: []string{"第一张"}}
	v := newTestCommentVision(t, sd, 1, client)
	ev := event.Event{ID: "comment:123:5", Channel: event.ChComment, Meta: commentMeta(
		"https://i0.hdslb.com/a.png", "https://i0.hdslb.com/b.png", "https://i0.hdslb.com/c.png",
	)}

	got := v.Describe(context.Background(), ev)
	if !strings.Contains(got, "图1：第一张") || strings.Contains(got, "图2") {
		t.Errorf("张数上限未生效：\n%s", got)
	}
	if len(*seen) != 1 || sd.calls != 1 {
		t.Errorf("超出上限的图片不应下载：download=%d describer=%d", len(*seen), sd.calls)
	}
}

// TestCommentVisionRejectsOversizeImage 单图超过 4MB 直接跳过。
func TestCommentVisionRejectsOversizeImage(t *testing.T) {
	big := bytes.Repeat([]byte("x"), commentImageMaxBytes+1)
	client, _ := imageClient(t, func(string) (int, []byte) { return http.StatusOK, big })
	v := newTestCommentVision(t, &stubImageDescriber{}, 2, client)
	ev := event.Event{ID: "comment:123:6", Channel: event.ChComment, Meta: commentMeta("https://i0.hdslb.com/big.png")}

	if got := v.Describe(context.Background(), ev); got != "" {
		t.Errorf("超大图应返回空串，实际：%q", got)
	}
}

// TestCommentVisionRejectsRedirectAway 302 到白名单外同样拒绝，防借重定向访问内网。
func TestCommentVisionRejectsRedirectAway(t *testing.T) {
	requests := 0
	v, err := newCommentVision(&stubImageDescriber{}, 2*time.Second, 2, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	v.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://evil.example.com/a.png"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})
	ev := event.Event{ID: "comment:123:7", Channel: event.ChComment, Meta: commentMeta("https://i0.hdslb.com/a.png")}
	if got := v.Describe(context.Background(), ev); got != "" {
		t.Errorf("重定向到白名单外应返回空串，实际：%q", got)
	}
	if requests != 1 {
		t.Errorf("只应发起首次请求，实际 %d 次", requests)
	}
}

// TestCommentVisionOnlyForComments 其他通道即使 Meta 带 pics 也不下载。
func TestCommentVisionOnlyForComments(t *testing.T) {
	client, seen := imageClient(t, func(string) (int, []byte) { return http.StatusOK, testPNG })
	v := newTestCommentVision(t, &stubImageDescriber{}, 2, client)
	ev := event.Event{ID: "dm:1:1", Channel: event.ChDM, Meta: commentMeta("https://i0.hdslb.com/a.png")}
	if got := v.Describe(context.Background(), ev); got != "" || len(*seen) != 0 {
		t.Errorf("非评论通道不应识图：text=%q download=%d", got, len(*seen))
	}
}

// TestAllowedCommentImageURL 只放行 https 的 B站图床域名。
func TestAllowedCommentImageURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://i0.hdslb.com/bfs/a.jpg", true},
		{"https://hdslb.com/a.jpg", true},
		{"https://s1.biliimg.com/a.png", true},
		{"https://www.bilibili.com/a.png", true},
		{"http://i0.hdslb.com/a.jpg", false},
		{"https://user:pass@i0.hdslb.com/a.jpg", false},
		{"https://evilhdslb.com/a.jpg", false},
		{"https://i0.hdslb.com.evil.com/a.jpg", false},
		{"", false},
		{"not-a-url", false},
	}
	for _, tc := range cases {
		if got := allowedCommentImageURL(tc.url); got != tc.want {
			t.Errorf("allowedCommentImageURL(%q) = %v，期望 %v", tc.url, got, tc.want)
		}
	}
}

// TestCommentImageExt 只接受视觉客户端支持的三种格式。
func TestCommentImageExt(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"png", testPNG, ".png"},
		{"jpeg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF"), ".jpg"},
		{"webp", []byte("RIFF\x24\x00\x00\x00WEBPVP8 "), ".webp"},
		{"text", []byte("hello"), ""},
	}
	for _, tc := range cases {
		if got := commentImageExt(tc.data); got != tc.want {
			t.Errorf("%s = %q，期望 %q", tc.name, got, tc.want)
		}
	}
}

// TestSanitizeVisionText 清控制字符；非法 UTF-8 直接丢弃。
func TestSanitizeVisionText(t *testing.T) {
	if got := sanitizeVisionText("  一行\r\n两行\x00  "); got != "一行\n两行" {
		t.Errorf("清理结果 = %q", got)
	}
	if got := sanitizeVisionText(string([]byte{0xff, 0xfe})); got != "" {
		t.Errorf("非法 UTF-8 应返回空串，实际 %q", got)
	}
}

// TestBuildCommentVisionConfig 关闭/LLM 未启用/路由缺失都不装配；正常配置带出超时与张数。
func TestBuildCommentVisionConfig(t *testing.T) {
	if v, err := buildCommentVision(config.Config{}, nil, zap.NewNop()); err != nil || v != nil {
		t.Fatalf("未开启应返回 nil：%v %v", v, err)
	}
	cfg := config.Config{
		LLM: config.LLM{
			Enabled: true,
			Providers: map[string]config.Provider{
				"fake": {Type: "openai", BaseURL: "https://api.example.com", APIKey: "unit-only", Models: []string{"model"}},
			},
			Routing: map[string]string{"default": "fake:model"},
		},
		CommentVision: config.CommentVision{Enabled: true, LLMRoute: "default", TimeoutMS: 5000, MaxImages: 2},
	}
	v, err := buildCommentVision(cfg, nil, zap.NewNop())
	if err != nil || v == nil {
		t.Fatalf("正常配置装配失败：%v", err)
	}
	if v.timeout != 5*time.Second || v.maxImages != 2 {
		t.Errorf("装配参数不符：timeout=%v max=%d", v.timeout, v.maxImages)
	}
	bad := cfg
	bad.CommentVision.LLMRoute = "missing"
	if _, err := buildCommentVision(bad, nil, zap.NewNop()); err == nil {
		t.Error("路由缺失应报错")
	}
	noLLM := cfg
	noLLM.LLM.Enabled = false
	if v, err := buildCommentVision(noLLM, nil, zap.NewNop()); err != nil || v != nil {
		t.Errorf("LLM 未启用应降级为 nil：%v %v", v, err)
	}
}

func commentClaim(meta json.RawMessage) event.Claim {
	const id = "comment:123:77"
	return event.Claim{
		Event: event.Event{
			ID: id, Channel: event.ChComment, Dir: event.DirIn,
			Session: "123", FromID: "42", FromName: "观众",
			Text: "这图什么意思", At: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC),
			Status: event.StatusQueued, Meta: meta,
		},
		Lease: event.Lease{Owner: "worker-1", Version: 1},
	}
}

func commentTask(id string) *store.Task {
	return &store.Task{ID: "task-" + id, EventID: id, Channel: event.ChComment, Session: "123", Brain: "default", CreatedAt: time.Now().UTC()}
}

func commentWorkers(t *testing.T, vision *commentVision, engine brain.Brain, snd sender.Sender) scheduler.Workers {
	t.Helper()
	pack := snapshotTestPack()
	pack.Contexts[persona.ContextComment] = "你在B站评论区陪观众聊天"
	const id = "comment:123:77"
	workers, err := buildConsumeWorkers(
		scheduler.ChannelPolicy{}, pack,
		map[event.Channel]brain.Brain{event.ChComment: engine},
		snd, newFakeBus(id), time.Second,
		nil, nil, nil, "", nil, nil, nil, nil, zap.NewNop(),
		withCommentVision(vision),
	)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	return workers
}

// TestGenerateInjectsCommentVisionIntoBrainInput 带图评论 → 描述进 ContextInput.Visual。
func TestGenerateInjectsCommentVisionIntoBrainInput(t *testing.T) {
	client, _ := imageClient(t, func(string) (int, []byte) { return http.StatusOK, testPNG })
	vision := newTestCommentVision(t, &stubImageDescriber{results: []string{"图里是一碗面，旁边写着「加蛋」"}}, 2, client)
	engine := &fakeBrain{reply: brain.Reply{Text: "这面看着不错"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	workers := commentWorkers(t, vision, engine, snd)

	claim := commentClaim(commentMeta("https://i0.hdslb.com/bfs/noodles.png"))
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: claim, Task: commentTask(claim.Event.ID)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}
	if !strings.Contains(engine.gotInput.Visual, "一碗面") || !strings.Contains(engine.gotInput.Visual, "加蛋") {
		t.Errorf("图片描述未注入 Visual：%q", engine.gotInput.Visual)
	}
	if snd.sendCalls != 1 || snd.gotReply.Text != "这面看着不错" {
		t.Errorf("评论回复未发送：calls=%d text=%q", snd.sendCalls, snd.gotReply.Text)
	}
}

// TestGenerateCommentVisionReachesReActContext 评论图描述进入 ReAct 的 Context（不只单步）。
func TestGenerateCommentVisionReachesReActContext(t *testing.T) {
	client, _ := imageClient(t, func(string) (int, []byte) { return http.StatusOK, testPNG })
	vision := newTestCommentVision(t, &stubImageDescriber{results: []string{"图里是一碗面，旁边写着「加蛋」"}}, 2, client)
	llm := &capturingReactLLM{}
	runtime := newCapturingReactRuntime(t, llm)
	engine := &fakeBrain{reply: brain.Reply{Text: "不该被调用"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	pack := snapshotTestPack()
	pack.Contexts[persona.ContextComment] = "你在B站评论区陪观众聊天"
	const id = "comment:123:77"
	workers, err := buildConsumeWorkers(
		scheduler.ChannelPolicy{}, pack,
		map[event.Channel]brain.Brain{event.ChComment: engine},
		snd, newFakeBus(id), time.Second,
		nil, nil, nil, "", nil, nil, runtime, nil, zap.NewNop(),
		withCommentVision(vision),
	)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	claim := commentClaim(commentMeta("https://i0.hdslb.com/bfs/noodles.png"))
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: claim, Task: commentTask(claim.Event.ID)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}
	if llm.calls != 1 || engine.calls != 0 {
		t.Fatalf("评论应走 ReAct：llm=%d brain=%d", llm.calls, engine.calls)
	}
	if !strings.Contains(llm.system, "你在回答一条评论") {
		t.Errorf("评论 ReAct 提示词未区分通道：%q", llm.system)
	}
	if !strings.Contains(llm.user, "一碗面") || !strings.Contains(llm.user, "加蛋") {
		t.Errorf("图片描述未进入 ReAct Context：\n%s", llm.user)
	}
}

// TestGenerateCommentVisionFailureStillReplies 识图失败只降级为无图，不阻塞评论回复。
func TestGenerateCommentVisionFailureStillReplies(t *testing.T) {
	client, _ := imageClient(t, func(string) (int, []byte) { return http.StatusInternalServerError, nil })
	vision := newTestCommentVision(t, &stubImageDescriber{}, 2, client)
	engine := &fakeBrain{reply: brain.Reply{Text: "我看不到图，但可以说说"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	workers := commentWorkers(t, vision, engine, snd)

	claim := commentClaim(commentMeta("https://i0.hdslb.com/bfs/broken.png"))
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: claim, Task: commentTask(claim.Event.ID)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}
	if engine.gotInput.Visual != "" {
		t.Errorf("识图失败不应注入 Visual：%q", engine.gotInput.Visual)
	}
	if engine.calls != 1 || snd.sendCalls != 1 {
		t.Errorf("识图失败不应阻塞回复：brain=%d sender=%d", engine.calls, snd.sendCalls)
	}
}
