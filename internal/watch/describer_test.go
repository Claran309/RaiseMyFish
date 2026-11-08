package watch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAIDescriberSendsBase64Frames(t *testing.T) {
	frameOne := tempPath(t, "frame-00.jpg")
	frameTwo := tempPath(t, "frame-01.jpg")
	writeTestFile(t, frameOne, []byte("first-frame"))
	writeTestFile(t, frameTwo, []byte("second-frame"))

	var gotAuth, gotModel, gotPrompt string
	var gotImages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					ImageURL *struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("请求不是合法JSON：%v", err)
		}
		gotModel = payload.Model
		if len(payload.Messages) != 1 || len(payload.Messages[0].Content) != 3 {
			t.Errorf("消息结构不符：%+v", payload.Messages)
		}
		gotPrompt = payload.Messages[0].Content[0].Text
		for _, part := range payload.Messages[0].Content[1:] {
			if part.Type != "image_url" || part.ImageURL == nil {
				t.Errorf("缺少image_url：%+v", part)
				continue
			}
			gotImages = append(gotImages, part.ImageURL.URL)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"主播正在喝水，屏幕上有弹幕"}}]}`)
	}))
	defer server.Close()

	describer, err := NewOpenAIDescriber(server.URL, "test-key", "deepseek-chat", server.Client(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	text, err := describer.Describe(context.Background(), []string{frameOne, frameTwo}, DefaultPrompt)
	if err != nil {
		t.Fatal(err)
	}
	if text != "主播正在喝水，屏幕上有弹幕" {
		t.Fatalf("描述=%q", text)
	}
	if gotAuth != "Bearer test-key" || gotModel != "deepseek-chat" || gotPrompt != DefaultPrompt {
		t.Fatalf("请求头/模型/提示词不符：%q %q %q", gotAuth, gotModel, gotPrompt)
	}
	if len(gotImages) != 2 ||
		gotImages[0] != "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString([]byte("first-frame")) ||
		gotImages[1] != "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString([]byte("second-frame")) {
		t.Fatalf("帧base64不符：%v", gotImages)
	}
}

func TestOpenAIDescriberErrorsAreExplicit(t *testing.T) {
	frame := tempPath(t, "frame.jpg")
	writeTestFile(t, frame, []byte("jpeg"))

	var status int
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer server.Close()

	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"HTTP错误脱敏", http.StatusInternalServerError, `{"error":"secret-canary"}`, "HTTP 500"},
		{"空描述", http.StatusOK, `{"choices":[{"message":{"content":"  "}}]}`, "空描述"},
		{"缺描述", http.StatusOK, `{"choices":[]}`, "缺少描述"},
		{"响应非JSON", http.StatusOK, `not-json`, "格式无法识别"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body = tc.status, tc.body
			describer, err := NewOpenAIDescriber(server.URL, "", "deepseek-chat", server.Client(), 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = describer.Describe(context.Background(), []string{frame}, DefaultPrompt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误=%v，期望包含 %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret-canary") {
				t.Fatal("错误泄露了响应正文")
			}
		})
	}
}

func TestOpenAIDescriberInputValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()
	describer, err := NewOpenAIDescriber(server.URL, "", "deepseek-chat", server.Client(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := describer.Describe(context.Background(), nil, DefaultPrompt); err == nil {
		t.Fatal("接受空帧")
	}
	if _, err := describer.Describe(context.Background(), []string{tempPath(t, "missing.jpg")}, DefaultPrompt); err == nil {
		t.Fatal("接受不存在的帧")
	}
	gif := tempPath(t, "frame.gif")
	writeTestFile(t, gif, []byte("gif"))
	if _, err := describer.Describe(context.Background(), []string{gif}, DefaultPrompt); err == nil {
		t.Fatal("接受不支持的帧格式")
	}
	if _, err := NewOpenAIDescriber("http://example.com/v1", "", "m", nil, 0, nil); err == nil {
		t.Fatal("允许非本机HTTP端点")
	}
	if _, err := NewOpenAIDescriber(server.URL, "", "", nil, 0, nil); err == nil {
		t.Fatal("接受空模型")
	}
}

// TestDescribeRecordsUsage O08：视觉响应里的 usage 要交给 sink（成功路径）。
func TestDescribeRecordsUsage(t *testing.T) {
	frame := tempPath(t, "frame-00.jpg")
	writeTestFile(t, frame, []byte("jpeg-bytes"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"画面正常"}}],"usage":{"prompt_tokens":120,"completion_tokens":30}}`)
	}))
	defer server.Close()

	var got VisionUsage
	var gotOK bool
	called := 0
	sink := func(_ context.Context, u VisionUsage, ok bool, _ time.Duration) {
		called++
		got, gotOK = u, ok
	}
	describer, err := NewOpenAIDescriber(server.URL, "", "vision-model", server.Client(), 0, sink)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := describer.Describe(context.Background(), []string{frame}, DefaultPrompt); err != nil {
		t.Fatal(err)
	}
	if called != 1 || !gotOK {
		t.Fatalf("sink 未按成功记账：calls=%d ok=%v", called, gotOK)
	}
	if got.InputTokens == nil || *got.InputTokens != 120 {
		t.Fatalf("输入 token 不符：%v", got.InputTokens)
	}
	if got.OutputTokens == nil || *got.OutputTokens != 30 {
		t.Fatalf("输出 token 不符：%v", got.OutputTokens)
	}
}

// TestDescribeRecordsUnknownUsage 服务端没给 usage 时记 unknown（nil），绝不写成 0。
func TestDescribeRecordsUnknownUsage(t *testing.T) {
	frame := tempPath(t, "frame-00.jpg")
	writeTestFile(t, frame, []byte("jpeg-bytes"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"画面正常"}}]}`)
	}))
	defer server.Close()

	var got VisionUsage
	describer, err := NewOpenAIDescriber(server.URL, "", "vision-model", server.Client(), 0,
		func(_ context.Context, u VisionUsage, _ bool, _ time.Duration) { got = u })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := describer.Describe(context.Background(), []string{frame}, DefaultPrompt); err != nil {
		t.Fatal(err)
	}
	if got.InputTokens != nil || got.OutputTokens != nil {
		t.Fatalf("缺失 usage 应为未知（nil）：%+v", got)
	}
}

// TestDescribeRecordsFailure 调用失败也要记一行（OK=false），否则模型一直挂看不见。
func TestDescribeRecordsFailure(t *testing.T) {
	frame := tempPath(t, "frame-00.jpg")
	writeTestFile(t, frame, []byte("jpeg-bytes"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	called, gotOK := 0, true
	describer, err := NewOpenAIDescriber(server.URL, "", "vision-model", server.Client(), 0,
		func(_ context.Context, _ VisionUsage, ok bool, _ time.Duration) {
			called++
			gotOK = ok
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := describer.Describe(context.Background(), []string{frame}, DefaultPrompt); err == nil {
		t.Fatal("500 应报错")
	}
	if called != 1 || gotOK {
		t.Fatalf("失败也应记账且 ok=false：calls=%d ok=%v", called, gotOK)
	}
}

// TestDescribeSanitizesBadTokens 负数/越界 token 记 unknown，不让脏数据进账本。
func TestDescribeSanitizesBadTokens(t *testing.T) {
	frame := tempPath(t, "frame-00.jpg")
	writeTestFile(t, frame, []byte("jpeg-bytes"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"画面正常"}}],"usage":{"prompt_tokens":-5,"completion_tokens":99999999999}}`)
	}))
	defer server.Close()

	var got VisionUsage
	describer, err := NewOpenAIDescriber(server.URL, "", "vision-model", server.Client(), 0,
		func(_ context.Context, u VisionUsage, _ bool, _ time.Duration) { got = u })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := describer.Describe(context.Background(), []string{frame}, DefaultPrompt); err != nil {
		t.Fatal(err)
	}
	if got.InputTokens != nil || got.OutputTokens != nil {
		t.Fatalf("非法 token 应为 unknown：%+v", got)
	}
}
