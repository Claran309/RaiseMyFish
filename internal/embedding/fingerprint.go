package embedding

// 本文件定义「向量空间指纹」：同一维度下换模型/提供方/输入模板，
// 旧向量不能继续参与排序，也不能被增量任务当成「已入库」跳过。
//
// 指纹只含非敏感配置（提供方地址、模型名、维度、模板版本），
// 不含 API Key：换密钥不改变向量空间，凭据也绝不进入指纹或日志。

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// indexFingerprintVersion 是指纹算法的版本；算法本身变化时递增。
const indexFingerprintVersion = "fish-embedding-index/v1"

// IndexFingerprint 把提供方、模型、维度与输入模板压成稳定的十六进制指纹。
//
// provider 用 embedding 服务地址表示；template 由领域层提供并带版本号，
// 这样模板拼接规则一变，指纹随之变化，旧向量自动失效。
func IndexFingerprint(provider, model string, dimension int, template string) string {
	source := strings.Join([]string{
		indexFingerprintVersion,
		strings.TrimSpace(provider),
		strings.TrimSpace(model),
		strconv.Itoa(dimension),
		strings.TrimSpace(template),
	}, "\n")
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

// Fingerprint 用客户端实际配置计算指纹，避免调用方另抄一份参数导致漂移。
func (c *Client) Fingerprint(template string) string {
	if c == nil {
		return ""
	}
	return IndexFingerprint(c.url, c.model, c.dimension, template)
}
