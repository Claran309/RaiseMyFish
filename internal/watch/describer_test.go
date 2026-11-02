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

	describer, err := NewOpenAIDescriber(server.URL, "test-key", "deepseek-chat", server.Client(), 0)
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
			describer, err := NewOpenAIDescriber(server.URL, "", "deepseek-chat", server.Client(), 0)
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
	describer, err := NewOpenAIDescriber(server.URL, "", "deepseek-chat", server.Client(), 0)
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
	if _, err := NewOpenAIDescriber("http://example.com/v1", "", "m", nil, 0); err == nil {
		t.Fatal("允许非本机HTTP端点")
	}
	if _, err := NewOpenAIDescriber(server.URL, "", "", nil, 0); err == nil {
		t.Fatal("接受空模型")
	}
}
