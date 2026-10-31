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
	Database Database `toml:"db"`
	Log      Log      `toml:"log"`
	Runtime  Runtime  `toml:"runtime"`
	Channels Channels `toml:"channels"`
	LLM      LLM      `toml:"llm"`
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

// Channel 是通道开关；当前尚未装配业务通道，开启会明确报错。
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
func Load(path, envPath string) (Config, error) {
	cfg := Config{
		Database: Database{DSNEnv: "FISH_DB_DSN", TimeoutSeconds: 5},
		Log:      Log{Level: "info", Dir: "logs"},
		Runtime:  Runtime{StateFile: "data/fishd-status.json", HeartbeatSeconds: 5, StaleSeconds: 20},
		LLM:      defaultLLM(),
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
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("校验基础设施配置：%w", err)
	}
	return cfg, nil
}

// Validate 只验证已装配能力；LLM/B站密钥留到对应模块启用时检查。
func (c Config) Validate() error {
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
	if c.Channels.Live.Enabled || c.Channels.Comment.Enabled || c.Channels.DM.Enabled || c.Channels.QQ.Enabled {
		return errors.New("当前尚未装配业务通道，请关闭通道开关")
	}
	return c.LLM.Validate()
}
