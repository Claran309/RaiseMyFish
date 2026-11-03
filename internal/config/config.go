// Package config 加载基础设施与可选Brain配置，不修改进程环境或回显敏感值。
package config

import (
	"bytes"
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
	Channels  Channels   `toml:"channels"`
	LLM       LLM        `toml:"llm"`
	Knowledge Knowledge  `toml:"knowledge"`
	Reasoning Reasoning  `toml:"reasoning"`
	Viewer    Viewer     `toml:"viewer"`
	Watch     Watch      `toml:"watch"`
	Proactive Proactive  `toml:"proactive"`
	Gift      Gift       `toml:"gift"`
	Memory    Memory     `toml:"memory"`
	Collector Collectors `toml:"collector"`
	// Scheduler 为 nil 表示「不接消费循环」，只采集。
	Scheduler *Scheduler `toml:"scheduler"`
	// Sender 为 nil 表示「未配置发送」。
	Sender *Sender `toml:"sender"`
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
// fishd 使用完整校验，collector.live 缺房间/凭据时拒绝启动。
func Load(path, envPath string) (Config, error) {
	return load(path, envPath, true)
}

// LoadForCLI 供 fishctl 的运维命令加载配置：跳过 collector.live 的运行校验。
// CLI 不采集直播，不应因为 live 房间/自身身份/凭据缺失而不可用；
// 数据库、日志、LLM 等其余配置仍按 fishd 同一口径校验。
func LoadForCLI(path, envPath string) (Config, error) {
	return load(path, envPath, false)
}

func load(path, envPath string, strictLive bool) (Config, error) {
	cfg := Config{
		Database:  Database{DSNEnv: "FISH_DB_DSN", TimeoutSeconds: 5},
		Log:       Log{Level: "info", Dir: "logs"},
		Runtime:   Runtime{StateFile: "data/fishd-status.json", HeartbeatSeconds: 5, StaleSeconds: 20},
		LLM:       defaultLLM(),
		Knowledge: defaultKnowledge(),
		Reasoning: DefaultReasoning(),
		Viewer:    defaultViewer(),
		Watch:     defaultWatch(),
		Proactive: defaultProactive(),
		Gift:      defaultGift(),
		Memory:    defaultMemory(),
		Collector: Collectors{Live: defaultLiveCollector()},
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
	cfg.Collector.Live.resolveEnvironment(lookup, cfg.Channels.Live.Enabled)
	// 主动弹幕复用看直播的截帧与视觉模型，因此也需要解析同一份凭据。
	cfg.Watch.resolveEnvironment(lookup, cfg.Watch.Enabled || cfg.Proactive.Enabled)
	cfg.Gift.resolveEnvironment(lookup, cfg.Gift.Enabled)
	if err := cfg.validate(strictLive); err != nil {
		return cfg, fmt.Errorf("校验基础设施配置：%w", err)
	}
	return cfg, nil
}

// Validate 只验证已装配能力；LLM/B站密钥留到对应模块启用时检查。
func (c Config) Validate() error { return c.validate(true) }

func (c Config) validate(strictLive bool) error {
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
	if c.Channels.Comment.Enabled || c.Channels.DM.Enabled || c.Channels.QQ.Enabled {
		return errors.New("评论/私信/QQ尚未装配，请关闭对应通道开关")
	}
	// CLI 只读命令不需要直播采集运行时；跳过这一项，但保留其余严格校验。
	if strictLive {
		if err := c.Collector.Live.Validate(c.Channels.Live.Enabled); err != nil {
			return err
		}
	}
	if err := c.Knowledge.Validate(); err != nil {
		return err
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
	if err := c.Sender.Validate(); err != nil {
		return err
	}
	if err := c.Watch.Validate(); err != nil {
		return err
	}
	if err := c.Proactive.Validate(); err != nil {
		return err
	}
	if err := c.Gift.Validate(); err != nil {
		return err
	}
	if err := c.Memory.Validate(); err != nil {
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
		if err := c.Watch.validateStream(); err != nil {
			return err
		}
	}
	return nil
}
