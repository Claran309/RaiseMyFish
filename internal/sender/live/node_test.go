package live

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/sender"
)

func nodeAttempt() sender.Attempt {
	return sender.Attempt{ID: "attempt_1", EventID: "live:123:event-1", RoomID: "123", ActorID: "456", Text: "合成弹幕", Nonce: "1234567890"}
}
func nodePermit() sender.Permit {
	return sender.Permit{AttemptID: "attempt_1", RequestID: "attempt_1", Nonce: "1234567890", ExpiresAt: time.Now().Add(time.Second)}
}
func helperTransport(t *testing.T, mode string) (*NodeTransport, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	o := DefaultNodeOptions()
	o.Cookie = "synthetic-secret"
	o.Timeout = 3 * time.Second
	n, err := NewNodeTransport(o)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "frames.jsonl")
	n.command = func(ctx context.Context, action string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestNodeHelperProcess$", "--", mode, action, path)
		cmd.Env = n.buildCommand(ctx, action).Env
		return cmd
	}
	return n, path
}

func closeNodeTestSession(t *testing.T, session Session) {
	t.Helper()
	if err := session.Close(); err != nil {
		t.Error(err)
	}
}

// 当前Go测试程序充当真正的管道子进程，不依赖Node安装或网络。
func TestNodeHelperProcess(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--" {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	mode, action, path := os.Args[index+1], os.Args[index+2], os.Args[index+3]
	read := bufio.NewScanner(os.Stdin)
	if !read.Scan() {
		os.Exit(21)
	}
	first := append([]byte(nil), read.Bytes()...)
	if err := os.WriteFile(path, append(first, '\n'), 0600); err != nil {
		os.Exit(22)
	}
	var f map[string]any
	if json.Unmarshal(first, &f) != nil {
		os.Exit(23)
	}
	if mode == "hang-open" {
		time.Sleep(time.Minute)
		os.Exit(24)
	}
	if mode == "auth" {
		fmt.Println(`{"protocol":1,"type":"result","request_id":"attempt_1","attempt_id":"","result":{"outcome":"not_posted","msg_id":"","verified":false,"error_kind":"auth","evidence":"none"}}`)
		os.Exit(1)
	}
	if mode == "secret" {
		fmt.Fprintln(os.Stderr, "synthetic-secret")
		fmt.Println("synthetic-secret")
		os.Exit(1)
	}
	if mode == "oversized" {
		fmt.Println(strings.Repeat("s", 65537))
		os.Exit(1)
	}
	if action == "verify" {
		if f["type"] != "verify" || f["attempt_id"] != "attempt_1" || f["nonce"] != "1234567890" || f["private"] != false {
			os.Exit(25)
		}
		outcome, verified, evidence := "accepted", true, "history_nonce"
		if mode == "verify-not-posted" {
			outcome, verified, evidence = "not_posted", false, "none"
		}
		if mode == "wrong-evidence" {
			evidence = "receipt_id"
		}
		fmt.Printf(`{"protocol":1,"type":"result","request_id":"attempt_1","attempt_id":"attempt_1","result":{"outcome":%q,"msg_id":"999","verified":%t,"error_kind":"","evidence":%q}}`+"\n", outcome, verified, evidence)
		os.Exit(0)
	}
	room, self, request := "123", "456", "attempt_1"
	if mode == "room" {
		room = "999"
	}
	if mode == "actor" {
		self = "999"
	}
	if mode == "request" {
		request = "other"
	}
	if mode == "unknown-field" {
		fmt.Println(`{"protocol":1,"type":"ready","request_id":"attempt_1","room_id":"123","self_id":"456","secret":"synthetic-secret"}`)
	} else if mode == "duplicate" {
		fmt.Println(`{"protocol":1,"type":"ready","request_id":"other","request_id":"attempt_1","room_id":"123","self_id":"456"}`)
	} else {
		fmt.Printf(`{"protocol":1,"type":"ready","request_id":%q,"room_id":%q,"self_id":%q}`+"\n", request, room, self)
	}
	if !read.Scan() {
		os.Exit(26)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(29)
	}
	_, writeErr := file.Write(append(append([]byte(nil), read.Bytes()...), '\n'))
	if err := file.Close(); err != nil || writeErr != nil {
		os.Exit(30)
	}
	if mode == "hang-post" {
		time.Sleep(time.Minute)
		os.Exit(27)
	}
	if mode == "eof-post" {
		os.Exit(28)
	}
	if mode == "result-id" {
		request = "other"
	}
	if mode == "result-phase" {
		fmt.Println(`{"protocol":1,"type":"ready","request_id":"attempt_1","room_id":"123","self_id":"456"}`)
		os.Exit(1)
	}
	if mode == "result-not-posted" {
		fmt.Println(`{"protocol":1,"type":"result","request_id":"attempt_1","attempt_id":"attempt_1","result":{"outcome":"not_posted","msg_id":"","verified":false,"error_kind":"permit","evidence":"none"}}`)
		os.Exit(1)
	}
	if mode == "wrong-evidence" {
		fmt.Println(`{"protocol":1,"type":"result","request_id":"attempt_1","attempt_id":"attempt_1","result":{"outcome":"accepted","msg_id":"999","verified":true,"error_kind":"","evidence":"history_id"}}`)
		os.Exit(0)
	}
	fmt.Printf(`{"protocol":1,"type":"result","request_id":%q,"attempt_id":"attempt_1","result":{"outcome":"accepted","msg_id":"999","verified":true,"error_kind":"","evidence":"receipt_id"}}`+"\n", request)
	os.Exit(0)
}

func TestNodeHandshakeAndSinglePost(t *testing.T) {
	n, path := helperTransport(t, "ok")
	s, err := n.Open(context.Background(), nodeAttempt())
	if err != nil {
		t.Fatal(err)
	}
	defer closeNodeTestSession(t, s)
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), "\n") != 1 || !strings.Contains(string(data), `"type":"prepare"`) {
		t.Fatalf("预检前后意外commit：%s", data)
	}
	v, err := s.Post(context.Background(), nodePermit())
	if err != nil || !v.Verified || v.Origin != sender.OriginPost {
		t.Fatalf("结果=%+v，错误=%v", v, err)
	}
	if _, err := s.Post(context.Background(), nodePermit()); err == nil {
		t.Fatal("允许重复Post")
	}
	data, _ = os.ReadFile(path)
	if strings.Count(string(data), `"type":"commit"`) != 1 {
		t.Fatalf("commit次数错误：%s", data)
	}
}

func TestNodeRejectsPreflightMismatchAndSecrets(t *testing.T) {
	for _, mode := range []string{"room", "actor", "request", "unknown-field", "duplicate", "secret", "oversized", "auth"} {
		t.Run(mode, func(t *testing.T) {
			n, path := helperTransport(t, mode)
			_, err := n.Open(context.Background(), nodeAttempt())
			if err == nil {
				t.Fatal("接受了不合法预检")
			}
			if strings.Contains(fmt.Sprintf("%+v", err), "synthetic-secret") {
				t.Fatal("泄漏子进程文本")
			}
			var e *sender.Error
			if mode == "auth" && (!errors.As(err, &e) || e.Kind != sender.KindAuth) {
				t.Fatalf("鉴权分类：%v", err)
			}
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), `"type":"commit"`) {
				t.Fatal("预检失败仍commit")
			}
		})
	}
}

func TestNodeInvalidPermitNeverCommits(t *testing.T) {
	for _, mode := range []string{"expired", "request", "attempt", "nonce", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			n, path := helperTransport(t, "ok")
			s, err := n.Open(context.Background(), nodeAttempt())
			if err != nil {
				t.Fatal(err)
			}
			defer closeNodeTestSession(t, s)
			permit := nodePermit()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "expired":
				permit.ExpiresAt = time.Now().Add(-time.Second)
			case "request":
				permit.RequestID = "other"
			case "attempt":
				permit.AttemptID = "other"
			case "nonce":
				permit.Nonce = "0"
			case "cancelled":
				cancel()
			}
			v, err := s.Post(ctx, permit)
			if err != nil || v.Outcome != sender.OutcomeNotPosted {
				t.Fatalf("未提交结果=%+v，错误=%v", v, err)
			}
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), `"type":"commit"`) {
				t.Fatal("无效许可仍commit")
			}
		})
	}
}

func TestNodePostMissingOrInvalidResultIsUncertain(t *testing.T) {
	for _, mode := range []string{"eof-post", "result-id", "result-phase", "result-not-posted"} {
		t.Run(mode, func(t *testing.T) {
			n, _ := helperTransport(t, mode)
			s, err := n.Open(context.Background(), nodeAttempt())
			if err != nil {
				t.Fatal(err)
			}
			defer closeNodeTestSession(t, s)
			v, err := s.Post(context.Background(), nodePermit())
			if err == nil || v.Outcome == sender.OutcomeNotPosted {
				t.Fatalf("缺失结果被判未发：%+v %v", v, err)
			}
		})
	}
}

func TestNodeCancellationInterruptsReadAndReaps(t *testing.T) {
	for _, mode := range []string{"hang-open", "hang-post"} {
		t.Run(mode, func(t *testing.T) {
			n, _ := helperTransport(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			started := time.Now()
			if mode == "hang-open" {
				if _, err := n.Open(ctx, nodeAttempt()); err == nil {
					t.Fatal("未取消Open")
				}
			} else {
				s, err := n.Open(context.Background(), nodeAttempt())
				if err != nil {
					t.Fatal(err)
				}
				// 单独的Post上下文从ready后计时，避免慢启动将用例变成提交前取消。
				postCtx, postCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
				defer postCancel()
				v, err := s.Post(postCtx, nodePermit())
				if err == nil || v.Outcome == sender.OutcomeNotPosted {
					t.Fatalf("提交后取消结果=%+v %v", v, err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if time.Since(started) > 2*time.Second {
				t.Fatal("取消未及时回收")
			}
		})
	}
}

func TestNodeVerifyOnlyReadsAndNeverProvesNotPosted(t *testing.T) {
	for _, mode := range []string{"ok", "verify-not-posted"} {
		t.Run(mode, func(t *testing.T) {
			n, path := helperTransport(t, mode)
			v, err := n.Verify(context.Background(), nodeAttempt())
			if err != nil {
				t.Fatal(err)
			}
			if v.Origin != sender.OriginVerify || v.Outcome == sender.OutcomeNotPosted {
				t.Fatalf("非法核验证据：%+v", v)
			}
			if mode == "ok" && !v.Verified {
				t.Fatalf("丢失核验证据：%+v", v)
			}
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), "commit") || strings.Contains(string(data), "prepare") {
				t.Fatal("核验触发发送握手")
			}
		})
	}
}

func TestNodeConstructorAndEnvironmentIsolation(t *testing.T) {
	o := DefaultNodeOptions()
	o.CookieFile = filepath.Join(t.TempDir(), "nonexistent")
	n, err := NewNodeTransport(o)
	if err != nil {
		t.Fatalf("构造读取了凭据文件：%v", err)
	}
	for _, key := range []string{"NODE_OPTIONS", "FISH_DB_DSN", "FISH_LLM_KEY", "FISH_BILI_COOKIE"} {
		t.Setenv(key, "synthetic-secret")
	}
	for _, action := range []string{"send", "verify"} {
		cmd := n.buildCommand(context.Background(), action)
		joined := strings.Join(cmd.Env, "\n")
		for _, key := range []string{"NODE_OPTIONS=", "FISH_DB_DSN=", "FISH_LLM_KEY=", "FISH_BILI_COOKIE="} {
			if strings.Contains(joined, key) {
				t.Fatalf("继承秘密环境：%s", key)
			}
		}
		if action == "verify" && strings.Contains(strings.Join(cmd.Args, " "), "--go") {
			t.Fatal("核验带发送开关")
		}
		if cmd.WaitDelay != 2*time.Second {
			t.Fatal("缺少WaitDelay")
		}
		if strings.Contains(strings.Join(cmd.Args, " "), "synthetic-secret") || strings.Contains(strings.Join(cmd.Args, " "), nodeAttempt().Text) {
			t.Fatal("命令行含秘密或正文")
		}
	}
	o.Cookie = "synthetic-secret"
	serialized, err := json.Marshal(o)
	if err != nil || strings.Contains(string(serialized), "synthetic-secret") || strings.Contains(string(serialized), "nonexistent") {
		t.Fatal("凭据配置参与JSON序列化")
	}
	for _, bad := range []NodeOptions{{}, {NodePath: "node", ScriptPath: "x", Timeout: -time.Second}} {
		if _, err := NewNodeTransport(bad); err == nil {
			t.Fatal("接受非法配置")
		}
	}
}

func TestNodeRejectsEvidenceFromWrongPhase(t *testing.T) {
	n, _ := helperTransport(t, "wrong-evidence")
	s, err := n.Open(context.Background(), nodeAttempt())
	if err != nil {
		t.Fatal(err)
	}
	defer closeNodeTestSession(t, s)
	if _, err := s.Post(context.Background(), nodePermit()); err == nil {
		t.Fatal("POST接受了历史证据")
	}
	if _, err := n.Verify(context.Background(), nodeAttempt()); err == nil {
		t.Fatal("核验接受了投递回执")
	}
}
