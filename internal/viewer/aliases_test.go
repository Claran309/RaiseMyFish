package viewer

// 本文件测观众别名/同音组的生产来源：文件解析、确认状态、热更新与认人链路。
//
// 关键回归（B3/N04）：
//   - 别名表不是「传了就生效」：只有 status=confirmed 参与认人，
//     candidate 留档不注入，rejected 作为负例抑制弱身份证据；
//   - 同音组只扩大近似候选（TierNear/phonetic），不算别名档，也不改写昵称；
//   - 文件按 mtime/大小热更新，坏文件保留旧快照；
//   - 陌生 UID 的 not-found 契约与仓储实现解耦：ErrNotFound / ErrViewerNotFound 都算「没找到」。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
)

// writeAliasContent 写别名文件并固定 mtime，保证热更新判定不依赖文件系统时钟粒度。
func writeAliasContent(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入别名文件失败：%v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("设置别名文件时间失败：%v", err)
	}
}

// singleAliasYAML 生成只含一个 confirmed 别名的文件内容。
func singleAliasYAML(alias, uid string) string {
	return fmt.Sprintf(`schema: fish-viewer-aliases/v1
aliases:
  - alias: %s
    uid: %q
    status: confirmed
`, alias, uid)
}

func assertSingleConfirmed(t *testing.T, idx AliasIndex, wantAlias string) {
	t.Helper()
	if len(idx.Confirmed) != 1 || idx.Confirmed[0].Alias != wantAlias {
		t.Fatalf("confirmed 别名不符合预期：%+v", idx.Confirmed)
	}
}

// TestParseAliasFileStatuses 三态分流：confirmed 注入、candidate 留档、rejected 记负例。
func TestParseAliasFileStatuses(t *testing.T) {
	doc := `schema: fish-viewer-aliases/v1
aliases:
  - alias: 长弓三溢
    uid: "5005"
    kind: typo
    status: confirmed
  - alias: 三亿
    uid: "5005"
    status: candidate
  - alias: 弓长三益的狗
    name: 弓长三益
    status: rejected
phonetic_groups:
  - words: ["真得", "贞德"]
    status: confirmed
  - words: ["测试甲", "测试乙"]
    status: candidate
`
	idx, err := parseAliasFile([]byte(doc))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(idx.Confirmed) != 1 {
		t.Fatalf("confirmed 条目数 = %d，期望 1：%+v", len(idx.Confirmed), idx.Confirmed)
	}
	entry := idx.Confirmed[0]
	if entry.Alias != "长弓三溢" || entry.UID != "5005" || entry.Kind != AliasKindTypo || entry.Status != AliasConfirmed {
		t.Errorf("confirmed 条目字段不符：%+v", entry)
	}
	if len(idx.Rejected) != 1 || idx.Rejected[0].Name != "弓长三益" || idx.Rejected[0].UID != "" {
		t.Errorf("rejected 条目不符：%+v", idx.Rejected)
	}
	if idx.Phonetic == nil {
		t.Fatal("confirmed 同音组应构建查询表")
	}
	if got := idx.Phonetic("真得"); len(got) != 1 || got[0] != "贞德" {
		t.Errorf("真得 的同音写法 = %v，期望 [贞德]", got)
	}
	if got := idx.Phonetic("贞德"); len(got) != 1 || got[0] != "真得" {
		t.Errorf("贞德 的同音写法 = %v，期望 [真得]", got)
	}
	if got := idx.Phonetic("测试甲"); got != nil {
		t.Errorf("candidate 同音组不应注入：%v", got)
	}
}

// TestParseAliasFileValidation 结构校验：坏文件必须整份拒绝，不能带病半加载。
func TestParseAliasFileValidation(t *testing.T) {
	schema := "schema: fish-viewer-aliases/v1\n"
	cases := map[string]string{
		"空文件":     "",
		"不是 YAML": "schema: [\n",
		"未知字段": schema + `aliases:
  - alias: 甲
    uid: "1"
    status: confirmed
    extra: x
`,
		"schema 不符": "schema: other/v1\naliases: []\n",
		"多文档":       schema + "aliases: []\n---\nfoo: bar\n",
		"别名为空": schema + `aliases:
  - alias: ""
    uid: "1"
    status: confirmed
`,
		"uid 与 name 同时填": schema + `aliases:
  - alias: 甲
    uid: "1"
    name: 乙
    status: confirmed
`,
		"uid 与 name 都为空": schema + `aliases:
  - alias: 甲
    status: confirmed
`,
		"uid 非数字": schema + `aliases:
  - alias: 甲
    uid: abc
    status: confirmed
`,
		"kind 非法": schema + `aliases:
  - alias: 甲
    uid: "1"
    kind: whatever
    status: confirmed
`,
		"status 缺失": schema + `aliases:
  - alias: 甲
    uid: "1"
`,
		"status 非法": schema + `aliases:
  - alias: 甲
    uid: "1"
    status: maybe
`,
		"重复条目": schema + `aliases:
  - alias: 甲
    uid: "1"
    status: confirmed
  - alias: 甲
    uid: "1"
    status: confirmed
`,
		"别名过长": schema + fmt.Sprintf(`aliases:
  - alias: %s
    uid: "1"
    status: confirmed
`, strings.Repeat("鱼", maxAliasRunes+1)),
		"别名只有装饰字符": schema + `aliases:
  - alias: "★★★"
    uid: "1"
    status: confirmed
`,
		"name 只有装饰字符": schema + `aliases:
  - alias: 甲
    name: "☆☆"
    status: confirmed
`,
		"同音组只有一个词": schema + `phonetic_groups:
  - words: ["真得"]
    status: confirmed
`,
		"同音组重复词": schema + `phonetic_groups:
  - words: ["真得", " 真得 "]
    status: confirmed
`,
		"同音组状态非法": schema + `phonetic_groups:
  - words: ["真得", "贞德"]
    status: maybe
`,
		"同音组词过长": schema + fmt.Sprintf(`phonetic_groups:
  - words: ["真得", %q]
    status: confirmed
`, strings.Repeat("鱼", maxAliasRunes+1)),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseAliasFile([]byte(doc)); err == nil {
				t.Fatal("坏文件应解析失败")
			}
		})
	}
}

// TestParseAliasFileEntryLimit 条目总数超过上限直接拒绝。
func TestParseAliasFileEntryLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("schema: fish-viewer-aliases/v1\naliases:\n")
	for i := 0; i <= maxAliasEntries; i++ {
		fmt.Fprintf(&b, "  - alias: 别名%d\n    uid: \"%d\"\n    status: candidate\n", i, 100000+i)
	}
	if _, err := parseAliasFile([]byte(b.String())); err == nil {
		t.Fatal("超过条目上限应报错")
	}
}

// TestNewAliasFileRejectsEmptyPath 空路径是配置错误，不能静默变成「无别名」。
func TestNewAliasFileRejectsEmptyPath(t *testing.T) {
	if _, err := NewAliasFile("  ", nil); err == nil {
		t.Fatal("空路径应报错")
	}
}

// TestShippedAliasFileParses 默认配置指向的仓库内别名文件必须是合法格式。
func TestShippedAliasFileParses(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "personas", "dafeiyu", "viewer_aliases.yaml"))
	if err != nil {
		t.Fatalf("读取默认别名文件失败：%v", err)
	}
	if _, err := parseAliasFile(data); err != nil {
		t.Fatalf("默认别名文件格式不合法：%v", err)
	}
}

// TestAliasFileHotReload 同长度内容靠 mtime 重载；坏文件保留旧快照并回调。
func TestAliasFileHotReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "viewer_aliases.yaml")
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeAliasContent(t, path, singleAliasYAML("长弓三溢", "5005"), base)

	var errs []error
	file, err := NewAliasFile(path, func(err error) { errs = append(errs, err) })
	if err != nil {
		t.Fatal(err)
	}
	assertSingleConfirmed(t, file.AliasIndex(), "长弓三溢")

	// 「溢」和「亿」同为三字节，文件大小不变，只能靠 mtime 发现变化。
	writeAliasContent(t, path, singleAliasYAML("长弓三亿", "5005"), base.Add(2*time.Second))
	assertSingleConfirmed(t, file.AliasIndex(), "长弓三亿")

	// 坏文件：保留旧快照，并通过回调告警。
	writeAliasContent(t, path, "schema: [坏掉的", base.Add(4*time.Second))
	assertSingleConfirmed(t, file.AliasIndex(), "长弓三亿")
	if len(errs) != 1 {
		t.Fatalf("坏文件应回调一次：%v", errs)
	}

	// 修好后再次热更新。
	writeAliasContent(t, path, singleAliasYAML("夜猫子甲", "5006"), base.Add(6*time.Second))
	assertSingleConfirmed(t, file.AliasIndex(), "夜猫子甲")
	if len(errs) != 1 {
		t.Errorf("修复后不应继续告警：%v", errs)
	}
}

// TestAliasFileMissingThenAppears 文件后补也要在下次查询生效，且文件缺失不算错误。
func TestAliasFileMissingThenAppears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "viewer_aliases.yaml")
	var errs []error
	file, err := NewAliasFile(path, func(err error) { errs = append(errs, err) })
	if err != nil {
		t.Fatal(err)
	}
	if idx := file.AliasIndex(); len(idx.Confirmed) != 0 || len(errs) != 0 {
		t.Fatalf("文件缺失应返回空快照且不告警：%+v / %v", idx, errs)
	}
	writeAliasContent(t, path, singleAliasYAML("长弓三溢", "5005"), time.Now().Add(-time.Hour))
	assertSingleConfirmed(t, file.AliasIndex(), "长弓三溢")
}

// TestFindAliasStatusGating 只有 confirmed 别名参与认人。
func TestFindAliasStatusGating(t *testing.T) {
	ctx := context.Background()
	pool := []store.ViewerProfile{namedProfile("5005", "弓长三益")}
	cases := map[string]AliasStatus{
		"confirmed": AliasConfirmed,
		"candidate": AliasCandidate,
	}
	for name, status := range cases {
		t.Run(name, func(t *testing.T) {
			s := newTestSearcher(t, &identitySource{pool: pool}, nil, SearchOptions{
				Aliases: []Alias{{Alias: "长弓三溢", UID: "5005", Status: status}},
			})
			got, err := s.Find(ctx, "长弓三溢", 5)
			if err != nil {
				t.Fatalf("检索失败：%v", err)
			}
			if status == AliasConfirmed {
				if got.Status != StatusFound || len(got.Candidates) != 1 {
					t.Fatalf("confirmed 别名应命中：%+v", got)
				}
				c := got.Candidates[0]
				if c.Profile.UID != "5005" || c.Tier != TierAlias || c.Reason != "alias_exact" {
					t.Errorf("别名档位不符：%+v", c)
				}
				return
			}
			if got.Status != StatusNotFound || len(got.Candidates) != 0 {
				t.Fatalf("candidate 别名不应参与认人：%+v", got)
			}
		})
	}
}

// TestFindRejectedAliasSuppressesWeakEvidence rejected 别名压制近似命中，但不影响昵称精确。
func TestFindRejectedAliasSuppressesWeakEvidence(t *testing.T) {
	ctx := context.Background()
	pool := []store.ViewerProfile{namedProfile("5005", "弓长三益")}

	baseline := newTestSearcher(t, &identitySource{pool: pool}, nil, SearchOptions{})
	got, err := baseline.Find(ctx, "弓长三溢", 5)
	if err != nil {
		t.Fatalf("基线检索失败：%v", err)
	}
	if got.Status != StatusFound || got.Candidates[0].Tier != TierNear {
		t.Fatalf("基线的编辑距离命中不符合预期：%+v", got)
	}

	rejected := newTestSearcher(t, &identitySource{pool: pool}, nil, SearchOptions{
		Aliases: []Alias{{Alias: "弓长三溢", UID: "5005", Status: AliasRejected}},
	})
	got, err = rejected.Find(ctx, "弓长三溢", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusNotFound {
		t.Fatalf("rejected 后不应再近似命中：%+v", got)
	}

	// 负例只否定「这个别名指向此人」，不否定本人当前昵称。
	got, err = rejected.Find(ctx, "弓长三益", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusFound || got.Candidates[0].Tier != TierName {
		t.Fatalf("当前昵称精确命中不应被负例压制：%+v", got)
	}
}

// TestFindPhoneticGroupWithoutSharedChars 同音组让完全不共字的昵称也能成为候选。
func TestFindPhoneticGroupWithoutSharedChars(t *testing.T) {
	ctx := context.Background()
	pool := []store.ViewerProfile{namedProfile("7101", "真得")}
	phonetic := func(word string) []string {
		if normalizeIdentity(word) == "贞德" {
			return []string{"真得"}
		}
		return nil
	}

	without := newTestSearcher(t, &identitySource{pool: pool}, nil, SearchOptions{})
	got, err := without.Find(ctx, "贞德", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusNotFound {
		t.Fatalf("没有同音组时不应凭猜测认人：%+v", got)
	}

	with := newTestSearcher(t, &identitySource{pool: pool}, nil, SearchOptions{Phonetic: phonetic})
	got, err = with.Find(ctx, "贞德", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusFound || len(got.Candidates) != 1 {
		t.Fatalf("同音组应命中一条候选：%+v", got)
	}
	c := got.Candidates[0]
	if c.Profile.UID != "7101" || c.Tier != TierNear || c.Reason != "phonetic" {
		t.Errorf("同音档位不符：%+v", c)
	}
}

// TestFindPhoneticGroupKeepsAmbiguous 同音多人必须保留歧义，不能替用户合并身份。
func TestFindPhoneticGroupKeepsAmbiguous(t *testing.T) {
	pool := []store.ViewerProfile{
		namedProfile("7201", "贞德"),
		namedProfile("7202", "甄德"),
	}
	phonetic := func(word string) []string {
		if normalizeIdentity(word) == "真得" {
			return []string{"贞德", "甄德"}
		}
		return nil
	}
	s := newTestSearcher(t, &identitySource{pool: pool}, nil, SearchOptions{Phonetic: phonetic})
	got, err := s.Find(context.Background(), "真得", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusAmbiguous || len(got.Candidates) != 2 {
		t.Fatalf("同音多人应判歧义：%+v", got)
	}
	for _, c := range got.Candidates {
		if c.Tier != TierNear || c.Reason != "phonetic" {
			t.Errorf("同音候选档位不符：%+v", c)
		}
	}
}

// TestFindAliasFileHotReload 认人链路直接读文件快照：两次查询之间改文件立即生效。
func TestFindAliasFileHotReload(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "viewer_aliases.yaml")
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeAliasContent(t, path, singleAliasYAML("长弓三溢", "5005"), base)
	file, err := NewAliasFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestSearcher(t, &identitySource{pool: []store.ViewerProfile{namedProfile("5005", "弓长三益")}}, nil, SearchOptions{
		AliasSource: file,
	})

	got, err := s.Find(ctx, "长弓三溢", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusFound || got.Candidates[0].Tier != TierAlias {
		t.Fatalf("旧别名应命中：%+v", got)
	}

	writeAliasContent(t, path, singleAliasYAML("长弓三亿", "5005"), base.Add(2*time.Second))
	got, err = s.Find(ctx, "长弓三亿", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusFound || got.Candidates[0].Tier != TierAlias {
		t.Fatalf("热更新后的别名应命中：%+v", got)
	}
	got, err = s.Find(ctx, "长弓三溢", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusNotFound {
		t.Fatalf("被移除的旧别名不应再命中：%+v", got)
	}
}

// getProfileErrorSource 用可控错误替代 UID 精确查询，模拟不同仓储实现的 not-found 契约。
type getProfileErrorSource struct {
	identitySource
	err error
}

func (f *getProfileErrorSource) GetProfile(context.Context, string) (store.ViewerProfile, error) {
	return store.ViewerProfile{}, f.err
}

// TestFindUnknownUIDIsUnifiedNotFound 陌生 UID 必须是 not_found，不是工具错误。
func TestFindUnknownUIDIsUnifiedNotFound(t *testing.T) {
	cases := map[string]error{
		"通用 ErrNotFound":       store.ErrNotFound,
		"观众 ErrViewerNotFound": store.ErrViewerNotFound,
		"包装后的观众 not-found":     fmt.Errorf("读取档案失败: %w", store.ErrViewerNotFound),
	}
	for name, getErr := range cases {
		t.Run(name, func(t *testing.T) {
			s := newTestSearcher(t, &getProfileErrorSource{err: getErr}, nil, SearchOptions{})
			got, err := s.Find(context.Background(), "999999", 5)
			if err != nil {
				t.Fatalf("陌生 UID 不能变成错误：%v", err)
			}
			if got.Status != StatusNotFound || len(got.Candidates) != 0 {
				t.Fatalf("陌生 UID 应为 not_found：%+v", got)
			}
		})
	}
}

// TestFindUnknownUIDPropagatesRealFailure 真正的查询故障仍要上抛，不能伪装成「没这个人」。
func TestFindUnknownUIDPropagatesRealFailure(t *testing.T) {
	boom := errors.New("数据库连接断了")
	s := newTestSearcher(t, &getProfileErrorSource{err: boom}, nil, SearchOptions{})
	if _, err := s.Find(context.Background(), "999999", 5); !errors.Is(err, boom) {
		t.Fatalf("真实故障应上抛：%v", err)
	}
}
