package watch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPTranscriberUploadsOpenAICompatibleRequest(t *testing.T) {
	audio := tempPath(t, "clip.wav")
	writeTestFile(t, audio, []byte("wav-bytes"))

	var gotPath, gotName string
	var gotContent []byte
	var gotModel, gotLanguage, gotFormat string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("不是multipart请求：%v", err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("缺少file字段：%v", err)
			return
		}
		defer file.Close()
		gotName = header.Filename
		gotContent, _ = io.ReadAll(file)
		gotModel = r.FormValue("model")
		gotLanguage = r.FormValue("language")
		gotFormat = r.FormValue("response_format")
		fmt.Fprint(w, `{"text":"主播说大家好"}`)
	}))
	defer server.Close()

	transcriber, err := NewHTTPTranscriber(server.URL+"/v1/audio/transcriptions", server.Client(), 0)
	if err != nil {
		t.Fatal(err)
	}
	text, err := transcriber.Transcribe(context.Background(), audio)
	if err != nil {
		t.Fatal(err)
	}
	if text != "主播说大家好" {
		t.Fatalf("转写=%q", text)
	}
	if gotPath != "/v1/audio/transcriptions" {
		t.Fatalf("请求路径=%q", gotPath)
	}
	if gotName != "clip.wav" || string(gotContent) != "wav-bytes" {
		t.Fatalf("上传内容不符：%q %q", gotName, gotContent)
	}
	if gotModel != "whisper-small" || gotLanguage != "zh" || gotFormat != "json" {
		t.Fatalf("可选字段不符：model=%q language=%q response_format=%q", gotModel, gotLanguage, gotFormat)
	}
}

func TestHTTPTranscriberFailuresAreExplicit(t *testing.T) {
	audio := tempPath(t, "clip.wav")
	writeTestFile(t, audio, []byte("wav"))
	status, body := http.StatusOK, `{"text":"ok"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	transcriber, err := NewHTTPTranscriber(server.URL+"/v1/audio/transcriptions", server.Client(), 0)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, body, want string
		status           int
	}{
		{"服务端错误", `{"error":"secret-canary"}`, "HTTP 500", http.StatusInternalServerError},
		{"非JSON响应", `plain text transcript`, "格式无法识别", http.StatusOK},
		{"空转写", `{"text":"  "}`, "空转写", http.StatusOK},
		{"缺字段", `{}`, "空转写", http.StatusOK},
		{"仅transcript不兼容", `{"transcript":"别名结果"}`, "空转写", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body = tc.status, tc.body
			_, err := transcriber.Transcribe(context.Background(), audio)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误=%v，期望包含 %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret-canary") || strings.Contains(err.Error(), "plain text") {
				t.Fatal("错误泄露了响应正文")
			}
		})
	}

	if _, err := transcriber.Transcribe(context.Background(), tempPath(t, "missing.wav")); err == nil {
		t.Fatal("接受不存在的音频")
	}
	if _, err := NewHTTPTranscriber("ftp://127.0.0.1/transcribe", nil, 0); err == nil {
		t.Fatal("接受非法协议")
	}
	if _, err := NewHTTPTranscriber("http://", nil, 0); err == nil {
		t.Fatal("接受空主机")
	}
}

func TestHTTPTranscriberUnavailableService(t *testing.T) {
	audio := tempPath(t, "clip.wav")
	writeTestFile(t, audio, []byte("wav"))
	// 127.0.0.1:1 基本不会监听；要求显式报“不可用”而不是空结果。
	transcriber, err := NewHTTPTranscriber("http://127.0.0.1:1/v1/audio/transcriptions", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transcriber.Transcribe(context.Background(), audio); err == nil || !strings.Contains(err.Error(), "不可用") {
		t.Fatalf("服务不可用未显式报错：%v", err)
	}
}
