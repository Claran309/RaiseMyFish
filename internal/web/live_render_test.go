package web

// 本文件用 Node vm 执行 /live 页面的真实内联脚本（最小 DOM/fetch 桩），
// 覆盖「未装配 / 运行中 / 已停止」三态渲染。
//
// 只断言「页面包含某段文字」抓不到 var 提升遮蔽同名函数这类运行时错误：
// 静态检查全绿，浏览器里一打开就 TypeError（D1/N15）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// liveHarness 在 vm 沙箱里加载 page.js，注入最小 DOM/fetch 桩后手动触发 render()。
//
// fetch 故意返回挂起 Promise：页面末尾的 load() 会停在 await，
// 三态由测试通过 state.json 精确控制，避免异步渲染掩盖错误。
const liveHarness = `import vm from "node:vm";
import fs from "node:fs";

const pageJS = fs.readFileSync(new URL("page.js", import.meta.url), "utf8");
const fixture = JSON.parse(fs.readFileSync(new URL("state.json", import.meta.url), "utf8"));

const elements = new Map();
function element(id) {
  if (!elements.has(id)) {
    elements.set(id, { className: "", textContent: "", innerHTML: "", value: "", disabled: false });
  }
  return elements.get(id);
}

const context = {
  document: {
    getElementById: (id) => element(id),
    querySelector: () => ({ content: "test-token" }),
  },
  fetch: () => new Promise(() => {}),
  setInterval: () => 0,
  console,
};
vm.createContext(context);

let error = "";
try {
  vm.runInContext(pageJS, context, { filename: "live.html" });
  context.state = fixture;
  vm.runInContext("render()", context, { filename: "live.html" });
} catch (e) {
  error = String(e);
}

const pick = (id) => {
  const el = element(id);
  return { text: el.textContent, class: el.className };
};
console.log(JSON.stringify({ error, live: pick("live-pill"), watch: pick("watch-pill") }));
`

var liveScriptPattern = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

type livePillView struct {
	Text  string `json:"text"`
	Class string `json:"class"`
}

type liveRenderResult struct {
	Error string       `json:"error"`
	Live  livePillView `json:"live"`
	Watch livePillView `json:"watch"`
}

// TestLivePageRenderStatesWithoutTypeError 用真实页面脚本渲染三态：
// 任何 TypeError 都直接失败，并同时校验两个状态胶囊的文案与样式。
func TestLivePageRenderStatesWithoutTypeError(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("未找到 node，跳过页面脚本执行测试")
	}
	srv := newTestServer(t, &fakeRepo{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/live"))
	if rec.Code != http.StatusOK {
		t.Fatalf("/live 状态码 = %d", rec.Code)
	}
	match := liveScriptPattern.FindStringSubmatch(rec.Body.String())
	if match == nil || strings.TrimSpace(match[1]) == "" {
		t.Fatal("/live 页面没有可执行的内联脚本")
	}
	script := match[1]

	cases := []struct {
		name  string
		state string
		live  livePillView
		watch livePillView
	}{
		{
			name:  "未装配",
			state: `{"live":{"available":false},"watch":{"available":false}}`,
			live:  livePillView{Text: "未装配", Class: "pill bad"},
			watch: livePillView{Text: "未装配", Class: "pill"},
		},
		{
			name:  "运行中",
			state: `{"live":{"available":true,"running":true,"enabled":true},"watch":{"available":true,"running":true,"healthy":true,"enabled":true}}`,
			live:  livePillView{Text: "采集中", Class: "pill ok"},
			watch: livePillView{Text: "运行中", Class: "pill ok"},
		},
		{
			name:  "已停止",
			state: `{"live":{"available":true},"watch":{"available":true,"enabled":false}}`,
			live:  livePillView{Text: "已停止", Class: "pill bad"},
			watch: livePillView{Text: "已停止", Class: "pill"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runLiveRender(t, node, script, tc.state)
			if got.Error != "" {
				t.Fatalf("render 抛出运行时错误：%s", got.Error)
			}
			if got.Live != tc.live {
				t.Errorf("直播采集状态 = %+v，期望 %+v", got.Live, tc.live)
			}
			if got.Watch != tc.watch {
				t.Errorf("看直播状态 = %+v，期望 %+v", got.Watch, tc.watch)
			}
		})
	}
}

func runLiveRender(t *testing.T, node, script, state string) liveRenderResult {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "page.js"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.mjs"), []byte(liveHarness), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "run.mjs")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("node 执行失败：%v\n%s", err, exitErr.Stderr)
		}
		t.Fatalf("node 执行失败：%v", err)
	}
	var got liveRenderResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("解析 node 输出失败：%v，输出：%s", err, out)
	}
	return got
}
