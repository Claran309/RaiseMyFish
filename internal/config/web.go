package config

import (
	"errors"
	"net"
	"strings"
)

// Web 是 ReAct 联网工具的外部服务配置（`[web]`）。
// search_url 为空表示未配置搜索源，工具会明确回复「未配置」而不是编造结果。
type Web struct {
	// Enabled 控制运维修 panel 是否启动（默认关）。
	Enabled bool `toml:"enabled"`
	// Addr 是面板监听地址，只允许回环（127.0.0.1 / ::1 / localhost）。
	Addr      string `toml:"addr"`
	SearchURL string `toml:"search_url"`
	// SearchKey 作为 Authorization: Bearer，不序列化、不记日志。
	SearchKey string `toml:"search_key" json:"-"`
	// SearchKeyEnv 是读 key 的环境变量名，优先于 SearchKey（推荐，避免明文落盘）。
	SearchKeyEnv string `toml:"search_key_env" json:"-"`
	// Protocol 是搜索协议：generic（默认）/ serper / bing。
	Protocol string `toml:"protocol"`
	// AllowedDomains 是结果 URL 域名白名单；空表示不限。
	AllowedDomains []string `toml:"allowed_domains"`

	// resolvedKey/keyResolved 是 Load 阶段按「进程环境 > dotenv > 字面值」定下的 key（O07）。
	//
	// 不能在 ResolveKey 里现读 os.Getenv：那条路只看进程环境，
	// dotenv 里的同名 key 永远不生效，配置就分裂成两套来源。
	// 未导出字段不参与 TOML 解码，也不出现在 JSON 输出里。
	resolvedKey string
	keyResolved bool
}

func defaultWeb() Web { return Web{Addr: "127.0.0.1:8090", Protocol: "generic"} }

// normalizeDomain 把白名单条目规整成小写域名；不接受协议、端口、路径与通配符。
func normalizeDomain(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" || len(value) > 253 || strings.ContainsAny(value, "/:@*?# \t\r\n\x00[]") {
		return "", false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", false
			}
		}
	}
	return value, true
}

// resolveEnvironment 用主加载器的 lookup 解析密钥（进程环境 > 本地 dotenv）。
//
// 语义与 Embedding.resolveEnvironment 一致：环境变量**存在即覆盖**，
// 即使值为空（显式关闭）也不回退字面值；不存在才用 search_key 兼容旧配置。
func (w *Web) resolveEnvironment(lookup func(string) (string, bool)) {
	if name := strings.TrimSpace(w.SearchKeyEnv); name != "" {
		if value, ok := lookup(name); ok {
			w.resolvedKey = strings.TrimSpace(value)
			w.keyResolved = true
			return
		}
	}
	w.resolvedKey = strings.TrimSpace(w.SearchKey)
	w.keyResolved = true
}

// ResolveKey 返回实际使用的 key：Load 阶段已按统一来源解析好。
//
// 未经 Load 的临时对象（测试、手工构造）只认字面值——不再现读进程环境，
// 那正是「dotenv 里的 key 不生效」的根因。
func (w Web) ResolveKey() string {
	if w.keyResolved {
		return w.resolvedKey
	}
	return strings.TrimSpace(w.SearchKey)
}

// Validate 只校验已配置的搜索源；空地址合法。
func (w Web) Validate() error {
	raw := strings.TrimSpace(w.SearchURL)
	if raw != "" {
		if valid, _ := validEndpoint(raw); !valid {
			return errors.New("搜索服务地址必须是HTTPS或本机HTTP地址")
		}
	}
	if w.Enabled {
		host, port, err := net.SplitHostPort(strings.TrimSpace(w.Addr))
		if err != nil || port == "" {
			return errors.New("面板地址必须形如 127.0.0.1:8090")
		}
		h := strings.Trim(host, "[]")
		ip := net.ParseIP(h)
		if !strings.EqualFold(h, "localhost") && (ip == nil || !ip.IsLoopback()) {
			return errors.New("面板只允许监听回环地址")
		}
	}
	if strings.ContainsAny(w.SearchKey, "\r\n\x00") || strings.ContainsAny(w.SearchKeyEnv, "\r\n\x00") {
		return errors.New("搜索服务密钥不能包含控制字符")
	}
	switch protocol := strings.ToLower(strings.TrimSpace(w.Protocol)); protocol {
	case "", "generic", "serper", "bing":
	default:
		return errors.New("搜索协议必须是 generic、serper 或 bing")
	}
	for _, domain := range w.AllowedDomains {
		if _, ok := normalizeDomain(domain); !ok {
			return errors.New("搜索域名白名单只能是域名（不允许协议、端口、路径或通配符）")
		}
	}
	return nil
}
