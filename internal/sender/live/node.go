package live

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/sender"
)

// NodeOptions只接收显式部署配置；凭据不参与JSON序列化。
type NodeOptions struct {
	NodePath   string
	ScriptPath string
	Cookie     string `json:"-"`
	CookieFile string `json:"-"`
	Timeout    time.Duration
}

func DefaultNodeOptions() NodeOptions {
	return NodeOptions{NodePath: "node", ScriptPath: "tools/bili-whale/danmaku.mjs", Timeout: 35 * time.Second}
}

type NodeTransport struct {
	options NodeOptions
	command func(context.Context, string) *exec.Cmd
}

// NewNodeTransport只验证静态配置，不读取凭据、启动进程或访问平台。
func NewNodeTransport(o NodeOptions) (*NodeTransport, error) {
	if strings.TrimSpace(o.NodePath) == "" || strings.TrimSpace(o.ScriptPath) == "" || o.Timeout <= 0 || strings.ContainsAny(o.NodePath+o.ScriptPath+o.Cookie+o.CookieFile, "\r\n\x00") {
		return nil, &sender.Error{Kind: sender.KindInput}
	}
	n := &NodeTransport{options: o}
	n.command = n.buildCommand
	return n, nil
}

func (n *NodeTransport) buildCommand(ctx context.Context, action string) *exec.Cmd {
	args := []string{n.options.ScriptPath, "--action", action}
	if action == "send" {
		args = append(args, "--go")
	}
	// #nosec G204 -- Runtime与脚本为可信部署路径；逐参数传递、不经shell，正文与凭据只走管道和显式环境。
	cmd := exec.CommandContext(ctx, n.options.NodePath, args...)
	cmd.Env = []string{}
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "PATHEXT", "TEMP", "TMP"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	if n.options.Cookie != "" {
		cmd.Env = append(cmd.Env, "FISH_BILI_COOKIE="+n.options.Cookie)
	} else if n.options.CookieFile != "" {
		cmd.Env = append(cmd.Env, "FISH_BILI_COOKIE_FILE="+n.options.CookieFile)
	}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

var nodeToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// validReplyName 允许空昵称，但拒绝未修剪、超长、含控制字符或非法UTF-8的展示名。
func validReplyName(name string) bool {
	if name == "" {
		return true
	}
	if !utf8.ValidString(name) || name != strings.TrimSpace(name) || utf8.RuneCountInString(name) > 128 {
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func validNodeAttempt(a sender.Attempt) bool {
	return nodeToken.MatchString(a.ID) && a.EventID != "" && utf8.ValidString(a.EventID) && !strings.ContainsRune(a.EventID, 0) && sender.ValidActor(a.ActorID) && sender.ValidActor(a.RoomID) && strings.TrimSpace(a.Text) != "" && utf8.ValidString(a.Text) && utf8.RuneCountInString(a.Text) <= 40 && !strings.ContainsRune(a.Text, 0) && (a.ReplyMID == "" || sender.ValidActor(a.ReplyMID)) && (a.ReplyName == "" || a.ReplyMID != "") && validReplyName(a.ReplyName)
}

type nodeRequest struct {
	Protocol  int    `json:"protocol"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	RoomID    string `json:"room_id"`
	SelfID    string `json:"self_id"`
	Private   bool   `json:"private"`
	Text      string `json:"text"`
	// 仅发送帧携带；核验无需@目标。
	ReplyMID  string `json:"reply_mid,omitempty"`
	ReplyName string `json:"reply_uname,omitempty"`
}

type nodeVerifyRequest struct {
	nodeRequest
	AttemptID string `json:"attempt_id"`
	Nonce     string `json:"nonce"`
	MsgID     string `json:"msg_id"`
}

type nodeCommit struct {
	Protocol int    `json:"protocol"`
	Type     string `json:"type"`
	sender.Permit
}

type nodeFrame struct {
	Protocol  int             `json:"protocol"`
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	RoomID    string          `json:"room_id"`
	SelfID    string          `json:"self_id"`
	AttemptID string          `json:"attempt_id"`
	Result    json.RawMessage `json:"result"`
}

type nodeSession struct {
	attempt sender.Attempt
	ctx     context.Context
	cancel  context.CancelFunc
	stdin   *os.File
	stdout  *os.File
	reader  *bufio.Reader
	done    chan struct{}
	close   sync.Once
	mu      sync.Mutex
	posted  bool
	written int
	read    int
}

func nodeFailure(kind sender.Kind, causes ...error) error {
	return &sender.Error{Kind: kind, Cause: errors.Join(causes...)}
}

// start自持管道；Wait不会提前关闭仍待读取的stdout。取消同时关闭管道以中断阻塞读写。
func (n *NodeTransport) start(parent context.Context, a sender.Attempt, action string) (*nodeSession, error) {
	if parent == nil || !validNodeAttempt(a) {
		return nil, nodeFailure(sender.KindInput)
	}
	if parent.Err() != nil {
		return nil, nodeFailure(sender.KindRetry, parent.Err())
	}
	ctx, cancel := context.WithTimeout(parent, n.options.Timeout)
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		cancel()
		return nil, nodeFailure(sender.KindRetry, err)
	}
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		cancel()
		_ = inRead.Close()
		_ = inWrite.Close()
		return nil, nodeFailure(sender.KindRetry, err)
	}
	cmd := n.command(ctx, action)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inRead, outWrite, io.Discard
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Start()
	_ = inRead.Close()
	_ = outWrite.Close()
	if err != nil {
		ctxErr := ctx.Err()
		cancel()
		_ = inWrite.Close()
		_ = outRead.Close()
		return nil, nodeFailure(sender.KindRetry, err, ctxErr)
	}
	s := &nodeSession{attempt: a, ctx: ctx, cancel: cancel, stdin: inWrite, stdout: outRead, reader: bufio.NewReaderSize(outRead, 64*1024+1), done: make(chan struct{})}
	context.AfterFunc(ctx, func() {
		_ = inWrite.Close()
		_ = outRead.Close()
	})
	go func() {
		// 原始退出错误可能含路径或运行时信息，不跨驱动边界传播。
		_ = cmd.Wait()
		close(s.done)
		// 正常退出时让读端消费已有输出；Close或总超时负责关闭父端。
	}()
	return s, nil
}

func requestFor(a sender.Attempt, kind string) nodeRequest {
	request := nodeRequest{Protocol: 1, Type: kind, RequestID: a.ID, RoomID: a.RoomID, SelfID: a.ActorID, Text: a.Text}
	if kind == "prepare" {
		request.ReplyMID, request.ReplyName = a.ReplyMID, a.ReplyName
	}
	return request
}

func (n *NodeTransport) Open(ctx context.Context, a sender.Attempt) (Session, error) {
	s, err := n.start(ctx, a, "send")
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = s.Close()
		}
	}()
	if err := s.writeFrame(requestFor(a, "prepare")); err != nil {
		return nil, err
	}
	f, err := s.readFrame()
	if err != nil || f.RequestID != a.ID {
		return nil, nodeFailure(sender.KindRetry, err, s.ctx.Err())
	}
	if f.Type == "result" {
		v, err := decodeNodeVerdict(f.Result, sender.OriginPost)
		if err == nil && f.AttemptID == "" && (v.ErrorKind == "auth" || v.ErrorKind == "credentials") {
			return nil, nodeFailure(sender.KindAuth)
		}
		return nil, nodeFailure(sender.KindRetry)
	}
	if f.Type != "ready" || f.RoomID != a.RoomID || f.SelfID != a.ActorID || s.ctx.Err() != nil {
		return nil, nodeFailure(sender.KindRetry)
	}
	failed = false
	return s, nil
}

func (s *nodeSession) writeFrame(value any) error {
	data, err := json.Marshal(value)
	if err != nil || s.written+len(data)+1 > 16*1024 {
		return nodeFailure(sender.KindRetry)
	}
	data = append(data, '\n')
	written, err := s.stdin.Write(data)
	s.written += written
	if err != nil || written != len(data) {
		return nodeFailure(sender.KindRetry, err, s.ctx.Err())
	}
	return nil
}

// nodeObject拒绝重复键、null、尾随JSON以及缺失或新增字段。
func nodeObject(data []byte, fields ...string) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nodeFailure(sender.KindRetry, err)
	}
	object := make(map[string]json.RawMessage, len(fields))
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok {
			return nodeFailure(sender.KindRetry, err)
		}
		if _, duplicate := object[name]; duplicate {
			return nodeFailure(sender.KindRetry)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nodeFailure(sender.KindRetry, err)
		}
		object[name] = raw
	}
	if last, err := decoder.Token(); err != nil || last != json.Delim('}') {
		return nodeFailure(sender.KindRetry, err)
	}
	if decoder.Decode(new(any)) != io.EOF || len(object) != len(fields) {
		return nodeFailure(sender.KindRetry)
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return nodeFailure(sender.KindRetry)
		}
	}
	return nil
}

func (s *nodeSession) readFrame() (nodeFrame, error) {
	line, err := s.reader.ReadSlice('\n')
	s.read += len(line)
	var frame nodeFrame
	if err != nil || s.read > 64*1024 || !utf8.Valid(line) {
		return frame, nodeFailure(sender.KindRetry, err, s.ctx.Err())
	}
	if err := json.Unmarshal(line, &frame); err != nil || frame.Protocol != 1 {
		return frame, nodeFailure(sender.KindRetry, err)
	}
	fields := []string{"protocol", "type", "request_id"}
	switch frame.Type {
	case "ready":
		fields = append(fields, "room_id", "self_id")
	case "result":
		fields = append(fields, "attempt_id", "result")
	default:
		return frame, nodeFailure(sender.KindRetry)
	}
	if err := nodeObject(line, fields...); err != nil {
		return frame, err
	}
	return frame, nil
}

func decodeNodeVerdict(data []byte, origin string) (sender.Verdict, error) {
	var verdict sender.Verdict
	if err := nodeObject(data, "outcome", "msg_id", "verified", "error_kind", "evidence"); err != nil {
		return verdict, err
	}
	if err := json.Unmarshal(data, &verdict); err != nil {
		return verdict, nodeFailure(sender.KindRetry, err)
	}
	verdict.Origin = origin
	if err := verdict.Validate(); err != nil {
		return sender.Verdict{}, nodeFailure(sender.KindRetry, err)
	}
	return verdict, nil
}

// Post只消耗一次调用；commit之后的任何管道故障都不能证明平台未收到。
func (s *nodeSession) Post(ctx context.Context, permit sender.Permit) (sender.Verdict, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.posted {
		return sender.Verdict{}, nodeFailure(sender.KindInput)
	}
	s.posted = true
	notPosted := func(code string) (sender.Verdict, error) {
		return sender.Verdict{Outcome: sender.OutcomeNotPosted, ErrorKind: code, Evidence: "none", Origin: sender.OriginPost}, nil
	}
	if ctx == nil || ctx.Err() != nil || s.ctx.Err() != nil {
		return notPosted("cancelled")
	}
	if permit.AttemptID != s.attempt.ID || permit.RequestID != s.attempt.ID || !sender.ValidActor(permit.Nonce) || (s.attempt.Nonce != "" && permit.Nonce != s.attempt.Nonce) || !permit.ExpiresAt.After(time.Now()) {
		return notPosted("permit")
	}
	stop := context.AfterFunc(ctx, s.cancel)
	defer stop()
	if err := s.writeFrame(nodeCommit{Protocol: 1, Type: "commit", Permit: permit}); err != nil {
		return sender.Verdict{}, nodeFailure(sender.KindRetry, err, ctx.Err())
	}
	f, err := s.readFrame()
	if err != nil || f.Type != "result" || f.RequestID != s.attempt.ID || f.AttemptID != s.attempt.ID {
		return sender.Verdict{}, nodeFailure(sender.KindRetry, err, ctx.Err(), s.ctx.Err())
	}
	v, err := decodeNodeVerdict(f.Result, sender.OriginPost)
	if err != nil || v.Outcome == sender.OutcomeNotPosted {
		// commit已写入时，子进程的未发分类不能推翻父进程持久化的不确定边界。
		return sender.Verdict{}, nodeFailure(sender.KindRetry, err)
	}
	return v, nil
}

func (s *nodeSession) Close() error {
	s.close.Do(func() {
		s.cancel()
		_ = s.stdin.Close()
		_ = s.stdout.Close()
		<-s.done
	})
	return nil
}

// Verify启动独立只读进程，不携带发送开关，也不产生未投递证据。
func (n *NodeTransport) Verify(ctx context.Context, a sender.Attempt) (sender.Verdict, error) {
	unknown := sender.Verdict{Outcome: sender.OutcomeUnknown, ErrorKind: "protocol", Evidence: "none", Origin: sender.OriginVerify}
	if !sender.ValidActor(a.Nonce) && a.MsgID == "" {
		return unknown, nodeFailure(sender.KindInput)
	}
	s, err := n.start(ctx, a, "verify")
	if err != nil {
		return unknown, err
	}
	defer func() { _ = s.Close() }()
	if err := s.writeFrame(nodeVerifyRequest{nodeRequest: requestFor(a, "verify"), AttemptID: a.ID, Nonce: a.Nonce, MsgID: a.MsgID}); err != nil {
		return unknown, err
	}
	f, err := s.readFrame()
	if err != nil || f.Type != "result" || f.RequestID != a.ID || f.AttemptID != a.ID {
		return unknown, nodeFailure(sender.KindRetry, err, s.ctx.Err())
	}
	v, err := decodeNodeVerdict(f.Result, sender.OriginVerify)
	if err != nil {
		return unknown, err
	}
	if !v.Verified {
		v.Outcome = sender.OutcomeUnknown
	}
	return v, nil
}

var _ Transport = (*NodeTransport)(nil)
