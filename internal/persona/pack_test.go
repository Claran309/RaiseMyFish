package persona

// 本文件测 Load 与四个纯访问函数，对应 contracts/persona.md（P0-PACK-1）§5 验收项。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// realPackDir 是仓库里已验收的真实人格包。
// 直接打真实数据：加载器的首要职责就是能读懂 dafeiyu，造假包测不到真问题。
const realPackDir = "../../personas/dafeiyu"

// minMeta 是最小可用元信息（只声明 live）。
const minMeta = "name: mini\nversion: v1\nchannels: [live]\n"

// newPackDir 在临时目录写一个最小可用人格包（只声明 live），返回目录。
//
// 默认内容故意极短（1~2 个字符）：用来证明加载器**没有**最小篇幅门槛（契约 §5）。
// overrides 里给了同名键就覆盖；写成空串即得到空文件（用于空文件用例）。
// 需要测别的 meta 写法时直接测 decodeMeta，不必让这里再开一个参数。
func newPackDir(t *testing.T, overrides map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	files := map[string]string{
		fileMeta:    minMeta,
		fileSoul:    "鱼",
		fileRules:   "哼",
		fileSamples: "？",
		fileContext: "## live\n短。\n",
	}
	for name, content := range overrides {
		files[name] = content
	}

	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("写入测试文件 %s 失败：%v", name, err)
		}
	}
	return dir
}

// TestLoadRealPack 加载真实 dafeiyu 包：五份文件齐、五个场景都在。
func TestLoadRealPack(t *testing.T) {
	p, err := Load(context.Background(), realPackDir)
	if err != nil {
		t.Fatalf("加载真实人格包失败：%v", err)
	}

	if p.Meta.Name != "dafeiyu" {
		t.Errorf("meta.name = %q，期望 dafeiyu", p.Meta.Name)
	}
	if p.Meta.Version != "v1.1" {
		t.Errorf("meta.version = %q，期望安全单句修订版 v1.1", p.Meta.Version)
	}
	if !filepath.IsAbs(p.Dir) {
		t.Errorf("Pack.Dir 不是绝对路径：%q", p.Dir)
	}
	if p.Dir != filepath.Clean(p.Dir) {
		t.Errorf("Pack.Dir 未清理：%q", p.Dir)
	}
	if len(p.Contexts) != len(ContextKeys()) {
		t.Errorf("场景段数 = %d，期望 %d", len(p.Contexts), len(ContextKeys()))
	}
	if !strings.Contains(p.Rules, "「XX 是谁」不是在问本鱼") {
		t.Error("rules 缺少认人规则：XX 是谁 ≠ 你是谁")
	}

	for _, ch := range []string{"live", "comment", "dm", "qq"} {
		for _, priv := range []bool{false, true} {
			body, err := p.ContextFor(ch, priv)
			if err != nil {
				t.Errorf("ContextFor(%q, %v) 报错：%v", ch, priv, err)
				continue
			}
			if strings.TrimSpace(body) == "" {
				t.Errorf("ContextFor(%q, %v) 返回空正文", ch, priv)
			}
		}
	}
}

// TestLoadMinimalLivePack 证明两件事：
//  1. 只声明 live 的最小包合法；
//  2. 极短（1~2 字）的 soul/rules/samples/context 也能加载，没有篇幅门槛；
//  3. 不要求 eval/ 目录存在（回归文件属 0.7a/0.7c）。
func TestLoadMinimalLivePack(t *testing.T) {
	dir := newPackDir(t, nil)

	if _, err := os.Stat(filepath.Join(dir, "eval")); !os.IsNotExist(err) {
		t.Fatalf("测试前提不成立：不该有 eval 目录")
	}

	p, err := Load(context.Background(), dir)
	if err != nil {
		t.Fatalf("最小包应可加载，实际报错：%v", err)
	}
	if len(p.Contexts) != 1 {
		t.Errorf("最小包场景数 = %d，期望 1", len(p.Contexts))
	}
	if _, err := p.ContextFor("dm", true); err == nil {
		t.Error("最小包没有 dm 场景，ContextFor 应报错而不是回退")
	}
}

// TestLoadMissingFile 缺文件必须报错。
func TestLoadMissingFile(t *testing.T) {
	for _, name := range []string{fileMeta, fileSoul, fileRules, fileSamples, fileContext} {
		t.Run(name, func(t *testing.T) {
			dir := newPackDir(t, nil)
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				t.Fatalf("删除 %s 失败：%v", name, err)
			}
			if _, err := Load(context.Background(), dir); err == nil {
				t.Errorf("缺少 %s 时应报错", name)
			}
		})
	}
}

// TestLoadEmptyFile 空文件必须报错（meta 空 = 无可解码文档，其余为空正文）。
func TestLoadEmptyFile(t *testing.T) {
	for _, name := range []string{fileMeta, fileSoul, fileRules, fileSamples, fileContext} {
		t.Run(name, func(t *testing.T) {
			dir := newPackDir(t, map[string]string{name: ""})
			if _, err := Load(context.Background(), dir); err == nil {
				t.Errorf("%s 为空时应报错", name)
			}
		})
	}
}

// TestLoadInvalidUTF8 非法 UTF-8 必须报错。
func TestLoadInvalidUTF8(t *testing.T) {
	dir := newPackDir(t, nil)
	bad := []byte{0xff, 0xfe, 0xfd}
	if err := os.WriteFile(filepath.Join(dir, fileSoul), bad, 0o600); err != nil {
		t.Fatalf("写入非法 UTF-8 失败：%v", err)
	}
	if _, err := Load(context.Background(), dir); err == nil {
		t.Error("非法 UTF-8 应报错")
	}
}

// TestLoadOversizeFile 超过 1 MiB 的文件必须报错（I/O 边界，不是篇幅门槛）。
func TestLoadOversizeFile(t *testing.T) {
	dir := newPackDir(t, nil)
	big := strings.Repeat("鱼", maxFileBytes) // 每个汉字 3 字节，必然超限
	if err := os.WriteFile(filepath.Join(dir, fileRules), []byte(big), 0o600); err != nil {
		t.Fatalf("写入大文件失败：%v", err)
	}
	if _, err := Load(context.Background(), dir); err == nil {
		t.Error("超过 1 MiB 的文件应报错")
	}
}

// TestLoadNonRegularFile 目录等非常规文件必须报错。
func TestLoadNonRegularFile(t *testing.T) {
	dir := newPackDir(t, nil)
	if err := os.Remove(filepath.Join(dir, fileSoul)); err != nil {
		t.Fatalf("删除 soul.md 失败：%v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, fileSoul), 0o700); err != nil {
		t.Fatalf("创建同名目录失败：%v", err)
	}
	if _, err := Load(context.Background(), dir); err == nil {
		t.Error("同名目录应报错（非常规文件）")
	}
}

// TestLoadCanceledContext ctx 已取消时必须返回 context.Canceled，且能被 errors.Is 识别。
func TestLoadCanceledContext(t *testing.T) {
	dir := newPackDir(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Load(ctx, dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，实际：%v", err)
	}
}

// TestLoadDeadlineExceeded 超时同理，必须保留 DeadlineExceeded。
func TestLoadDeadlineExceeded(t *testing.T) {
	dir := newPackDir(t, nil)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()

	_, err := Load(ctx, dir)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("期望 context.DeadlineExceeded，实际：%v", err)
	}
}

// TestLoadBOMAndCRLF 允许一个 BOM 与 CRLF，返回正文去 BOM 并统一 LF。
func TestLoadBOMAndCRLF(t *testing.T) {
	dir := newPackDir(t, map[string]string{
		fileSoul: "\ufeff鱼\r\n第二行\r\n",
	})

	p, err := Load(context.Background(), dir)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	want := "鱼\n第二行\n"
	if p.Soul != want {
		t.Errorf("Soul = %q，期望 %q（去 BOM + 统一 LF）", p.Soul, want)
	}
}

// TestLoadZeroValueOnError 任何错误都只返回零值 Pack，不给半成品。
func TestLoadZeroValueOnError(t *testing.T) {
	dir := newPackDir(t, map[string]string{fileRules: ""})

	p, err := Load(context.Background(), dir)
	if err == nil {
		t.Fatal("期望报错")
	}
	if !reflect.DeepEqual(p, Pack{}) {
		t.Errorf("出错时应返回零值 Pack，实际：%+v", p)
	}
}

// TestLoadEmptyDir 空目录参数直接报错。
func TestLoadEmptyDir(t *testing.T) {
	if _, err := Load(context.Background(), "   "); err == nil {
		t.Error("空目录参数应报错")
	}
	if _, err := Load(nil, realPackDir); err == nil { //nolint:staticcheck // 故意传 nil 验证防御
		t.Error("ctx 为 nil 应报错")
	}
}

// TestSystemPrompt 校验三段顺序、内容来源与稳定性（契约 §4）。
func TestSystemPrompt(t *testing.T) {
	dir := newPackDir(t, map[string]string{
		fileSoul:    "SOUL段",
		fileRules:   "RULES段",
		fileSamples: "SAMPLES段",
	})
	p, err := Load(context.Background(), dir)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}

	got := p.SystemPrompt()
	want := "SOUL段\n\nRULES段\n\nSAMPLES段"
	if got != want {
		t.Fatalf("SystemPrompt = %q，期望 %q", got, want)
	}

	// 重复调用必须稳定（可被 prompt 缓存依赖）
	if again := p.SystemPrompt(); again != got {
		t.Errorf("SystemPrompt 不稳定：%q vs %q", got, again)
	}

	// 纯函数：不掺场景口径
	if strings.Contains(got, "<context>") || strings.Contains(got, "短。") {
		t.Errorf("system 段混入了场景内容：%q", got)
	}
}

// TestSystemPromptTrims 三段使用前各自 TrimSpace。
func TestSystemPromptTrims(t *testing.T) {
	dir := newPackDir(t, map[string]string{
		fileSoul:  "\n\n鱼  \n\n",
		fileRules: "  哼\n",
	})
	p, err := Load(context.Background(), dir)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if got, want := p.SystemPrompt(), "鱼\n\n哼\n\n？"; got != want {
		t.Errorf("SystemPrompt = %q，期望 %q", got, want)
	}
}

// TestContextKey 校验五种映射与 QQ 的 private 分支（契约 §3）。
func TestContextKey(t *testing.T) {
	cases := []struct {
		channel string
		private bool
		want    string
	}{
		{"live", false, ContextLive},
		{"live", true, ContextLive}, // 非 QQ 通道忽略 private
		{"comment", false, ContextComment},
		{"comment", true, ContextComment},
		{"dm", false, ContextDM},
		{"dm", true, ContextDM},
		{"qq", true, ContextQQPrivate},
		{"qq", false, ContextQQGroup},
	}
	for _, c := range cases {
		got, err := ContextKey(c.channel, c.private)
		if err != nil {
			t.Errorf("ContextKey(%q, %v) 报错：%v", c.channel, c.private, err)
			continue
		}
		if got != c.want {
			t.Errorf("ContextKey(%q, %v) = %q，期望 %q", c.channel, c.private, got, c.want)
		}
	}

	if _, err := ContextKey("telegram", false); err == nil {
		t.Error("未知通道应报错")
	}
	if _, err := ContextKey("", true); err == nil {
		t.Error("空通道应报错")
	}
}

// TestContextKeysFresh 每次返回新 slice：改返回值不能污染下次结果。
func TestContextKeysFresh(t *testing.T) {
	want := []string{ContextLive, ContextComment, ContextDM, ContextQQPrivate, ContextQQGroup}

	got := ContextKeys()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ContextKeys() = %v，期望 %v（顺序固定）", got, want)
	}

	got[0] = "被改掉了"
	if again := ContextKeys(); !reflect.DeepEqual(again, want) {
		t.Errorf("修改返回值污染了下次结果：%v", again)
	}
}

// TestContextForPreservesNormalizedBody 覆盖读取、换行归一、分段与访问的完整边界。
func TestContextForPreservesNormalizedBody(t *testing.T) {
	body := "\r\n    首行缩进  \r\n\r\n"
	dir := newPackDir(t, map[string]string{fileContext: "\ufeff## live\r\n" + body})
	p, err := Load(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ContextFor(ContextLive, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.ReplaceAll(body, "\r\n", "\n"); got != want {
		t.Fatalf("口径正文被裁剪：got=%q want=%q", got, want)
	}
}
