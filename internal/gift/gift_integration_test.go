//go:build integration

package gift

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func integrationSender(t *testing.T, mode string, mutate func(*Options)) (*NodeSender, error) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("送礼集成测试要求安装Node：", err)
	}
	script, err := filepath.Abs(filepath.Join("testdata", "node-"+mode+".mjs"))
	if err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions()
	o.NodePath, o.ScriptPath = node, script
	o.DryRun = false
	o.Cookie = "SESSDATA=fixture; bili_jct=fixture; buvid3=fixture"
	o.DailyLimit = 10
	if mutate != nil {
		mutate(&o)
	}
	return New(o)
}

func TestNodeScriptDryRunOffline(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("送礼集成测试要求安装Node：", err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "tools", "bili-whale", "gift.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions()
	o.NodePath, o.ScriptPath = node, script
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Send(context.Background(), "99", "31036", 1)
	if err != nil || !result.Sent || result.GiftID != "31036" || result.Num != 1 {
		t.Fatalf("dry-run结果不符：%+v %v", result, err)
	}
	if s.used != 0 {
		t.Fatal("dry-run不应计数")
	}
}

func TestNodeFixtures(t *testing.T) {
	t.Run("sent", func(t *testing.T) {
		s, err := integrationSender(t, "sent", nil)
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.Send(context.Background(), "99", "31036", 2)
		if err != nil || !result.Sent || result.Num != 2 {
			t.Fatalf("sent帧解析失败：%+v %v", result, err)
		}
	})
	t.Run("noisy-stderr", func(t *testing.T) {
		s, err := integrationSender(t, "noisy", nil)
		if err != nil {
			t.Fatal(err)
		}
		if result, err := s.Send(context.Background(), "99", "31036", 1); err != nil || !result.Sent {
			t.Fatalf("stderr噪声影响结果：%+v %v", result, err)
		}
	})
	for mode, want := range map[string]Kind{"retryable": KindRetryable, "fatal": KindFatal} {
		t.Run(mode, func(t *testing.T) {
			s, err := integrationSender(t, mode, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Send(context.Background(), "99", "31036", 1)
			var typed *Error
			if !errors.As(err, &typed) || typed.Kind != want {
				t.Fatalf("分类错误：%v", err)
			}
			if strings.Contains(err.Error(), "fixture") {
				t.Fatal("错误泄露凭据")
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		s, err := integrationSender(t, "hang", func(o *Options) { o.Timeout = time.Second })
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		_, err = s.Send(context.Background(), "99", "31036", 1)
		if err == nil || !strings.Contains(err.Error(), "超时") {
			t.Fatalf("超时未生效：%v", err)
		}
		if time.Since(started) > 5*time.Second {
			t.Fatal("超时过慢")
		}
		if s.used != 1 {
			t.Fatal("超时必须保留额度")
		}
	})
}
