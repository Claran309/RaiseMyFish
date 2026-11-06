package mcp

// 本文件登记 MCP 侧的工具名、描述与 inputSchema。
//
// 工具名与 internal/reasoning 的常量一一对应（catalog_test 会校验，防止漂移）；
// 参数适配规则：
//   - argsQuery：取 arguments.query 作为旧工具的单个文本参数；
//   - argsJSON：把 arguments 紧凑 JSON 原样交给旧工具（旧解析器优先按 JSON 对象解析）；
//   - argsIgnored：旧工具不读参数，统一传空串。

// argMode 是 MCP arguments 到旧 Tool.Run(string) 的转换方式。
type argMode int

const (
	argsIgnored argMode = iota
	argsQuery
	argsJSON
)

// InputSchema 是 MCP tools/list 返回的 JSON Schema（只覆盖本服务器用到的子集）。
type InputSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]SchemaProperty `json:"properties,omitempty"`
	Required   []string                  `json:"required,omitempty"`
}

// SchemaProperty 是单个参数的类型与约束。
type SchemaProperty struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Minimum     *int     `json:"minimum,omitempty"`
	Maximum     *int     `json:"maximum,omitempty"`
	MaxLength   *int     `json:"maxLength,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// Tool 是 tools/list 的条目。
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

type toolSpec struct {
	Description string
	Schema      InputSchema
	Args        argMode
}

func intPtr(v int) *int { return &v }

func textProp(desc string) SchemaProperty {
	return SchemaProperty{Type: "string", Description: desc}
}

func intProp(desc string, min, max int) SchemaProperty {
	return SchemaProperty{Type: "integer", Description: desc, Minimum: intPtr(min), Maximum: intPtr(max)}
}

func objectSchema(props map[string]SchemaProperty, required ...string) InputSchema {
	return InputSchema{Type: "object", Properties: props, Required: required}
}

// toolSpecs 是 MCP 暴露的全部工具元数据；实际暴露范围还要叠加「已注册 + 白名单」。
var toolSpecs = map[string]toolSpec{
	"memory_search": {
		Description: "查本会话/跨通道的历史弹幕原文与记忆摘要；query 传昵称或关键词，可为空",
		Schema:      objectSchema(map[string]SchemaProperty{"query": textProp("检索词（昵称/关键词）；留空表示按当前会话召回")}),
		Args:        argsQuery,
	},
	"knowledge_search": {
		Description: "查梗库/设定；query 传要查的词或整句",
		Schema:      objectSchema(map[string]SchemaProperty{"query": textProp("检索词；留空表示默认召回")}),
		Args:        argsQuery,
	},
	"presence": {
		Description: "查本鱼此刻在做什么（MCP 进程通常没有进行中的状态）",
		Schema:      objectSchema(nil),
		Args:        argsIgnored,
	},
	"history": {
		Description: "查本房间最近几条弹幕",
		Schema:      objectSchema(map[string]SchemaProperty{"limit": intProp("条数，默认10", 1, 20)}),
		Args:        argsJSON,
	},
	"viewer_memory": {
		Description: "查某个观众说过什么",
		Schema: objectSchema(map[string]SchemaProperty{
			"from_id": {Type: "string", Description: "观众UID（纯数字）", Pattern: `^[0-9]+$`},
			"limit":   intProp("条数，默认10", 1, 20),
		}, "from_id"),
		Args: argsJSON,
	},
	"viewer_search": {
		Description: "查某个观众是谁（uid/昵称/别名/描述都行；问「XX是谁」就传 XX；结果会标明命中理由）",
		Schema:      objectSchema(map[string]SchemaProperty{"query": textProp("观众昵称/uid/别名/描述关键词")}, "query"),
		Args:        argsQuery,
	},
	"reply_history": {
		Description: "查本房间自己最近回过什么",
		Schema:      objectSchema(map[string]SchemaProperty{"limit": intProp("条数，默认10", 1, 20)}),
		Args:        argsJSON,
	},
	"live_status": {
		Description: "查直播间现在在不在播、标题和人气",
		Schema:      objectSchema(nil),
		Args:        argsIgnored,
	},
	"quota": {
		Description: "查今天送了多少礼物、还剩多少额度",
		Schema:      objectSchema(nil),
		Args:        argsIgnored,
	},
	"now": {
		Description: "查当前时间（本地时区）",
		Schema:      objectSchema(nil),
		Args:        argsIgnored,
	},
	"vision": {
		Description: "看当前直播画面并详细描述",
		Schema:      objectSchema(nil),
		Args:        argsIgnored,
	},
	"web_search": {
		Description: "联网查证一个事实；query 不超过100字",
		Schema: objectSchema(map[string]SchemaProperty{
			"query": {Type: "string", Description: "查询词", MaxLength: intPtr(100)},
		}, "query"),
		Args: argsQuery,
	},
	"gift": {
		Description: "给本房间送礼物（受额度/白名单/开关约束）",
		Schema: objectSchema(map[string]SchemaProperty{
			"gift_id": {Type: "string", Description: "礼物ID（纯数字，缺省用配置值）", Pattern: `^[0-9]+$`},
			"num":     intProp("数量，默认1", 1, 100),
		}),
		Args: argsJSON,
	},
	"follow": {
		Description: "关注或取关用户；mid 可逗号分隔，每次最多5个",
		Schema: objectSchema(map[string]SchemaProperty{
			"mid":    textProp("用户UID，逗号分隔"),
			"action": {Type: "string", Description: "follow 或 unfollow，默认 follow", Enum: []string{"follow", "unfollow"}},
		}, "mid"),
		Args: argsJSON,
	},
	"like": {
		Description: "给本房间点赞",
		Schema:      objectSchema(map[string]SchemaProperty{"count": intProp("点赞个数，默认1", 1, 3)}),
		Args:        argsJSON,
	},
	"dm": {
		Description: "给主人或白名单发私信",
		Schema: objectSchema(map[string]SchemaProperty{
			"mid":  {Type: "string", Description: "用户UID（纯数字）", Pattern: `^[0-9]+$`},
			"text": {Type: "string", Description: "私信内容，最多200字", MaxLength: intPtr(200)},
		}, "mid", "text"),
		Args: argsJSON,
	},
	"post_dynamic": {
		Description: "发一条动态（默认关闭）",
		Schema: objectSchema(map[string]SchemaProperty{
			"text": {Type: "string", Description: "动态正文，最多200字", MaxLength: intPtr(200)},
		}, "text"),
		Args: argsJSON,
	},
}
