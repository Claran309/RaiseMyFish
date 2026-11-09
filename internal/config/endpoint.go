package config

import (
	"net/url"
	"strings"
)

// containerHost 是 Docker Compose 通过 host-gateway 注入的宿主机别名。
//
// 只放行这个精确主机名：它由 Docker 解析到虚拟网桥的宿主机端，不会指向公网；
// 网关 IP、其它私网地址或相似域名一律仍按 validEndpoint 拒绝。
const containerHost = "host.docker.internal"

// validServiceEndpoint 校验 ASR、搜索这类「本机辅助服务」的地址：
// HTTPS 任意主机；HTTP 仅限回环，或精确的 host.docker.internal。
//
// LLM/Embedding/视觉模型不走这里：contracts/brain.md 要求 Provider 端点
// 只能是 HTTPS 或字面回环 HTTP，放宽会越过契约。
func validServiceEndpoint(raw string) bool {
	if valid, _ := validEndpoint(raw); valid {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Scheme == "http" && strings.EqualFold(u.Hostname(), containerHost)
}
