package config

import "testing"

// TestValidServiceEndpointContainerHost 只放行精确的容器宿主别名。
//
// host.docker.internal 是 Docker 的 host-gateway 别名；允许它走明文 HTTP
// 是为了让容器访问宿主机 ASR/搜索。相似域名、网关 IP、其它私网地址不享受
// 这个例外，否则就变成「放宽所有 HTTP」。
func TestValidServiceEndpointContainerHost(t *testing.T) {
	good := []string{
		"http://host.docker.internal:8077/v1/audio/transcriptions",
		"http://HOST.DOCKER.INTERNAL:8080/search",
		"https://host.docker.internal/search",
		"http://127.0.0.1:8077/v1/audio/transcriptions",
		"http://localhost:8080/search",
		"http://[::1]:8080/search",
		"https://search.example.com/api",
	}
	for _, raw := range good {
		if !validServiceEndpoint(raw) {
			t.Errorf("地址 %q 应被接受", raw)
		}
	}
	bad := []string{
		"http://example.com/search",
		"http://192.168.65.254:8080/search",
		"http://10.0.0.5:8080/search",
		"http://host.containers.internal:8080/search",
		"http://gateway.docker.internal:8080/search",
		"http://host.docker.internal.evil.com:8080/search",
		"http://evil.host.docker.internal:8080/search",
		"http://host.docker.internal.:8080/search",
		"http://user@host.docker.internal:8080/search",
		"http://host.docker.internal:8080/search?token=x",
		"http://host.docker.internal:8080/search#frag",
		"ftp://host.docker.internal/search",
	}
	for _, raw := range bad {
		if validServiceEndpoint(raw) {
			t.Errorf("地址 %q 应被拒绝", raw)
		}
	}
}
