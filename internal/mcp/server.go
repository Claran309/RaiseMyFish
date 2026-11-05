// Package mcp 实现最小 MCP stdio 服务器：JSON-RPC 2.0 编解码 + 工具转发。
//
// 边界：
//   - stdout 只写协议响应；日志由调用方注入的 zap 输出到 stderr/文件；
//   - 只实现 tools 能力（initialize / ping / tools/list / tools/call），不实现 resources/prompts；
//   - 工具执行错误按 MCP 惯例放进 result.content 并置 isError=true；
//     未知/未授权工具走 JSON-RPC -32602，错误信息只含工具名。
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
)

const (
	// ProtocolVersion 是本服务器实现的 MCP 协议版本。
	ProtocolVersion = "2024-11-05"
	// ServerName 与 ServerVersion 是 initialize 返回的服务标识。
	ServerName    = "raise-my-fish-mcp"
	ServerVersion = "0.1.0"
	// maxMessageBytes 是单条 JSON-RPC 消息上限，避免异常客户端撑爆内存。
	maxMessageBytes = 4 << 20
)

// JSON-RPC 2.0 标准错误码。
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

var errMessageTooLarge = errors.New("MCP 单条消息超过大小上限")

// Toolset 是服务器可执行的最小工具集；*reasoning.Toolbox 直接满足。
type Toolset interface {
	Names() []string
	Run(ctx context.Context, name, args string) (string, error)
}

// Server 是单连接、顺序处理的 stdio MCP 服务器。
type Server struct {
	toolset Toolset
	tools   []Tool
	visible map[string]struct{}
	log     *zap.Logger
}

// NewServer 构造服务器：可见工具 = 已注册 ∩ 白名单 ∩ 已登记 schema。
//
// 白名单为空表示不限制；白名单里出现未注册的工具会在启动期报错，
// 避免「配置写了但实际没暴露」的静默失败。
func NewServer(toolset Toolset, allowed []string, log *zap.Logger) (*Server, error) {
	if toolset == nil {
		return nil, errors.New("MCP 工具集不能为 nil")
	}
	if log == nil {
		log = zap.NewNop()
	}
	allow := make(map[string]struct{}, len(allowed))
	for _, raw := range allowed {
		name := strings.TrimSpace(raw)
		if name != "" {
			allow[name] = struct{}{}
		}
	}
	registered := make(map[string]struct{})
	for _, name := range toolset.Names() {
		registered[name] = struct{}{}
	}
	if len(allow) > 0 {
		for name := range allow {
			if _, ok := registered[name]; !ok {
				return nil, fmt.Errorf("MCP 工具白名单包含未注册工具：%s", name)
			}
		}
	}

	s := &Server{toolset: toolset, visible: make(map[string]struct{}), log: log}
	for _, name := range toolset.Names() {
		if len(allow) > 0 {
			if _, ok := allow[name]; !ok {
				continue
			}
		}
		spec, ok := toolSpecs[name]
		if !ok {
			// 新工具未登记 schema 时跳过，而不是暴露一个参数不可知的接口。
			log.Warn("MCP 工具未登记参数 schema，已跳过",
				zap.String("action", "mcp_catalog"),
				zap.String("tool", name),
			)
			continue
		}
		s.tools = append(s.tools, Tool{Name: name, Description: spec.Description, InputSchema: spec.Schema})
		s.visible[name] = struct{}{}
	}
	if len(s.tools) == 0 {
		return nil, errors.New("MCP 没有可暴露的工具（检查白名单与已注册工具）")
	}
	return s, nil
}

// Tools 返回将要暴露的工具清单（拷贝）。
func (s *Server) Tools() []Tool { return append([]Tool(nil), s.tools...) }

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    rpcCapabilities `json:"capabilities"`
	ServerInfo      serverInfo      `json:"serverInfo"`
}

type rpcCapabilities struct {
	Tools map[string]any `json:"tools"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type listResult struct {
	Tools []Tool `json:"tools"`
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type callResult struct {
	Content []content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type readResult struct {
	line []byte
	err  error
}

// Serve 逐行读取 JSON-RPC 消息并写回响应，直到 stdin EOF 或 ctx 取消。
//
// 读循环放在独立 goroutine，保证收到信号时即使 stdin 仍打开也能立即退出。
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	if in == nil || out == nil {
		return errors.New("MCP 输入/输出不能为 nil")
	}
	reader := bufio.NewReader(in)
	writer := bufio.NewWriter(out)
	if ctx == nil {
		ctx = context.Background()
	}
	lines := make(chan readResult)
	go func() {
		defer close(lines)
		for {
			line, err := readMessage(reader)
			select {
			case lines <- readResult{line: line, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case result, ok := <-lines:
			if !ok {
				return nil
			}
			if result.err != nil && !errors.Is(result.err, io.EOF) {
				return fmt.Errorf("读取 MCP 消息失败：%w", result.err)
			}
			if line := bytes.TrimSpace(result.line); len(line) > 0 {
				resp, write := s.handle(ctx, line)
				if write {
					if err := writeResponse(writer, resp); err != nil {
						return err
					}
				}
			}
			if errors.Is(result.err, io.EOF) {
				return nil
			}
		}
	}
}

// readMessage 读取一行完整消息；超长直接报错，避免截断后误执行。
func readMessage(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(buf)+len(chunk) > maxMessageBytes {
			return nil, errMessageTooLarge
		}
		buf = append(buf, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return buf, err
	}
}

func writeResponse(w *bufio.Writer, resp response) error {
	payload, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("编码 MCP 响应失败：%w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("写入 MCP 响应失败：%w", err)
	}
	if err := w.WriteByte('\n'); err != nil {
		return fmt.Errorf("写入 MCP 响应失败：%w", err)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("刷新 MCP 响应失败：%w", err)
	}
	return nil
}

// handle 处理一条消息；第二个返回值表示是否需要写回响应。
func (s *Server) handle(ctx context.Context, line []byte) (response, bool) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		s.log.Warn("MCP 请求解析失败", zap.String("action", "mcp_parse"), zap.Error(err))
		return errorResponse(nil, codeParseError, "请求不是合法 JSON"), true
	}
	hasID := len(req.ID) > 0
	if req.JSONRPC != "2.0" || strings.TrimSpace(req.Method) == "" || !validID(req.ID) {
		s.log.Warn("MCP 请求格式无效", zap.String("action", "mcp_parse"), zap.String("reason", "invalid_request"))
		return errorResponse(nil, codeInvalidRequest, "无效的 JSON-RPC 请求"), true
	}

	switch req.Method {
	case "initialize":
		if !hasID {
			return response{}, false
		}
		return response{JSONRPC: "2.0", ID: req.ID, Result: initializeResult{
			ProtocolVersion: ProtocolVersion,
			Capabilities:    rpcCapabilities{Tools: map[string]any{}},
			ServerInfo:      serverInfo{Name: ServerName, Version: ServerVersion},
		}}, true
	case "notifications/initialized":
		if !hasID {
			return response{}, false
		}
		return response{JSONRPC: "2.0", ID: req.ID, Result: struct{}{}}, true
	case "ping":
		if !hasID {
			return response{}, false
		}
		return response{JSONRPC: "2.0", ID: req.ID, Result: struct{}{}}, true
	case "tools/list":
		if !hasID {
			return response{}, false
		}
		return response{JSONRPC: "2.0", ID: req.ID, Result: listResult{Tools: s.tools}}, true
	case "tools/call":
		if !hasID {
			// 没有 id 的消息按通知处理：不回响应，也不执行副作用工具。
			s.log.Warn("MCP 忽略无 id 的 tools/call", zap.String("action", "mcp_tool"), zap.String("reason", "notification"))
			return response{}, false
		}
		return s.call(ctx, req)
	default:
		if !hasID {
			return response{}, false
		}
		return errorResponse(req.ID, codeMethodNotFound, "未知方法："+req.Method), true
	}
}

// call 执行 tools/call；参数错误走 JSON-RPC 错误，工具自身失败走 isError 结果。
func (s *Server) call(ctx context.Context, req request) (response, bool) {
	var params callParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errorResponse(req.ID, codeInvalidParams, "tools/call 参数必须是 JSON 对象"), true
	}
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return errorResponse(req.ID, codeInvalidParams, "缺少工具名"), true
	}
	if _, ok := s.visible[name]; !ok {
		s.log.Warn("MCP 工具调用被拒绝",
			zap.String("action", "mcp_tool"),
			zap.String("tool", name),
			zap.String("result", "denied"),
		)
		return errorResponse(req.ID, codeInvalidParams, "未知工具或未授权："+name), true
	}
	args, err := normalizeArguments(params.Arguments)
	if err != nil {
		return errorResponse(req.ID, codeInvalidParams, err.Error()), true
	}
	legacy, err := encodeArgs(toolSpecs[name].Args, args)
	if err != nil {
		return errorResponse(req.ID, codeInvalidParams, err.Error()), true
	}

	started := time.Now()
	text, runErr := s.toolset.Run(ctx, name, legacy)
	latency := time.Since(started).Milliseconds()
	if runErr != nil {
		s.log.Warn("MCP 工具调用失败",
			zap.String("action", "mcp_tool"),
			zap.String("tool", name),
			zap.String("result", "failed"),
			zap.Int64("latency_ms", latency),
			zap.Error(runErr),
		)
		return response{JSONRPC: "2.0", ID: req.ID, Result: callResult{
			Content: []content{{Type: "text", Text: runErr.Error()}},
			IsError: true,
		}}, true
	}
	s.log.Info("MCP 工具调用完成",
		zap.String("action", "mcp_tool"),
		zap.String("tool", name),
		zap.String("result", "ok"),
		zap.Int64("latency_ms", latency),
		zap.Int("runes", utf8.RuneCountInString(text)),
	)
	return response{JSONRPC: "2.0", ID: req.ID, Result: callResult{
		Content: []content{{Type: "text", Text: text}},
	}}, true
}

// normalizeArguments 把缺省/null 参数规整成空对象，并拒绝非对象参数。
func normalizeArguments(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage("{}"), nil
	}
	if trimmed[0] != '{' {
		return nil, errors.New("arguments 必须是 JSON 对象")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return nil, errors.New("arguments 必须是 JSON 对象")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return nil, errors.New("arguments 必须是 JSON 对象")
	}
	return compact.Bytes(), nil
}

// encodeArgs 按工具登记的模式把 MCP arguments 转成旧 Tool.Run(string) 参数。
func encodeArgs(mode argMode, args json.RawMessage) (string, error) {
	switch mode {
	case argsIgnored:
		return "", nil
	case argsQuery:
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(args, &obj); err != nil {
			return "", errors.New("arguments 必须是 JSON 对象")
		}
		raw, ok := obj["query"]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return "", nil
		}
		var query string
		if err := json.Unmarshal(raw, &query); err != nil {
			return "", errors.New("query 必须是字符串")
		}
		return query, nil
	case argsJSON:
		return string(args), nil
	default:
		return "", errors.New("工具参数模式无效")
	}
}

// validID 只接受 JSON-RPC 允许的 string/number/null id。
func validID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	switch v.(type) {
	case string, float64, nil:
		return true
	default:
		return false
	}
}

func errorResponse(id json.RawMessage, code int, message string) response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}
