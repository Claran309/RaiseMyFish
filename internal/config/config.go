// Package config 加载基础设施与可选Brain配置，不修改进程环境或回显敏感值。
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/pelletier/go-toml/v2"
	"go.uber.org/zap/zapcore"
)

// Config 是当前阶段实际装配的配置。
type Config struct {
	Database  Database   `toml:"db"`
	Log       Log        `toml:"log"`
	Runtime   Runtime    `toml:"runtime"`
	Presence  Presence   `toml:"presence"`
	Channels  Channels   `toml:"channels"`
	LLM       LLM        `toml:"llm"`
	Knowledge Knowledge  `toml:"knowledge"`
	Embedding Embedding  `toml:"embedding"`
	Reasoning Reasoning  `toml:"reasoning"`
	Viewer    Viewer     `toml:"viewer"`
	Watch     Watch      `toml:"watch"`
	Proactive Proactive  `toml:"proactive"`
	Gift      Gift       `toml:"gift"`
	Memory    Memory     `toml:"memory"`
	Web       Web        `toml:"web"`
	MCP       MCP        `toml:"mcp"`
	Watchdog  Watchdog   `toml:"watchdog"`
	Collector Collectors `toml:"collector"`
	// Scheduler 为 nil 表示「不接消费循环」，只采集。
	Scheduler *Scheduler `toml:"scheduler"`
	// Sender 为 nil 表示「未配置发送」。
	Sender *Sender `toml:"sender"`
	// BiliResolver 由 fishd 在数据库就绪后注入：每次请求从账号服务取当前活跃凭据。
	// 不属于配置文件，不参与 TOML/JSON 序列化。
	BiliResolver func(ctx context.Context) (cookie, selfMID string, err error) `toml:"-" json:"-"`
}

// Database 的连接串只从环境/本地dotenv读取，不能序列化输出。
type Database struct {
	DSNEnv         string `toml:"dsn_env"`
	DSN            string `toml:"-" json:"-"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
}

// Log 控制JSON日志级别与目录。
type Log struct {
	Level string `toml:"level"`
	Dir   string `toml:"dir"`
}

// Runtime 控制本机服务状态文件与健康检查周期。
type Runtime struct {
	StateFile        string `toml:"state_file"`
	HeartbeatSeconds int    `toml:"heartbeat_seconds"`
	StaleSeconds     int    `toml:"stale_seconds"`
}

// Channel 是通道开关；当前仅live采集可启用，其它通道仍拒绝开启。
type Channel struct {
	Enabled bool `toml:"enabled"`
}

// Channels 保留已有通道命名，不提前实现平台能力。
type Channels struct {
	Live    Channel `toml:"live"`
	Comment Channel `toml:"comment"`
	DM      Channel `toml:"dm"`
	QQ      Channel `toml:"qq"`
}

type safeError struct {
	message string
	cause   error
}

func (e *safeError) Error() string { return e.message }
func (e *safeError) Unwrap() error { return e.cause }

// Load 按进程环境 > dotenv > TOML > 默认值加载；空路径表示不读该文件。
// 完成结构校验与B站凭据校验，适用于不使用 DB 活跃账号的进程。
func Load(path, envPath string) (Config, error) {
	cfg, err := LoadForDaemon(path, envPath)
	if err != nil {
		return cfg, err
	}
	if err := cfg.ValidateCredentials(); err != nil {
		return cfg, fmt.Errorf("校验基础设施配置：%w", err)
	}
	return cfg, nil
}

// LoadForDaemon 供 fishd 启动加载：先只做结构与非B站凭据校验。
// DB 就绪并选出活跃账号、回填静态配置后，再调用 ValidateCredentials；
// 这样「只有 DB 账号、环境没有 cookie」的部署不会被启动期强校验挡住。
func LoadForDaemon(path, envPath string) (Config, error) {
	cfg, err := load(path, envPath)
	if err != nil {
		return cfg, err
	}
	if err := cfg.validateStructure(true); err != nil {
		return cfg, fmt.Errorf("校验基础设施配置：%w", err)
	}
	return cfg, nil
}

// LoadForCLI 供 fishctl 的运维命令加载配置：跳过 collector.live 的运行校验。
// CLI 不采集直播，不应因为 live 房间/自身身份/凭据缺失而不可用；
// 数据库、日志、LLM 等其余配置仍按 fishd 同一口径校验。
func LoadForCLI(path, envPath string) (Config, error) {
	cfg, err := load(path, envPath)
	if err != nil {
		return cfg, err
	}
	if err := cfg.validateStructure(false); err != nil {
		return cfg, fmt.Errorf("校验基础设施配置：%w", err)
	}
	if err := cfg.validateCredentials(false); err != nil {
		return cfg, fmt.Errorf("校验基础设施配置：%w", err)
	}
	return cfg, nil
}

func load(path, envPath string) (Config, error) {
	cfg := Config{
		Database:  Database{DSNEnv: "FISH_DB_DSN", TimeoutSeconds: 5},
		Log:       Log{Level: "info", Dir: "logs"},
		Runtime:   Runtime{StateFile: "data/fishd-status.json", HeartbeatSeconds: 5, StaleSeconds: 20},
		LLM:       defaultLLM(),
		Knowledge: defaultKnowledge(),
		Embedding: defaultEmbedding(),
		Reasoning: DefaultReasoning(),
		Viewer:    defaultViewer(),
		Watch:     defaultWatch(),
		Proactive: defaultProactive(),
		Gift:      defaultGift(),
		Memory:    defaultMemory(),
		Web:       defaultWeb(),
		MCP:       defaultMCP(),
		Watchdog:  defaultWatchdog(),
		// 评论采集默认关闭；interval 先补默认值，开启后省略也能工作。
		Collector: Collectors{Live: defaultLiveCollector(), Comment: defaultCommentCollector(), DM: defaultDMCollector()},
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, &safeError{"读取TOML配置失败", err}
		}
		if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&cfg); err != nil {
			return cfg, &safeError{"TOML配置格式或字段错误", err}
		}
	}
	// [scheduler] 段存在才补默认值；整段缺失时保持 nil，等价于「只采集」。
	cfg.Scheduler.ApplyDefaults()
	// [sender] 同理：缺失即未配置发送。
	cfg.Sender.ApplyDefaults()
	// [reasoning] 是值结构：缺字段补默认（默认开启，显式 enabled=false 才关）。
	cfg.Reasoning.ApplyDefaults()
	// soul_path 未显式配置时跟随人格包，多 persona 各用各的。
	cfg.Memory.deriveSoulPath(cfg.LLM.PersonaDir)
	local := map[string]string{}
	if envPath != "" {
		values, err := godotenv.Read(envPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return cfg, &safeError{"读取本地环境配置失败", err}
		}
		if err == nil {
			local = values
		}
	}
	lookup := func(key string) (string, bool) {
		if value, ok := os.LookupEnv(key); ok {
			return value, true
		}
		value, ok := local[key]
		return value, ok
	}
	cfg.Database.DSN, _ = lookup(cfg.Database.DSNEnv)
	for key, dst := range map[string]*string{"FISH_LOG_LEVEL": &cfg.Log.Level, "FISH_LOG_DIR": &cfg.Log.Dir, "FISH_STATE_FILE": &cfg.Runtime.StateFile} {
		if value, ok := lookup(key); ok {
			*dst = value
		}
	}
	cfg.LLM.resolveEnvironment(lookup)
	cfg.Embedding.resolveEnvironment(lookup)
	// 评论采集/发送与直播共用同一账号的 B站凭据；live 关闭时也要为评论解析。
	commentCredentials := cfg.Collector.Comment.Enabled || (cfg.Sender != nil && cfg.Sender.Comment != nil && cfg.Sender.Comment.Enabled)
	dmCredentials := cfg.Collector.DM.Enabled || (cfg.Sender != nil && cfg.Sender.DM.Enabled)
	cfg.Collector.Live.resolveEnvironment(lookup, cfg.Channels.Live.Enabled || commentCredentials || dmCredentials)
	// 主动弹幕复用看直播的截帧与视觉模型，因此也需要解析同一份凭据。
	cfg.Watch.resolveEnvironment(lookup, cfg.Watch.Enabled || cfg.Proactive.Enabled)
	cfg.Gift.resolveEnvironment(lookup, cfg.Gift.Enabled)
	return cfg, nil
}

// Validate 只验证已装配能力；LLM/B站密钥留到对应模块启用时检查。
func (c Config) Validate() error {
	if err := c.validateStructure(true); err != nil {
		return err
	}
	return c.validateCredentials(true)
}

// ValidateCredentials 校验各实际启用能力所需的B站凭据与账号身份。
// fishd 在 DB 活跃账号回填静态配置后调用；环境变量凭据在加载阶段已解析。
func (c Config) ValidateCredentials() error { return c.validateCredentials(true) }

// validateStructure 只校验非B站凭据部分：DB 活跃账号可能在本阶段之后才回填 cookie/self_id。
func (c Config) validateStructure(strictLive bool) error {
	if strings.TrimSpace(c.Database.DSNEnv) == "" || strings.TrimSpace(c.Database.DSN) == "" {
		return errors.New("数据库环境变量未配置")
	}
	if c.Database.TimeoutSeconds < 1 || c.Database.TimeoutSeconds > 60 {
		return errors.New("数据库超时必须为1至60秒")
	}
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(c.Log.Level)); err != nil {
		return &safeError{"日志级别无效", err}
	}
	if strings.TrimSpace(c.Log.Dir) == "" || strings.TrimSpace(c.Runtime.StateFile) == "" {
		return errors.New("日志目录和状态文件不能为空")
	}
	if c.Runtime.HeartbeatSeconds < 1 || c.Runtime.HeartbeatSeconds > 300 || c.Runtime.StaleSeconds <= 2*c.Runtime.HeartbeatSeconds || c.Runtime.StaleSeconds > 3600 {
		return errors.New("心跳周期需为1至300秒，过期阈值需大于两倍周期且不超过3600秒")
	}
	if err := c.Presence.Validate(); err != nil {
		return err
	}
	if c.Channels.QQ.Enabled {
		return errors.New("QQ尚未装配，请关闭对应通道开关")
	}
	if err := c.Collector.Comment.Validate(); err != nil {
		return err
	}
	if c.Collector.Comment.Enabled && !c.Channels.Comment.Enabled {
		return errors.New("评论采集启用需要先开启[channels.comment]")
	}
	if c.Collector.DM.Enabled && !c.Channels.DM.Enabled {
		return errors.New("私信采集启用需要先开启[channels.dm]")
	}
	if c.Sender != nil && c.Sender.DM.Enabled && !c.Channels.DM.Enabled {
		return errors.New("私信发送启用需要先开启[channels.dm]")
	}
	if c.Sender != nil && c.Sender.Comment != nil && c.Sender.Comment.Enabled && !c.Channels.Comment.Enabled {
		return errors.New("评论发送启用需要先开启[channels.comment]")
	}
	// CLI 只读命令不需要直播采集运行时；跳过这一项，但保留其余严格校验。
	if strictLive {
		if err := c.Collector.Live.ValidateStructure(c.Channels.Live.Enabled); err != nil {
			return err
		}
	}
	if err := c.Knowledge.Validate(); err != nil {
		return err
	}
	if c.Knowledge.MemeLearn && !c.LLM.Enabled {
		return errors.New("梗白学习启用需要先启用LLM")
	}
	if err := c.Embedding.Validate(); err != nil {
		return err
	}
	if c.Embedding.AutoRefresh && !c.Knowledge.Enabled {
		return errors.New("embedding 定时增量需要先启用知识库")
	}
	if err := c.Reasoning.Validate(); err != nil {
		return err
	}
	if err := c.Viewer.Validate(); err != nil {
		return err
	}
	if err := c.Scheduler.Validate(); err != nil {
		return err
	}
	if err := c.Collector.DM.Validate(); err != nil {
		return err
	}
	if err := c.Sender.Validate(); err != nil {
		return err
	}
	if err := c.Watch.validateStructure(); err != nil {
		return err
	}
	if err := c.Proactive.Validate(); err != nil {
		return err
	}
	if err := c.Gift.validateStructure(); err != nil {
		return err
	}
	if err := c.Memory.Validate(); err != nil {
		return err
	}
	if err := c.Web.Validate(); err != nil {
		return err
	}
	if err := c.MCP.Validate(); err != nil {
		return err
	}
	if err := c.Watchdog.Validate(); err != nil {
		return err
	}
	if err := c.LLM.Validate(); err != nil {
		return err
	}
	if c.Watch.Enabled {
		if !c.LLM.Enabled {
			return errors.New("看直播启用需要先启用LLM")
		}
		if _, _, _, err := c.LLM.Resolve(c.Watch.LLMRoute); err != nil {
			return errors.New("看直播的视觉模型路由不可用")
		}
	}
	if c.Proactive.Enabled {
		if !c.LLM.Enabled {
			return errors.New("主动弹幕启用需要先启用LLM")
		}
		if _, _, _, err := c.LLM.Resolve(c.Watch.LLMRoute); err != nil {
			return errors.New("主动弹幕的视觉模型路由不可用")
		}
		if !c.Channels.Live.Enabled {
			return errors.New("主动弹幕启用需要开启live采集")
		}
		if c.Sender == nil {
			return errors.New("主动弹幕启用需要配置[sender]")
		}
		// 截帧与看直播复用同一套流地址/清晰度/ffmpeg/凭据配置。
		if err := c.Watch.validateStreamStructure(); err != nil {
			return err
		}
	}
	return nil
}

// validateCredentials 只校验B站凭据与账号身份，且只针对实际启用能力。
// 结构问题已在 validateStructure 拒绝；这里允许 DB 活跃账号在加载后才回填。
func (c Config) validateCredentials(strictLive bool) error {
	// CLI 只读命令不采集直播，跳过 live 凭据；其余已启用能力仍要能实际工作。
	if strictLive {
		if err := c.Collector.Live.ValidateCredential(c.Channels.Live.Enabled); err != nil {
			return err
		}
	}
	if c.Collector.Comment.Enabled {
		if c.Collector.Live.Cookie == "" && c.Collector.Live.CookieFile == "" {
			return errors.New("评论采集启用需要B站凭据")
		}
		// 没有本账号 UID 就无法跳过自己发出的回复，会形成自回复循环。
		if id := c.accountSelfID(); id == "" || !positiveSenderID.MatchString(id) || strings.Trim(id, "0") == "" {
			return errors.New("评论采集启用需要本账号 self_id（避免回复自己的评论）")
		}
	}
	if c.Collector.DM.Enabled {
		if c.Collector.Live.Cookie == "" && c.Collector.Live.CookieFile == "" {
			return errors.New("私信采集启用需要B站凭据")
		}
		if id := c.accountSelfID(); id == "" || !positiveSenderID.MatchString(id) || strings.Trim(id, "0") == "" {
			return errors.New("私信采集启用需要本账号 self_id（避免回复自己的私信）")
		}
	}
	if c.Sender != nil && c.Sender.DM.Enabled && c.Collector.Live.Cookie == "" && c.Collector.Live.CookieFile == "" {
		return errors.New("私信发送启用需要B站凭据")
	}
	if c.Sender != nil && c.Sender.Comment != nil && c.Sender.Comment.Enabled && c.Collector.Live.Cookie == "" && c.Collector.Live.CookieFile == "" {
		return errors.New("评论发送启用需要B站凭据")
	}
	// 看直播与主动弹幕共用截帧凭据；主动弹幕开启时即使 [watch] 关闭也要校验。
	if c.Watch.Enabled || c.Proactive.Enabled {
		if err := c.Watch.validateStreamCredential(); err != nil {
			return err
		}
	}
	if err := c.Gift.validateCredential(); err != nil {
		return err
	}
	return nil
}
