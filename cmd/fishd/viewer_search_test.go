package main

// 本文件测观众检索的生产装配：buildViewerSearcher 是否真的把别名/同音表
// 与统一的 not-found 契约接进了认人链路（不是只给 fake 开小灶）。
//
// 验收口径（B3/N04）：
//   - 别名文件里的 confirmed 条目生效，candidate 不生效；
//   - 文件热更新在两次查询之间生效；
//   - 陌生 UID 在仓储返回 ErrViewerNotFound / ErrNotFound 时都是 not_found，
//     真正的查询故障仍然是错误。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
	"go.uber.org/zap"
)

// viewerSearchStub 模拟 ViewerRepo：身份/事实关键词、活跃池与 UID 精确查询。
type viewerSearchStub struct {
	profiles []store.ViewerProfile
	getErr   error
}

func (s *viewerSearchStub) KeywordProfiles(_ context.Context, kind viewer.EmbeddingKind, keyword string, limit int) ([]store.ViewerProfileHit, error) {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	var hits []store.ViewerProfileHit
	for _, profile := range s.profiles {
		var values []string
		switch kind {
		case viewer.KindIdentity:
			values = []string{profile.Name}
		case viewer.KindFacts:
			values = profile.RecentTexts
		}
		for _, value := range values {
			if keyword != "" && strings.Contains(strings.ToLower(value), keyword) {
				hits = append(hits, store.ViewerProfileHit{Profile: profile})
				break
			}
		}
	}
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (s *viewerSearchStub) SemanticProfiles(context.Context, viewer.EmbeddingKind, []float64, int) ([]store.ViewerProfileHit, error) {
	return nil, nil
}

func (s *viewerSearchStub) GetProfile(_ context.Context, uid string) (store.ViewerProfile, error) {
	if s.getErr != nil {
		return store.ViewerProfile{}, s.getErr
	}
	for _, profile := range s.profiles {
		if profile.UID == uid {
			return profile, nil
		}
	}
	return store.ViewerProfile{}, store.ErrViewerNotFound
}

func (s *viewerSearchStub) TopViewers(_ context.Context, limit int) ([]store.ViewerProfile, error) {
	if limit > 0 && len(s.profiles) > limit {
		return s.profiles[:limit], nil
	}
	return s.profiles, nil
}

func viewerSearchProfile(uid, name string) store.ViewerProfile {
	return store.ViewerProfile{
		UID: uid, Name: name, Hits: 5,
		LastSeen:    time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
		RecentTexts: []string{"今天也在"},
	}
}

// writeViewerAliasFile 写别名文件并固定 mtime，保证热更新判定稳定。
func writeViewerAliasFile(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入别名文件失败：%v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("设置别名文件时间失败：%v", err)
	}
}

func viewerSearchConfig(aliasFile string) config.Config {
	return config.Config{
		Viewer:    config.Viewer{Enabled: true, AliasFile: aliasFile},
		Embedding: config.Embedding{Alpha: 0.5, MinSimilarity: 0.6},
	}
}

// TestBuildViewerSearcherInjectsAliasFile 生产装配下 confirmed 别名与同音组生效。
func TestBuildViewerSearcherInjectsAliasFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "viewer_aliases.yaml")
	doc := `schema: fish-viewer-aliases/v1
aliases:
  - alias: 长弓三溢
    uid: "5005"
    status: confirmed
  - alias: 长弓三亿
    uid: "5005"
    status: candidate
phonetic_groups:
  - words: ["贞德", "真得"]
    status: confirmed
`
	writeViewerAliasFile(t, path, doc, time.Now().Add(-time.Hour))
	source := &viewerSearchStub{profiles: []store.ViewerProfile{
		viewerSearchProfile("5005", "弓长三益"),
		viewerSearchProfile("5006", "真得"),
	}}
	searcher, err := buildViewerSearcher(viewerSearchConfig(path), source, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("生产装配失败：%v", err)
	}
	ctx := context.Background()

	got, err := searcher.Find(ctx, "长弓三溢", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != viewer.StatusFound || len(got.Candidates) == 0 {
		t.Fatalf("confirmed 别名应命中：%+v", got)
	}
	if c := got.Candidates[0]; c.Profile.UID != "5005" || c.Tier != viewer.TierAlias {
		t.Errorf("别名档位不符：%+v", c)
	}

	got, err = searcher.Find(ctx, "长弓三亿", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != viewer.StatusNotFound {
		t.Fatalf("candidate 别名不应生效：%+v", got)
	}

	got, err = searcher.Find(ctx, "贞德", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != viewer.StatusFound || len(got.Candidates) == 0 {
		t.Fatalf("同音组应命中：%+v", got)
	}
	if c := got.Candidates[0]; c.Profile.UID != "5006" || c.Tier != viewer.TierNear || c.Reason != "phonetic" {
		t.Errorf("同音档位不符：%+v", c)
	}

	got, err = searcher.Find(ctx, "999999", 5)
	if err != nil {
		t.Fatalf("陌生 UID 不能变成错误：%v", err)
	}
	if got.Status != viewer.StatusNotFound {
		t.Fatalf("陌生 UID 应为 not_found：%+v", got)
	}

	got, err = searcher.Find(ctx, "完全不认识的陌生人", 5)
	if err != nil {
		t.Fatalf("陌生名字不能变成错误：%v", err)
	}
	if got.Status != viewer.StatusNotFound {
		t.Fatalf("陌生名字应为 not_found：%+v", got)
	}
}

// TestBuildViewerSearcherTreatsNotFoundAsNoMatch 仓储两种 not-found 表达都按「没找到」处理。
func TestBuildViewerSearcherTreatsNotFoundAsNoMatch(t *testing.T) {
	cases := map[string]error{
		"通用 ErrNotFound":       store.ErrNotFound,
		"观众 ErrViewerNotFound": store.ErrViewerNotFound,
		"包装后的观众 not-found":     fmt.Errorf("读取档案失败: %w", store.ErrViewerNotFound),
	}
	for name, getErr := range cases {
		t.Run(name, func(t *testing.T) {
			source := &viewerSearchStub{getErr: getErr}
			searcher, err := buildViewerSearcher(viewerSearchConfig(""), source, nil, zap.NewNop())
			if err != nil {
				t.Fatalf("生产装配失败：%v", err)
			}
			got, err := searcher.Find(context.Background(), "999999", 5)
			if err != nil {
				t.Fatalf("陌生 UID 不能变成错误：%v", err)
			}
			if got.Status != viewer.StatusNotFound {
				t.Fatalf("陌生 UID 应为 not_found：%+v", got)
			}
		})
	}

	boom := errors.New("数据库连接断了")
	searcher, err := buildViewerSearcher(viewerSearchConfig(""), &viewerSearchStub{getErr: boom}, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("生产装配失败：%v", err)
	}
	if _, err := searcher.Find(context.Background(), "999999", 5); !errors.Is(err, boom) {
		t.Fatalf("真实故障应上抛：%v", err)
	}
}

// TestBuildViewerSearcherAliasHotReload 生产装配下改别名文件，下次查询立即生效。
func TestBuildViewerSearcherAliasHotReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "viewer_aliases.yaml")
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeViewerAliasFile(t, path, `schema: fish-viewer-aliases/v1
aliases:
  - alias: 长弓三溢
    uid: "5005"
    status: confirmed
`, base)
	source := &viewerSearchStub{profiles: []store.ViewerProfile{
		viewerSearchProfile("5005", "弓长三益"),
	}}
	searcher, err := buildViewerSearcher(viewerSearchConfig(path), source, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("生产装配失败：%v", err)
	}
	ctx := context.Background()

	got, err := searcher.Find(ctx, "长弓三溢", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != viewer.StatusFound || got.Candidates[0].Tier != viewer.TierAlias {
		t.Fatalf("旧别名应命中：%+v", got)
	}

	writeViewerAliasFile(t, path, `schema: fish-viewer-aliases/v1
aliases:
  - alias: 长弓三亿
    uid: "5005"
    status: confirmed
`, base.Add(time.Minute))
	got, err = searcher.Find(ctx, "长弓三亿", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != viewer.StatusFound || got.Candidates[0].Tier != viewer.TierAlias {
		t.Fatalf("热更新后的别名应命中：%+v", got)
	}
	got, err = searcher.Find(ctx, "长弓三溢", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != viewer.StatusNotFound {
		t.Fatalf("被移除的旧别名不应再命中：%+v", got)
	}
}

// TestBuildViewerAliasSourceDisabled 未配置别名文件时不构造来源，其余识别能力保留。
func TestBuildViewerAliasSourceDisabled(t *testing.T) {
	if source := buildViewerAliasSource(config.Viewer{}, zap.NewNop()); source != nil {
		t.Fatalf("空路径不应构造别名来源：%+v", source)
	}
}
