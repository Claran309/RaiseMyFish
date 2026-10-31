// Package persona 负责人格包（Persona Pack）的加载与静态访问。
//
// 契约：contracts/persona.md（基线 P0-PACK-1）。本包只做「读五份静态文件 + 纯访问」：
//   - 人格是纯数据，不写死在代码里，改人格不用改代码（docs/08-persona.md）；
//   - 注入顺序：system = soul → rules → samples；通道口径 context 由 Brain 在 0.6 自行组织，
//     本包只按通道选出对应正文，不做动态拼装、不做预算裁剪；
//   - 不依赖 Brain、PG、eval 用例、baseline 或真实模型；不提供全局可变注册表。
package persona

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// 五种人格场景键（契约 §1）。它们是"人格口径"的键，不是 Event.Channel——
// 事件通道仍然是 live / comment / dm / qq，qq 再按 private 拆成两个口径。
const (
	ContextLive      = "live"
	ContextComment   = "comment"
	ContextDM        = "dm"
	ContextQQPrivate = "qq_private"
	ContextQQGroup   = "qq_group"
)

// maxFileBytes 是单文件 I/O 上限（1 MiB）。
// 这是防止误读大文件的边界，**不是**最低篇幅要求（契约 §2 明确不设最小字数门槛）。
const maxFileBytes = 1 << 20

// 人格包内的固定文件名（契约 §2：只读这五份）。
const (
	fileMeta    = "meta.yaml"
	fileSoul    = "soul.md"
	fileRules   = "rules.md"
	fileSamples = "samples.md"
	fileContext = "context.md"
)

// Pack 是一次加载得到的**人格快照**。
//
// 调用方把它当作只读：修改人格必须重新 Load 并整体替换，不提供原地修改入口。
type Pack struct {
	// Dir 清理后的绝对目录。
	Dir string
	// Meta 元信息。
	Meta Meta
	// Soul 身份与性格（system 第 1 段）。
	Soul string
	// Rules 铁律与禁忌（system 第 2 段）。
	Rules string
	// Samples 语气样本（system 第 3 段）。
	Samples string
	// Contexts 各场景口径正文，键为 ContextLive / ContextComment / ContextDM /
	// ContextQQPrivate / ContextQQGroup 中被 meta.channels 声明的那些。
	Contexts map[string]string
}

// ContextKeys 返回全部五种场景键。
//
// 每次调用都返回新 slice，调用方随意修改也不会污染下次结果（契约 §1）。
// 顺序固定为 live、comment、dm、qq_private、qq_group。
func ContextKeys() []string {
	return []string{ContextLive, ContextComment, ContextDM, ContextQQPrivate, ContextQQGroup}
}

// isContextKey 判断是否是五种场景键之一。
//
// 特意不用包级 slice：契约禁止全局可变注册表，用 switch 从根上杜绝被改。
func isContextKey(key string) bool {
	switch key {
	case ContextLive, ContextComment, ContextDM, ContextQQPrivate, ContextQQGroup:
		return true
	}
	return false
}

// ContextKey 把「事件通道 + 是否私密」映射成人格场景键（契约 §3）。
//
// 只做口径选择，**不是**公开/私密权限检查——ACL 与身份判断仍归 repository/上层。
// live / comment / dm 忽略 private；只有 qq 才按 private 二选一。
func ContextKey(channel string, private bool) (string, error) {
	switch channel {
	case ContextLive:
		return ContextLive, nil
	case ContextComment:
		return ContextComment, nil
	case ContextDM:
		return ContextDM, nil
	case "qq":
		if private {
			return ContextQQPrivate, nil
		}
		return ContextQQGroup, nil
	}
	return "", fmt.Errorf("未知通道 %q（允许 live / comment / dm / qq）", channel)
}

// ContextFor 取该包中对应场景的静态正文。
//
// 找不到就报错，**不**回退到 live 或私聊（契约 §3）：
// 静默回退会让「某通道没有口径」这种配置错误变成悄悄用错语气。
func (p Pack) ContextFor(channel string, private bool) (string, error) {
	key, err := ContextKey(channel, private)
	if err != nil {
		return "", err
	}
	body, ok := p.Contexts[key]
	if !ok {
		return "", fmt.Errorf("人格包 %q 未声明场景 %q", p.Meta.Name, key)
	}
	return body, nil
}

// SystemPrompt 按契约 §4 拼三段静态 system：
//
//	TrimSpace(Soul) + "\n\n" + TrimSpace(Rules) + "\n\n" + TrimSpace(Samples)
//
// 纯函数，重复调用结果完全一致；不掺入场景口径、当前消息、时间、记忆或额外标题。
func (p Pack) SystemPrompt() string {
	return strings.TrimSpace(p.Soul) + "\n\n" +
		strings.TrimSpace(p.Rules) + "\n\n" +
		strings.TrimSpace(p.Samples)
}

// Load 从目录加载人格包快照。
//
// 只读 meta.yaml、soul.md、rules.md、samples.md、context.md 五份文件，不读本机旧人设、
// .env、日志、账本、eval 用例或任何外部配置。任何错误都返回零值 Pack，不返回半成品。
//
// ctx 在开始和每次文件读取前检查；取消/超时原样返回，保持 errors.Is 可判别。
func Load(ctx context.Context, dir string) (Pack, error) {
	if ctx == nil {
		return Pack{}, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return Pack{}, err
	}
	if strings.TrimSpace(dir) == "" {
		return Pack{}, errors.New("人格包目录不能为空")
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return Pack{}, fmt.Errorf("解析人格包目录失败：%w", err)
	}
	abs = filepath.Clean(abs)

	// meta 先读：后面的校验和错误信息都要用到 name/channels
	metaRaw, err := readFileText(ctx, abs, fileMeta)
	if err != nil {
		return Pack{}, err
	}
	meta, err := decodeMeta([]byte(metaRaw))
	if err != nil {
		return Pack{}, fmt.Errorf("%s：%w", fileMeta, err)
	}
	if err := validateMeta(meta); err != nil {
		return Pack{}, fmt.Errorf("%s：%w", fileMeta, err)
	}

	soul, err := readRequiredText(ctx, abs, fileSoul)
	if err != nil {
		return Pack{}, err
	}
	rules, err := readRequiredText(ctx, abs, fileRules)
	if err != nil {
		return Pack{}, err
	}
	samples, err := readRequiredText(ctx, abs, fileSamples)
	if err != nil {
		return Pack{}, err
	}

	contextRaw, err := readRequiredText(ctx, abs, fileContext)
	if err != nil {
		return Pack{}, err
	}
	contexts, err := splitContexts(contextRaw)
	if err != nil {
		return Pack{}, fmt.Errorf("%s：%w", fileContext, err)
	}

	if err := matchDeclaredChannels(meta.Channels, contexts); err != nil {
		return Pack{}, err
	}

	return Pack{
		Dir:      abs,
		Meta:     meta,
		Soul:     soul,
		Rules:    rules,
		Samples:  samples,
		Contexts: contexts,
	}, nil
}

// readRequiredText 读取必需文本文件，并要求 trim 后非空。
func readRequiredText(ctx context.Context, dir, name string) (string, error) {
	text, err := readFileText(ctx, dir, name)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s 内容为空", name)
	}
	return text, nil
}

// readFileText 读取并规范化一个文本文件。
//
// 规则（契约 §2）：必须是常规文件、合法 UTF-8、不超过 maxFileBytes；
// 去掉开头一个 UTF-8 BOM，CRLF / 单独 CR 统一成 LF，其余内容原样保留。
func readFileText(ctx context.Context, dir, name string) (string, error) {
	// 每次读文件前都检查一次，长任务能被及时取消
	if err := ctx.Err(); err != nil {
		return "", err
	}

	path := filepath.Join(dir, name)

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败：%w", name, err)
	}
	// 目录、设备、命名管道等都不接受；符号链接若指向常规文件则按常规文件处理
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s 不是常规文件（mode=%s），不支持该文件类型", name, info.Mode().Type())
	}
	if info.Size() > maxFileBytes {
		return "", fmt.Errorf("%s 大小 %d 字节，超过上限 %d 字节", name, info.Size(), maxFileBytes)
	}

	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败：%w", name, err)
	}
	defer f.Close()

	// 多读 1 字节用于识别「文件在 Stat 之后又变大了」的情况
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败：%w", name, err)
	}
	if len(data) > maxFileBytes {
		return "", fmt.Errorf("%s 大小超过上限 %d 字节", name, maxFileBytes)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s 不是合法的 UTF-8 文本", name)
	}

	return normalizeText(data), nil
}

// normalizeText 去 BOM、统一换行，不裁剪正文。
func normalizeText(data []byte) string {
	s := string(data)
	// 只去掉开头的一个 BOM；正文中间出现的 U+FEFF 属于内容，不动
	s = strings.TrimPrefix(s, "\ufeff")
	// 先处理 CRLF，再处理孤立的 CR（老 Mac / 手工编辑可能留下）
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

// splitContexts 按「围栏代码块外的独立二级 ASCII 标题 `## <key>`」切分 context.md（契约 §2）。
//
// 规则细节：
//   - 首个标题之前的内容是文件头说明，只给人看，不作为场景；
//   - 标题词是 ASCII 记号但不在五种场景键内 → 报错（防拼错键名静默失效）；
//   - 标题词含中文/空格（如 `## 直播口径`）**不是**场景分隔，原样留在当前场景正文里；
//   - 围栏代码块（``` 或 ~~~）里的 `## xxx` 不算标题；
//   - 重复标题、空场景正文都报错；
//   - 场景内的段落、`###` 子标题、表格一律原样保留。
func splitContexts(raw string) (map[string]string, error) {
	out := make(map[string]string)

	var (
		cur     string
		inFence bool
		buf     strings.Builder
	)

	// flush 把当前累积的场景正文落到 out
	flush := func() error {
		if cur == "" {
			return nil
		}
		body := buf.String()
		if strings.TrimSpace(body) == "" {
			return fmt.Errorf("场景 %q 正文为空", cur)
		}
		out[cur] = body
		buf.Reset()
		return nil
	}

	for _, original := range strings.SplitAfter(raw, "\n") {
		line := strings.TrimSuffix(original, "\n")
		// 围栏行本身属于正文，但要先翻转状态
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		} else if !inFence && isLevelTwoHeading(line) {
			token := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "##"))
			if isASCIIKeyToken(token) {
				if !isContextKey(token) {
					return nil, fmt.Errorf("未知场景键 %q（允许 %s）", token, strings.Join(ContextKeys(), " / "))
				}
				// 重复判定要同时看「已落盘」和「正在写」：
				// 相邻两段同名时，前一段还没 flush，只看 out 会漏掉。
				if _, dup := out[token]; dup || token == cur {
					return nil, fmt.Errorf("场景键 %q 重复出现", token)
				}
				if err := flush(); err != nil {
					return nil, err
				}
				cur = token
				continue
			}
			// 非 ASCII 记号（中文说明标题等）：不切分，落到正文里
		}

		if cur != "" {
			buf.WriteString(original)
		}
	}

	if err := flush(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("没有任何 `## <场景键>` 分节")
	}
	return out, nil
}

// isLevelTwoHeading 判断是否为二级标题（`## x`），排除 `###` 及更深层级。
func isLevelTwoHeading(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "##") || strings.HasPrefix(t, "###") {
		return false
	}
	return len(t) == 2 || t[2] == ' ' || t[2] == '\t'
}

// isASCIIKeyToken 判断是否纯 ASCII 记号（字母/数字/下划线/连字符）。
// 只有这种记号才可能被当作场景键；含中文或空格的标题一律视为普通说明文字。
func isASCIIKeyToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// matchDeclaredChannels 校验「meta.channels 声明的集合」与「context.md 实际段集合」完全相等。
//
// 允许只声明 live 的最小包；声明了却没写段、写了段却没声明，都算配置错误。
func matchDeclaredChannels(declared []string, contexts map[string]string) error {
	declaredSet := make(map[string]struct{}, len(declared))
	for _, c := range declared {
		declaredSet[c] = struct{}{}
		if _, ok := contexts[c]; !ok {
			return fmt.Errorf("%s 声明了场景 %q，但 %s 里没有对应的 `## %s` 段",
				fileMeta, c, fileContext, c)
		}
	}
	for key := range contexts {
		if _, ok := declaredSet[key]; !ok {
			return fmt.Errorf("%s 里出现了未声明的场景 %q（%s 的 channels 未包含）",
				fileContext, key, fileMeta)
		}
	}
	return nil
}
