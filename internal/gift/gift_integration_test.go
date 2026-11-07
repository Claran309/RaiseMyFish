//go:build integration

package gift

import (
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

// TestT26NodePostResponseClassification 走真实 gift.mjs 分类逻辑，再经 Go 封装与 FileQuota 验证额度。
func TestT26NodePostResponseClassification(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("送礼集成测试要求安装Node：", err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "tools", "bili-whale", "gift.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	rawModule, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	writeScript := func(t *testing.T, dir, name, post string) string {
		t.Helper()
		rawPost, err := json.Marshal(post)
		if err != nil {
			t.Fatal(err)
		}
		source := fmt.Sprintf(`import { pathToFileURL } from 'node:url'
const { runGift } = await import(pathToFileURL(%s).href)
const post = JSON.parse(%s)
const reply = body => ({ ok: true, status: 200, text: async () => JSON.stringify(body) })
const frame = await runGift(['--room','99','--gift-id','31036','--num','1','--go'], {
  fetch: async url => {
    if (url.includes('/Room/get_info')) return reply({ code: 0, data: { room_id: 99, uid: 8, short_id: 0 } })
    if (url.includes('/giftConfig')) return reply({ code: 0, data: { list: [{ id: 31036, price: 100, bag_gift: 0 }] } })
    if (url.includes('/nav')) return reply({ code: 0, data: { isLogin: true, mid: 8 } })
    return reply(post)
  },
})
process.stdout.write(JSON.stringify(frame) + '\n')
`, rawModule, rawPost)
		path := filepath.Join(dir, name+".mjs")
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	fixed := time.Date(2026, 11, 7, 12, 0, 0, 0, time.Local)
	day := fixed.Format("2006-01-02")
	cases := []struct {
		name      string
		post      string
		sent      bool
		kind      Kind
		uncertain bool
		pending   bool
		used      int
	}{
		{"缺code", `{}`, false, KindUncertain, true, true, 1},
		{"code为null", `{"code":null}`, false, KindUncertain, true, true, 1},
		{"code为字符串0", `{"code":"0"}`, false, KindUncertain, true, true, 1},
		{"code为字符串10024", `{"code":"10024"}`, false, KindUncertain, true, true, 1},
		{"未知整数code", `{"code":999999}`, false, KindUncertain, true, true, 1},
		{"服务器错误code=-500", `{"code":-500}`, false, KindUncertain, true, true, 1},
		{"正常拒绝code=-400", `{"code":-400}`, false, KindFatal, false, false, 0},
		{"限流拒绝code=10024", `{"code":10024}`, false, KindFatal, false, false, 0},
		{"正常成功code=0", `{"code":0}`, true, "", false, false, 1},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			dir := t.TempDir()
			ledgerPath := filepath.Join(dir, "gift-ledger.json")
			quota, err := NewFileQuota(ledgerPath)
			if err != nil {
				t.Fatal(err)
			}
			o := DefaultOptions()
			o.NodePath = node
			o.ScriptPath = writeScript(t, dir, "case", item.post)
			o.DryRun = false
			o.Cookie = "SESSDATA=fixture; bili_jct=fixture; buvid3=fixture"
			o.DailyLimit = 1
			o.Quota = quota
			o.Now = func() time.Time { return fixed }
			s, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			result, sendErr := s.Send(context.Background(), "99", "31036", 1)
			if item.sent {
				if sendErr != nil || !result.Sent || result.PendingReview {
					t.Fatalf("成功分类错误：%+v %v", result, sendErr)
				}
			} else {
				var typed *Error
				if !errors.As(sendErr, &typed) || typed.Kind != item.kind || typed.Uncertain != item.uncertain || result.PendingReview != item.pending {
					t.Fatalf("失败分类错误：%+v %v", result, sendErr)
				}
			}
			if used, readErr := quota.Used(day); readErr != nil || used != item.used {
				t.Fatalf("额度结果错误：used=%d want=%d %v", used, item.used, readErr)
			}
			if !item.pending {
				return
			}
			// 结果未知后重建 sender（复用同一账本文件）仍必须受每日额度约束。
			rebuiltQuota, err := NewFileQuota(ledgerPath)
			if err != nil {
				t.Fatal(err)
			}
			rebuiltOptions := o
			rebuiltOptions.Quota = rebuiltQuota
			rebuiltOptions.ScriptPath = writeScript(t, dir, "success", `{"code":0}`)
			rebuilt, err := New(rebuiltOptions)
			if err != nil {
				t.Fatal(err)
			}
			result, sendErr = rebuilt.Send(context.Background(), "99", "31036", 1)
			if sendErr == nil || !strings.Contains(result.Error, "每日限额") {
				t.Fatalf("重建sender后未受额度约束：%+v %v", result, sendErr)
			}
		})
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
