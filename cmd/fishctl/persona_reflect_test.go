package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/personaeval"
)

// TestJudgeReflectionParsesVerdict judge 输出大小写/空白容错，非法输出按 neutral。
func TestJudgeReflectionParsesVerdict(t *testing.T) {
	cases := []struct {
		reply string
		want  string
	}{
		{"improved", "improved"},
		{" HARMED \n", "harmed"},
		{"Neutral", "neutral"},
		{"不知道", "neutral"},
	}
	for _, tc := range cases {
		body, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": tc.reply}}},
		})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(body)
		}))
		client, err := provider.NewOpenAI("fake", server.URL, "", server.Client(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		got, err := judgeReflection(context.Background(), client, "m", personaeval.ReflectionCase{
			ID: "x", Input: "在吗", Problem: "答非所问",
		}, "草稿", "反思后")
		server.Close()
		if err != nil {
			t.Fatalf("reply=%q err=%v cause=%v", tc.reply, err, errors.Unwrap(err))
		}
		if got != tc.want {
			t.Fatalf("verdict=%q，期望 %q", got, tc.want)
		}
	}
}

// TestRunPersonaReflectRejectsBadArgs 缺 output/超时非法时不连模型。
func TestRunPersonaReflectRejectsBadArgs(t *testing.T) {
	if err := runPersonaReflect(context.Background(), "", "", []string{}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("缺 output 被接受")
	}
	if err := runPersonaReflect(context.Background(), "", "", []string{"--output", "x", "--timeout", "3h"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("超长 timeout 被接受")
	}
}
