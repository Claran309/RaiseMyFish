package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/personaeval"
	"go.uber.org/zap"
)

// TestCandidateFullPersonaBudget 使用假HTTP验证真实装配，不读取.env/PG或访问模型。
func TestCandidateFullPersonaBudget(t *testing.T) {
	scorer, err := personaeval.NewScorerVersion("persona-lexical-v3")
	if err != nil {
		t.Fatal(err)
	}
	f, err := personaeval.Prepare(context.Background(), "../../personas/dafeiyu-v2", "../../personas/dafeiyu/eval/cases.jsonl", scorer)
	if err != nil {
		t.Fatal(err)
	}
	if f.Pack.Meta.Name != "dafeiyu" || f.Pack.Meta.Version != "v2" || len(f.Cases) != 35 {
		t.Fatal("候选版本/原集错误")
	}
	full := f.Pack.SystemPrompt()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []provider.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if len(request.Messages) != 2 || request.Messages[0].Content != full {
			t.Error("候选人格被裁或system变化")
		}
		_, _ = fmt.Fprint(w, `{"model":"unit-fake","choices":[{"message":{"content":"本鱼在"}}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`)
	}))
	defer server.Close()
	p, err := provider.NewOpenAI("unit", server.URL, "unit-not-real", server.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	audit := &evalProvider{inner: p}
	usage := &evalUsage{inner: evalUsageStub{}}
	o := brain.DefaultOptions()
	o.Model = "deepseek-chat"
	o.MaxPromptTokens = 32768
	o.MaxConcurrent = 1
	b, err := brain.New(audit, usage, o, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	g := &evalGenerator{runtime: b, pack: f.Pack, provider: audit, usage: usage, runID: "20990101T000000Z-00000000"}
	maximum := 0
	for _, c := range f.Cases {
		result, err := g.Generate(context.Background(), personaeval.Input{ID: c.ID, Channel: c.Channel, Private: c.Private, Text: c.Input})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Attempts) != 1 || result.Attempts[0].SystemSHA256 != personaeval.Digest([]byte(full)) {
			t.Fatal("完整system未注入")
		}
		if result.Attempts[0].PromptEstimate > 32768 {
			t.Fatal("预算超限")
		}
		if result.Attempts[0].PromptEstimate > maximum {
			maximum = result.Attempts[0].PromptEstimate
		}
	}
	t.Logf("real_model_calls=0 cases=35 full_system_bytes=%d maximum_estimate=%d", len(full), maximum)
}
