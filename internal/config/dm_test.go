package config

// 本文件测私信采集/发送的通道依赖与 all_followers 默认值。

import (
	"strings"
	"testing"
)

func TestDMSenderAllFollowersDefaultTrue(t *testing.T) {
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=x; bili_jct=token")
	body := "[channels.dm]\nenabled=true\n[sender]\n[sender.dm]\nenabled=true\n"
	cfg, err := loadWithScheduler(t, body)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if !cfg.Sender.DM.AllFollowersEnabled() {
		t.Error("未写 all_followers 时应默认 true")
	}
}

func TestDMSenderAllFollowersFalseRequiresWhitelist(t *testing.T) {
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=x; bili_jct=token")
	base := "[channels.dm]\nenabled=true\n[sender.dm]\nenabled=true\nall_followers=false\n"
	if _, err := loadWithScheduler(t, base); err == nil || !strings.Contains(err.Error(), "allowed_mids") {
		t.Fatalf("关闭 all_followers 且无白名单应拒绝，实际 %v", err)
	}
	cfg, err := loadWithScheduler(t, base+"allowed_mids=['313591854']\n")
	if err != nil {
		t.Fatalf("带白名单应通过：%v", err)
	}
	if cfg.Sender.DM.AllFollowersEnabled() {
		t.Error("显式 false 不应被默认值覆盖")
	}
}

func TestDMCollectorRequiresChannelCredentialAndSelfID(t *testing.T) {
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=x")
	base := "[collector.live]\nself_id='8'\n[channels.dm]\nenabled=true\n[collector.dm]\nenabled=true\n"
	if _, err := loadWithScheduler(t, base); err != nil {
		t.Fatalf("合法私信采集配置应通过：%v", err)
	}
	if _, err := loadWithScheduler(t, "[collector.live]\nself_id='8'\n[collector.dm]\nenabled=true\n"); err == nil || !strings.Contains(err.Error(), "channels.dm") {
		t.Fatalf("缺通道开关应拒绝，实际 %v", err)
	}
	if _, err := loadWithScheduler(t, "[channels.dm]\nenabled=true\n[collector.dm]\nenabled=true\n"); err == nil || !strings.Contains(err.Error(), "self_id") {
		t.Fatalf("缺 self_id 应拒绝，实际 %v", err)
	}
	t.Setenv("FISH_BILI_COOKIE", "")
	if _, err := loadWithScheduler(t, base); err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("缺凭据应拒绝，实际 %v", err)
	}
}

// TestDMCollectorStatePath 读取水位文件路径，供装配层跨重启保留游标。
func TestDMCollectorStatePath(t *testing.T) {
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=x; bili_jct=token")
	body := "[collector.live]\nself_id='8'\n[channels.dm]\nenabled=true\n[collector.dm]\nenabled=true\nstate_path='state/test-dm-cursors.json'\n"
	cfg, err := loadWithScheduler(t, body)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if cfg.Collector.DM.StatePath != "state/test-dm-cursors.json" {
		t.Fatalf("state_path 未生效：%q", cfg.Collector.DM.StatePath)
	}
}

func TestDMSenderRequiresChannel(t *testing.T) {
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=x; bili_jct=token")
	if _, err := loadWithScheduler(t, "[sender.dm]\nenabled=true\n"); err == nil || !strings.Contains(err.Error(), "channels.dm") {
		t.Fatalf("未开通道应拒绝发送，实际 %v", err)
	}
}

func TestQQChannelStillRejected(t *testing.T) {
	if _, err := loadWithScheduler(t, "[channels.qq]\nenabled=true\n"); err == nil || !strings.Contains(err.Error(), "QQ") {
		t.Fatalf("QQ 通道应仍被拒绝，实际 %v", err)
	}
}
