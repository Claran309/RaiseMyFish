package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
)

type fakeCall struct {
	name string
	args string
}

type fakeToolset struct {
	names []string
	calls []fakeCall
	out   map[string]string
	errs  map[string]error
}

func (f *fakeToolset) Names() []string { return f.names }

func (f *fakeToolset) Run(_ context.Context, name, args string) (string, error) {
	f.calls = append(f.calls, fakeCall{name: name, args: args})
	if err := f.errs[name]; err != nil {
		return "", err
	}
	return f.out[name], nil
}

func newTestServer(t *testing.T, toolset Toolset, allowed []string) *Server {
	t.Helper()
	srv, err := NewServer(toolset, allowed, zap.NewNop())
	if err != nil {
		t.Fatalf("构造服务器失败：%v", err)
	}
	return srv
}

type testResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

func runLines(t *testing.T, srv *Server, lines ...string) []testResponse {
	t.Helper()
	var out bytes.Buffer
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	if err := srv.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("Serve 失败：%v", err)
	}
	var responses []testResponse
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var resp testResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("响应不是合法 JSON：%s：%v", line, err)
		}
		responses = append(responses, resp)
	}
	return responses
}

func decodeResult[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("解析 result 失败：%v（%s）", err, raw)
	}
	return v
}

// TestInitializeAndList 覆盖 initialize / initialized 通知 / tools/list。
func TestInitializeAndList(t *testing.T) {
	toolset := &fakeToolset{names: []string{"memory_search", "history", "now"}}
	srv := newTestServer(t, toolset, nil)
	responses := runLines(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(responses) != 2 {
		t.Fatalf("notifications/initialized 不应有响应，实际 %d 条", len(responses))
	}
	if string(responses[0].ID) != "1" || responses[0].Error != nil {
		t.Fatalf("initialize 响应错误：%+v", responses[0])
	}
	init := decodeResult[struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools map[string]any `json:"tools"`
		} `json:"capabilities"`
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}](t, responses[0].Result)
	if init.ProtocolVersion != ProtocolVersion || init.ServerInfo.Name != ServerName || init.ServerInfo.Version != ServerVersion {
		t.Fatalf("initialize 返回不符：%+v", init)
	}
	if init.Capabilities.Tools == nil {
		t.Fatal("initialize 未声明 tools 能力")
	}

	list := decodeResult[struct {
		Tools []Tool `json:"tools"`
	}](t, responses[1].Result)
	if len(list.Tools) != 3 {
		t.Fatalf("工具数量不符：%d", len(list.Tools))
	}
	byName := map[string]Tool{}
	for _, tool := range list.Tools {
		byName[tool.Name] = tool
		if tool.Description == "" || tool.InputSchema.Type != "object" {
			t.Fatalf("工具元数据不完整：%+v", tool)
		}
	}
	limit := byName["history"].InputSchema.Properties["limit"]
	if limit.Type != "integer" || limit.Minimum == nil || *limit.Minimum != 1 || limit.Maximum == nil || *limit.Maximum != 20 {
		t.Fatalf("history.limit schema 不符：%+v", limit)
	}
	if _, ok := byName["memory_search"].InputSchema.Properties["query"]; !ok {
		t.Fatal("memory_search 缺少 query 参数")
	}
}

// TestToolsCallForwards 覆盖 query/JSON/忽略参数三种适配。
func TestToolsCallForwards(t *testing.T) {
	toolset := &fakeToolset{
		names: []string{"memory_search", "history", "now"},
		out:   map[string]string{"memory_search": "记忆召回", "history": "最近弹幕", "now": "2026-11-05 12:00:00"},
	}
	srv := newTestServer(t, toolset, nil)
	responses := runLines(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_search","arguments":{"query":"晚饭"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"history","arguments":{"limit":5}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"now"}}`,
	)
	if len(responses) != 3 {
		t.Fatalf("响应数量不符：%d", len(responses))
	}
	wantArgs := []string{"晚饭", `{"limit":5}`, ""}
	for i, resp := range responses {
		if resp.Error != nil {
			t.Fatalf("第 %d 条返回错误：%+v", i+1, resp.Error)
		}
		result := decodeResult[struct {
			Content []content `json:"content"`
			IsError bool      `json:"isError"`
		}](t, resp.Result)
		if result.IsError || len(result.Content) != 1 {
			t.Fatalf("第 %d 条结果不符：%+v", i+1, result)
		}
		if toolset.calls[i].args != wantArgs[i] {
			t.Fatalf("第 %d 次转发参数 = %q，期望 %q", i+1, toolset.calls[i].args, wantArgs[i])
		}
	}
}

// TestToolErrorBecomesIsError 工具自身失败走 result.isError，而不是 JSON-RPC error。
func TestToolErrorBecomesIsError(t *testing.T) {
	toolset := &fakeToolset{
		names: []string{"now"},
		errs:  map[string]error{"now": errors.New("时间服务不可用")},
	}
	srv := newTestServer(t, toolset, nil)
	responses := runLines(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"now"}}`)
	if len(responses) != 1 || responses[0].Error != nil {
		t.Fatalf("不应返回 JSON-RPC 错误：%+v", responses)
	}
	result := decodeResult[struct {
		Content []content `json:"content"`
		IsError bool      `json:"isError"`
	}](t, responses[0].Result)
	if !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "时间服务不可用") {
		t.Fatalf("isError 结果不符：%+v", result)
	}
}

// TestWhitelistFiltersListAndCall 白名单同时作用于 tools/list 与 tools/call。
func TestWhitelistFiltersListAndCall(t *testing.T) {
	toolset := &fakeToolset{names: []string{"memory_search", "history", "now"}}
	srv := newTestServer(t, toolset, []string{"now"})
	responses := runLines(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"history"}}`,
	)
	list := decodeResult[struct {
		Tools []Tool `json:"tools"`
	}](t, responses[0].Result)
	if len(list.Tools) != 1 || list.Tools[0].Name != "now" {
		t.Fatalf("白名单过滤失败：%+v", list.Tools)
	}
	if responses[1].Error == nil || responses[1].Error.Code != codeInvalidParams {
		t.Fatalf("未授权调用应返回 -32602：%+v", responses[1])
	}
}

// TestNotificationsDoNotExecute 无 id 的 tools/call 不执行也不响应。
func TestNotificationsDoNotExecute(t *testing.T) {
	toolset := &fakeToolset{names: []string{"now"}}
	srv := newTestServer(t, toolset, nil)
	responses := runLines(t, srv, `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"now"}}`)
	if len(responses) != 0 || len(toolset.calls) != 0 {
		t.Fatalf("通知被误执行/误响应：responses=%d calls=%d", len(responses), len(toolset.calls))
	}
}

// TestProtocolErrors 覆盖解析错误、非法请求、未知方法、非法参数。
func TestProtocolErrors(t *testing.T) {
	toolset := &fakeToolset{names: []string{"now"}}
	srv := newTestServer(t, toolset, nil)
	responses := runLines(t, srv,
		`{not json`,
		`{"jsonrpc":"1.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":2,"method":"unknown/method"}`,
		`{"jsonrpc":"2.0","id":{"bad":1},"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"now","arguments":[]}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"now","arguments":"x"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"arguments":{}}}`,
	)
	wantCodes := []int{codeParseError, codeInvalidRequest, codeMethodNotFound, codeInvalidRequest, codeInvalidParams, codeInvalidParams, codeInvalidParams}
	if len(responses) != len(wantCodes) {
		t.Fatalf("响应数量不符：%d", len(responses))
	}
	for i, code := range wantCodes {
		if responses[i].Error == nil || responses[i].Error.Code != code {
			t.Fatalf("第 %d 条错误码不符：%+v", i+1, responses[i])
		}
	}
	if string(responses[0].ID) != "null" || string(responses[3].ID) != "null" {
		t.Fatalf("解析错误/非法 id 的响应 id 应为 null：%s / %s", responses[0].ID, responses[3].ID)
	}
}

// TestNewServerValidation 校验构造期拒绝空工具集、未注册白名单项与无 schema 工具。
func TestNewServerValidation(t *testing.T) {
	if _, err := NewServer(nil, nil, zap.NewNop()); err == nil {
		t.Fatal("nil 工具集应被拒绝")
	}
	if _, err := NewServer(&fakeToolset{names: []string{"now"}}, []string{"gift"}, zap.NewNop()); err == nil {
		t.Fatal("白名单包含未注册工具应被拒绝")
	}
	if _, err := NewServer(&fakeToolset{names: []string{"future_tool"}}, nil, zap.NewNop()); err == nil {
		t.Fatal("没有登记 schema 的工具应导致无可暴露工具")
	}
	srv, err := NewServer(&fakeToolset{names: []string{"now"}}, nil, zap.NewNop())
	if err != nil || len(srv.Tools()) != 1 {
		t.Fatalf("正常装配失败：%v", err)
	}
}

// TestPingAndUnknownNotification 覆盖 ping 与未知通知的静默忽略。
func TestPingAndUnknownNotification(t *testing.T) {
	srv := newTestServer(t, &fakeToolset{names: []string{"now"}}, nil)
	responses := runLines(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
	)
	if len(responses) != 1 || responses[0].Error != nil {
		t.Fatalf("ping/未知通知处理不符：%+v", responses)
	}
}
