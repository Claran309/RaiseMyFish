package knowledgeeval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
)

type FileHash struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type Freeze struct {
	FrozenAt           string     `json:"frozen_at"`
	CasesSHA256        string     `json:"cases_sha256"`
	ACLSHA256          string     `json:"acl_fixture_sha256"`
	OrderSHA256        string     `json:"order_fixture_sha256"`
	Corpus             []FileHash `json:"corpus"`
	Code               []FileHash `json:"code"`
	ProductionRevision string     `json:"production_revision"`
	SyntheticRevision  string     `json:"synthetic_revision"`
	OrderRevision      string     `json:"order_revision"`
	Annotation         string     `json:"annotation"`
}
type OrderResult struct {
	ExpectedIDs []string `json:"expected_ids"`
	ActualIDs   []string `json:"actual_ids"`
	Repeats     int      `json:"repeats"`
	Revision    string   `json:"revision"`
	Passed      bool     `json:"passed"`
}
type OfflineReport struct {
	Freeze Freeze      `json:"freeze"`
	Report Report      `json:"report"`
	Order  OrderResult `json:"top3_order"`
	Passed bool        `json:"passed"`
}

func sum(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func revision(files map[string][]byte) string {
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("fish-knowledge/v1\n")
	for _, name := range names {
		fmt.Fprintf(&b, "%s\t%s\n", name, sum(files[name]))
	}
	return sum([]byte(b.String()))
}
func hashList(files map[string][]byte) []FileHash {
	var result []FileHash
	for name, data := range files {
		result = append(result, FileHash{Path: name, SHA256: sum(data)})
	}
	slices.SortFunc(result, func(a, b FileHash) int { return strings.Compare(a.Path, b.Path) })
	return result
}
func corpusBytes(dir string) (map[string][]byte, error) {
	names := []string{"lore.md", "memes.md", "viewers.md", "games.md"}
	matches, err := filepath.Glob(filepath.Join(dir, "memes", "batch-*.md"))
	if err != nil {
		return nil, err
	}
	for _, path := range matches {
		names = append(names, "memes/"+filepath.Base(path))
	}
	result := map[string][]byte{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			return nil, fmt.Errorf("读取公开快照%s：%w", name, err)
		}
		result[name] = data
	}
	return result, nil
}
func writeSnapshot(root string, files map[string][]byte) error {
	for name, data := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			return err
		}
	}
	return nil
}
func codeHashes() ([]FileHash, error) {
	result := map[string][]byte{}
	for _, dir := range []string{"internal/knowledge", "internal/brain", "internal/brain/provider", "internal/knowledgeeval", "cmd/knowledgeeval"} {
		paths, err := filepath.Glob(filepath.Join(filepath.FromSlash(dir), "*.go"))
		if err != nil {
			return nil, err
		}
		if len(paths) == 0 {
			return nil, errors.New("请从仓库根目录运行离线评测")
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("读取代码快照：%w", err)
			}
			result[filepath.ToSlash(path)] = data
		}
	}
	return hashList(result), nil
}
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("保存离线证据：%w", err)
	}
	return nil
}

// RunOffline 先独占创建输出、冻结所有输入hash，再用内存中同一批原字节构造临时Reader。
// 输出只含ID/范围/标签/版本/预算摘要，不保存知识正文、完整prompt或本机绝对源路径。
func RunOffline(ctx context.Context, corpus, casesPath, output string) (OfflineReport, error) {
	var out OfflineReport
	if ctx == nil {
		return out, errors.New("ctx不能为空")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := os.Mkdir(output, 0700); err != nil {
		return out, fmt.Errorf("新建独占输出目录（已有目录拒绝）：%w", err)
	}
	data, err := os.ReadFile(casesPath)
	if err != nil {
		return out, fmt.Errorf("读取冻结用例：%w", err)
	}
	cases, err := ReadCases(bytes.NewReader(data))
	if err != nil {
		return out, err
	}
	fixtureDir := filepath.Dir(casesPath)
	acl, err := os.ReadFile(filepath.Join(fixtureDir, "batch-synthetic-acl.md"))
	if err != nil {
		return out, fmt.Errorf("读取ACL夹具：%w", err)
	}
	order, err := os.ReadFile(filepath.Join(fixtureDir, "batch-synthetic-order.md"))
	if err != nil {
		return out, fmt.Errorf("读取排序夹具：%w", err)
	}
	files, err := corpusBytes(corpus)
	if err != nil {
		return out, err
	}
	synthetic := map[string][]byte{}
	for name, data := range files {
		synthetic[name] = data
	}
	const aclName = "memes/batch-synthetic-acl.md"
	if _, exists := synthetic[aclName]; exists {
		return out, errors.New("公开库包含保留的合成夹具路径")
	}
	synthetic[aclName] = acl
	empty := []byte("---\nschema: fish-knowledge/v1\nentries: []\n---\n")
	ordering := map[string][]byte{"lore.md": empty, "memes.md": empty, "viewers.md": empty, "games.md": empty, "memes/batch-synthetic-order.md": order}
	code, err := codeHashes()
	if err != nil {
		return out, err
	}
	out.Freeze = Freeze{FrozenAt: time.Now().UTC().Format(time.RFC3339Nano), CasesSHA256: sum(data), ACLSHA256: sum(acl), OrderSHA256: sum(order), Corpus: hashList(files), Code: code, ProductionRevision: revision(files), SyntheticRevision: revision(synthetic), OrderRevision: revision(ordering), Annotation: "开发助手按公开title/usage预先标注；非DSH独立标注。精确标题20、含标题自然句20、无目标标题语义场景20、alias0；不根据输出补标。私密/场景fixture全为合成数据，镜像与排序另计，不进入100条质量分母。"}
	if err := writeJSON(filepath.Join(output, "freeze.json"), out.Freeze); err != nil {
		return out, err
	}
	// freeze.json已落盘；首次真实Reader调用严格在此之后。
	temp, err := os.MkdirTemp("", "fish-knowledgeeval-")
	if err != nil {
		return out, fmt.Errorf("创建临时快照：%w", err)
	}
	defer os.RemoveAll(temp)
	pubDir, synDir, ordDir := filepath.Join(temp, "public"), filepath.Join(temp, "synthetic"), filepath.Join(temp, "order")
	for _, item := range []struct {
		dir   string
		files map[string][]byte
	}{{pubDir, files}, {synDir, synthetic}, {ordDir, ordering}} {
		if err := writeSnapshot(item.dir, item.files); err != nil {
			return out, fmt.Errorf("写临时快照：%w", err)
		}
	}
	publicReader, err := knowledge.NewFileReader(ctx, pubDir)
	if err != nil {
		return out, err
	}
	syntheticReader, err := knowledge.NewFileReader(ctx, synDir)
	if err != nil {
		return out, err
	}
	orderReader, err := knowledge.NewFileReader(ctx, ordDir)
	if err != nil {
		return out, err
	}
	report, err := (Evaluator{Public: publicReader, Synthetic: syntheticReader, Budget: true}).Run(ctx, cases)
	if err != nil {
		return out, err
	}
	out.Report = report
	for _, row := range report.Cases {
		want := out.Freeze.ProductionRevision
		if row.Kind == "permission" {
			want = out.Freeze.SyntheticRevision
		}
		if row.Revision != want {
			return out, errors.New("实际Reader版本与冻结原字节不符")
		}
	}
	out.Order = OrderResult{ExpectedIDs: []string{"sort-title", "sort-a", "sort-b"}, Repeats: 10, Revision: out.Freeze.OrderRevision, Passed: true}
	for range out.Order.Repeats {
		got, err := recall(ctx, orderReader, knowledge.Query{Scope: knowledge.Scope{Channel: event.ChLive, Session: "synthetic-order"}, Text: "排序碰撞测试"})
		if err != nil {
			return out, err
		}
		ids := []string{}
		for _, item := range got.Items {
			ids = append(ids, item.ID)
		}
		out.Order.ActualIDs = ids
		if got.Revision != out.Freeze.OrderRevision || !slices.Equal(ids, out.Order.ExpectedIDs) {
			out.Order.Passed = false
		}
	}
	out.Passed = out.Report.Passed && out.Order.Passed
	// 结果后只核对输入原字节不变，不再次取代本次快照或重算分数。
	after, err := corpusBytes(corpus)
	if err != nil {
		return out, err
	}
	afterCases, err := os.ReadFile(casesPath)
	if err != nil {
		return out, err
	}
	afterCode, err := codeHashes()
	if err != nil {
		return out, err
	}
	if revision(after) != out.Freeze.ProductionRevision || sum(afterCases) != out.Freeze.CasesSHA256 || !slices.Equal(afterCode, code) {
		return out, errors.New("评测期间输入快照发生变化，结果不得标为有效")
	}
	if err := writeJSON(filepath.Join(output, "results.json"), out); err != nil {
		return out, err
	}
	if err := os.WriteFile(filepath.Join(output, "report.md"), []byte(markdown(out)), 0600); err != nil {
		return out, err
	}
	return out, nil
}

func markdown(out OfflineReport) string {
	var b strings.Builder
	m := out.Report.Metrics
	b.WriteString("# K0-7 离线检索开发评测\n\n")
	precision := "N/A"
	if m.Precision != nil {
		precision = fmt.Sprintf("%.2f%%", *m.Precision*100)
	}
	fmt.Fprintf(&b, "质量门禁：%t；总体结构与质量通过：%t。相关条目/全部实际返回条目 = %d/%d = %s。正例覆盖 %d/60；负例误召回 %d/20；越界条目 %d；合法镜像命中 %d/20；逐条Context.Validate和Top3检查通过；预算 %d/100。\n\n", out.Report.Passed, out.Passed, m.Relevant, m.Returned, precision, m.PositiveHits, m.NegativeFalse, m.ForbiddenReturns, m.ControlsPassed, m.BudgetPassed)
	b.WriteString("阈值：相关率≥80%，正例覆盖≥48/60，负例误召回≤2/20，越界0，镜像控制20/20。无返回相关率记N/A；镜像/排序不进入质量分母。\n\n")
	b.WriteString("| 正例形式 | 条数 | 命中预期 | 相关/返回 |\n|---|---:|---:|---:|\n")
	for _, form := range []string{"exact_title", "natural_title", "natural_paraphrase", "alias"} {
		n, h, r, total := 0, 0, 0, 0
		for _, c := range out.Report.Cases {
			if c.Kind == "positive" && c.Form == form {
				n++
				if c.PositiveHit {
					h++
				}
				r += c.Relevant
				total += len(c.ActualIDs)
			}
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d/%d |\n", form, n, h, r, total)
	}
	fmt.Fprintf(&b, "\n生产Revision：`%s`；合成权限Revision：`%s`。\n\n合成排序：预期 `%s`，实际 `%s`，重复%d次，通过=%t；只检查标题>关键词、priority及ID稳定排序，不代表自然查询质量。\n\n", out.Freeze.ProductionRevision, out.Freeze.SyntheticRevision, strings.Join(out.Order.ExpectedIDs, ", "), strings.Join(out.Order.ActualIDs, ", "), out.Order.Repeats, out.Order.Passed)
	b.WriteString("预算：真实Brain + 显式Knowledge，固定合成人格，MaxPromptTokens=32768、10%；每条在内存fake Provider/Usage执行候选与空知识两次，独立比较最终消息字节增量与审计（含转义），静态system保持一致。未发送网络请求，fake服务端usage及费用均未知；没有真实模型或PG调用，不能代表真实35条人格开关对照或生产消费链路。\n\n标注局限：开发助手预先按title/usage审读；非DSH独立标注；标题20、含标题自然句20、无目标标题语义场景20、alias0（500条keywords为空）。语义场景保留词面Reader当前不支持的需求，不把标题换皮称语义泛化。未列入预期的返回保守记不相关；未根据结果补标或更改Reader。全部私密资料明确合成，不含真实用户事实。\n\n")
	b.WriteString("| ID | 类型/形式 | scope | 预期相关ID | 实际ID | 相关 | 命中/误召回/越界 | 控制ID | 知识字节/预算 |\n|---|---|---|---|---|---:|---|---|---|\n")
	for _, row := range out.Report.Cases {
		budget := "N/A"
		if row.Budget != nil {
			budget = fmt.Sprintf("%d/%d", row.Budget.Bytes, row.Budget.Cap)
		}
		fmt.Fprintf(&b, "| %s | %s/%s | %s/%s/private=%t | %s | %s | %d | %t/%t/%d | %s | %s |\n", row.ID, row.Kind, row.Form, row.Scope.Channel, row.Scope.Session, row.Scope.Private, strings.Join(row.ExpectedIDs, ", "), strings.Join(row.ActualIDs, ", "), row.Relevant, row.PositiveHit, row.NegativeFalse, row.ForbiddenReturns, strings.Join(row.ControlIDs, ", "), budget)
	}
	b.WriteString("\n仅为开发侧离线子集交付。K0-7正式35条开关对照、真实PG验收、DSH独立复核及扩量均未执行；质量不足时停止，不改冻结题或Reader追分。\n")
	return b.String()
}
