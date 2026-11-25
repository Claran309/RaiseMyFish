package config

import "testing"

// TestValidateCredentialsAllowsDryRunWithoutCookie 压测 dry_run 不需要真实B站凭据；
// 关闭 dry_run 后同一份配置必须重新要求凭据。
func TestValidateCredentialsAllowsDryRunWithoutCookie(t *testing.T) {
	cfg := Config{
		Database: Database{DSNEnv: "FISH_DB_DSN", TimeoutSeconds: 5},
		Log:      Log{Level: "info", Dir: "logs"},
		Runtime:  Runtime{StateFile: "state/x.json", HeartbeatSeconds: 5, StaleSeconds: 20},
		Channels: Channels{
			Live:    Channel{Enabled: true},
			Comment: Channel{Enabled: true},
			DM:      Channel{Enabled: true},
		},
		Sender: &Sender{
			NodePath: "node", ScriptPath: "x.mjs", HTTPTimeoutMS: 1000,
			SelfID: "1000", DryRun: true,
			Comment: &CommentSender{Enabled: true},
			DM:      DMSender{Enabled: true},
		},
		Collector: Collectors{
			Live:    LiveCollector{},
			Comment: CommentCollector{},
			DM:      DMCollector{},
		},
	}
	if err := cfg.validateCredentials(true); err != nil {
		t.Fatalf("dry_run 不应要求B站凭据：%v", err)
	}
	cfg.Sender.DryRun = false
	if err := cfg.validateCredentials(true); err == nil {
		t.Fatal("关闭 dry_run 后缺少凭据应报错")
	}
}
