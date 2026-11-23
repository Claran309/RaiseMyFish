package memory

// FactKey 把「主体 + 谓词」归一成稳定版本键。
//
// 同一 fact_key 只应有一条 active 事实；改口时旧行失效、新行生效。
// 归一规则刻意保守：只做大小写/空白/常见标点折叠，不做同义词扩展，
// 避免把「喜欢猫」和「喜欢狗」错误合并。

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// FactKey 返回 `<domain>:<subject>:<hash>`；domain 区分公开/私密可见域。
func FactKey(domain, subject, title string) string {
	domain = strings.TrimSpace(domain)
	subject = strings.TrimSpace(subject)
	normalized := normalizePredicate(title)
	if domain == "" || subject == "" || normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(domain + "\x1f" + subject + "\x1f" + normalized))
	return domain + ":" + subject + ":" + hex.EncodeToString(sum[:8])
}

// normalizePredicate 折叠空白/标点与全半角差异，保留语义字符。
func normalizePredicate(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
