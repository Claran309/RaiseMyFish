package knowledge

// 梗白学习：从最近窗口的公开事件里发现知识库未收录的重复短语，经模型确认后
// 追加到 memes/learned.md。
//
// 边界：
//   - 只读公开入向事件，私密内容在查询层就已排除；
//   - 模型只负责判断与措辞，落盘前仍按 K0 规则完整解析校验；
//   - learned.md 由机器维护，同 title 幂等；每日条数可选，用 ID 的日期前缀持久化限流（0=不限）。
//   - **去重口径是「精确词表」不是「召回结果」**：Recall 是相关性检索，
//     语义相近的不同梗也会命中，用它判断「已收录」会把新梗永久丢掉（M09）。
//     现在只比较知识库的 title/keywords/tags 是否字面相等。

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
	"go.yaml.in/yaml/v3"
)

const (
	memeLearnFileName = "learned.md"
	memeLearnSource   = "meme-learn:auto"
	memeLearnSummary  = "来源：直播公开弹幕里的重复用语，由模型判断后自动收录。"
	memeLearnPriority = 40
	// maxMemeScanEvents 是单轮扫描的事件上限，避免一场直播越积越多拖垮学习循环。
	maxMemeScanEvents = 2000
	// maxMemeTokenRunes 跳过超长连续文本：它们不是「短词/短语」，也会让 n-gram 数量失控。
	maxMemeTokenRunes = 64
	// maxMemeSourceRunes 限制单轮扫过的正文总长，防止异常数据把候选表撑爆。
	maxMemeSourceRunes = 200000
	memeRejectTTL      = 24 * time.Hour
	// learned.md 的容量上限：超了就从最旧的开始删（按 id 里的日期前缀排序）。
	maxLearnedEntries = 200
	maxLearnedBytes   = 256 << 10
)

var memeURLPattern = regexp.MustCompile(`(?i)https?://\S+`)

// MemeEventSource 是学习器需要的最小公开事件查询能力。
type MemeEventSource interface {
	PublicSince(ctx context.Context, ch event.Channel, since time.Time, limit int) ([]event.Event, error)
}

// MemeLLM 是学习器需要的最小模型能力（由 provider 适配，测试用 mock）。
type MemeLLM interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// MemeLearnOptions 是学习循环的参数。
type MemeLearnOptions struct {
	// Window 是回看时长（默认 24h）。
	Window time.Duration
	// MinCount 是候选至少出现的次数（默认 2）。
	MinCount int
	// HotLimit 是单轮按弹幕出现次数排序后最多判断几个候选（默认 15）。
	HotLimit int
	// MaxPerDay 是每个 UTC 自然日最多学习几条；0 表示不限（默认）。
	MaxPerDay int
	// Channel 是扫描的通道（默认 live）。
	Channel event.Channel
	// Now 可注入时钟（测试用）。
	Now func() time.Time
}

// MemeLearnStats 是一轮学习的统计；不包含任何弹幕正文或模型原文。
type MemeLearnStats struct {
	Scanned    int
	Candidates int
	Learned    int
	Duplicated int
	Skipped    int
	Limited    int
}

type memeCandidate struct {
	text    string
	count   int
	samples []string
}

type learnedMeme struct {
	Title string
	Usage string
	Tags  []string
}

// MemeLearner 串行执行「扫事件 → 提候选 → 问模型 → 追加文件」，不并发写同一文件。
type MemeLearner struct {
	dir    string
	reader Reader
	source MemeEventSource
	llm    MemeLLM
	opts   MemeLearnOptions

	mu       sync.Mutex
	rejected map[string]time.Time
}

// NewMemeLearner 装配学习器；reader 用于排除知识库已有词，source 与 llm 缺一不可。
func NewMemeLearner(dir string, reader Reader, source MemeEventSource, llm MemeLLM, opts MemeLearnOptions) (*MemeLearner, error) {
	if !textValid(dir) {
		return nil, errors.New("梗白学习目录不能为空")
	}
	if reader == nil || source == nil || llm == nil {
		return nil, errors.New("梗白学习依赖不能为空")
	}
	if opts.Window <= 0 {
		opts.Window = 24 * time.Hour
	}
	if opts.Window < time.Hour || opts.Window > 7*24*time.Hour {
		return nil, errors.New("梗白学习回看窗口必须在1小时至7天之间")
	}
	if opts.MinCount <= 0 {
		opts.MinCount = 2
	}
	if opts.MinCount > 20 {
		return nil, errors.New("梗白学习最低出现次数不能超过20")
	}
	if opts.HotLimit <= 0 {
		opts.HotLimit = 15
	}
	if opts.HotLimit > 50 {
		return nil, errors.New("梗白学习单轮候选上限不能超过50")
	}
	if opts.MaxPerDay < 0 {
		return nil, errors.New("梗白学习每日上限不能为负")
	}
	if opts.MaxPerDay > 100 {
		return nil, errors.New("梗白学习每日上限不能超过100")
	}
	if opts.Channel == "" {
		opts.Channel = event.ChLive
	}
	if !opts.Channel.Valid() {
		return nil, errors.New("梗白学习通道无效")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &MemeLearner{dir: dir, reader: reader, source: source, llm: llm, opts: opts, rejected: make(map[string]time.Time)}, nil
}

// RunOnce 执行一轮学习；基础设施错误返回 error，单个候选的模型失败只跳过。
func (l *MemeLearner) RunOnce(ctx context.Context) (MemeLearnStats, error) {
	var stats MemeLearnStats
	if l == nil {
		return stats, errors.New("梗白学习器不能为空")
	}
	if err := checkContext(ctx); err != nil {
		return stats, err
	}
	now := l.opts.Now()
	events, err := l.source.PublicSince(ctx, l.opts.Channel, now.Add(-l.opts.Window), maxMemeScanEvents)
	if err != nil {
		return stats, fmt.Errorf("读取梗白事件窗口：%w", err)
	}
	stats.Scanned = len(events)
	candidates := extractMemeCandidates(events, l.opts.MinCount, l.opts.HotLimit)
	stats.Candidates = len(candidates)
	if len(candidates) == 0 {
		return stats, nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	path := filepath.Join(l.dir, "memes", memeLearnFileName)
	doc, entries, err := loadMemeDoc(ctx, path)
	if err != nil {
		return stats, err
	}
	titles := make(map[string]bool, len(entries))
	titleList := make([]string, 0, len(entries))
	for _, e := range entries {
		key := normalize(e.title)
		titles[key] = true
		titleList = append(titleList, key)
	}
	today := now.UTC().Format("20060102")
	learnedToday := 0
	for _, e := range entries {
		if strings.HasPrefix(e.item.ID, memeLearnIDPrefix(today)) {
			learnedToday++
		}
	}
	// 知识库规范词：精确去重的唯一依据（与语义检索解耦）。
	known, err := l.exactTermSet(ctx)
	if err != nil {
		return stats, err
	}

	for _, cand := range candidates {
		if err := checkContext(ctx); err != nil {
			return stats, err
		}
		if l.opts.MaxPerDay > 0 && learnedToday >= l.opts.MaxPerDay {
			stats.Limited++
			break
		}
		key := normalize(cand.text)
		if until, ok := l.rejected[key]; ok && now.Before(until) {
			stats.Skipped++
			continue
		}
		if titles[key] {
			stats.Duplicated++
			continue
		}
		if titleCovered(titleList, key) {
			stats.Duplicated++
			continue
		}
		// 精确命中才算「已收录」：语义相近不是重复。
		if known[key] {
			stats.Duplicated++
			continue
		}
		learned, err := l.judge(ctx, cand)
		if err != nil || learned == nil {
			// 模型失败/拒绝也缓存一天，避免每小时对同一候选重复付费。
			l.rejected[key] = now.Add(memeRejectTTL)
			stats.Skipped++
			continue
		}
		if titles[normalize(learned.Title)] {
			stats.Duplicated++
			continue
		}
		titleKey := normalize(learned.Title)
		if titleCovered(titleList, titleKey) {
			stats.Duplicated++
			continue
		}
		if known[titleKey] {
			stats.Duplicated++
			continue
		}
		entry := learned.diskEntry(today)
		if err := appendMemeDoc(ctx, path, &doc, entry); err != nil {
			return stats, err
		}
		titles[titleKey] = true
		titleList = append(titleList, titleKey)
		delete(l.rejected, key)
		learnedToday++
		stats.Learned++
	}
	return stats, nil
}

// exactTermSet 取知识库的规范词集合（已规范化）。
//
// 只接受实现了 ExactTermSource 的 Reader：不支持时不退回 Recall——
// 相关性检索会把语义相近的不同梗算成重复，那是丢梗的根因。
func (l *MemeLearner) exactTermSet(ctx context.Context) (map[string]bool, error) {
	source, ok := l.reader.(ExactTermSource)
	if !ok {
		return nil, errors.New("知识Reader不支持精确词表，无法判定梗是否已收录")
	}
	terms, err := source.ExactTerms(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取知识规范词：%w", err)
	}
	known := make(map[string]bool, len(terms))
	for _, term := range terms {
		if key := normalize(term); key != "" {
			known[key] = true
		}
	}
	return known, nil
}

// judge 调模型并解析；模型原文不进入错误信息，避免把提示词或弹幕带进日志。
func (l *MemeLearner) judge(ctx context.Context, cand memeCandidate) (*learnedMeme, error) {
	raw, err := l.llm.Complete(ctx, memeLearnPrompt, buildMemeLearnInput(cand))
	if err != nil {
		return nil, err
	}
	return parseMemeLearnReply(raw, cand.text)
}

// memeLearnPrompt 要求模型只输出一行 JSON；是否收录与措辞都由模型判断。
const memeLearnPrompt = `你是直播间的梗库编辑。读候选短语、出现次数和示例弹幕，判断它是否值得长期收录为梗/黑话/直播间用语。
只输出一行 JSON，不要多余文字：
{"learn":true,"title":"梗本身，2到20字","usage":"什么场合怎么用，不超过150字","tags":["2到4个短标签"]}
普通词、人名、广告、辱骂、隐私或无意义拟声词就输出 {"learn":false}。`

func buildMemeLearnInput(cand memeCandidate) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "候选：%s\n出现次数：%d\n", cand.text, cand.count)
	if len(cand.samples) > 0 {
		sb.WriteString("示例弹幕：\n")
		for _, sample := range cand.samples {
			fmt.Fprintf(&sb, "- %s\n", sample)
		}
	}
	return sb.String()
}

type memeLearnReply struct {
	Learn bool     `json:"learn"`
	Title string   `json:"title"`
	Usage string   `json:"usage"`
	Tags  []string `json:"tags"`
}

func parseMemeLearnReply(raw, candidate string) (*learnedMeme, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil, errors.New("模型输出为空")
	}
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```")
		text = strings.TrimPrefix(text, "json")
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	if start := strings.Index(text, "{"); start >= 0 {
		if end := strings.LastIndex(text, "}"); end > start {
			text = text[start : end+1]
		}
	}
	var reply memeLearnReply
	if err := json.Unmarshal([]byte(text), &reply); err != nil {
		return nil, errors.New("模型输出不是JSON")
	}
	if !reply.Learn {
		return nil, nil
	}
	title := strings.TrimSpace(reply.Title)
	usage := strings.TrimSpace(reply.Usage)
	if !textValid(title) || !textValid(usage) || strings.ContainsAny(title, "\r\n\t") {
		return nil, errors.New("模型返回的梗标题或接法无效")
	}
	if n := utf8.RuneCountInString(title); n < 2 || n > 20 {
		return nil, errors.New("模型返回的梗标题长度无效")
	}
	if utf8.RuneCountInString(usage) > 150 {
		return nil, errors.New("模型返回的接法过长")
	}
	if !strings.Contains(normalize(title), normalize(candidate)) && !strings.Contains(normalize(candidate), normalize(title)) {
		return nil, errors.New("模型标题与候选不一致")
	}
	tags := make([]string, 0, 5)
	seen := make(map[string]bool, len(reply.Tags))
	for _, rawTag := range reply.Tags {
		tag := strings.TrimSpace(rawTag)
		if !textValid(tag) || strings.ContainsAny(tag, "\r\n\t") || utf8.RuneCountInString(tag) > 20 {
			continue
		}
		key := normalize(tag)
		if seen[key] {
			continue
		}
		seen[key] = true
		tags = append(tags, tag)
		if len(tags) == 5 {
			break
		}
	}
	if len(tags) == 0 {
		return nil, errors.New("模型返回的标签为空")
	}
	return &learnedMeme{Title: title, Usage: usage, Tags: tags}, nil
}

func (m learnedMeme) diskEntry(today string) diskEntry {
	channel := []string{"live", "comment", "dm", "qq_private", "qq_group"}
	keywords := []string{}
	tags := append([]string(nil), m.Tags...)
	priority := memeLearnPriority
	return diskEntry{
		ID:         memeLearnID(today, m.Title),
		Title:      m.Title,
		Scenes:     channel,
		Keywords:   keywords,
		Tags:       &tags,
		Priority:   &priority,
		Visibility: "public",
		Source:     memeLearnSource,
		Summary:    memeLearnSummary,
		Usage:      m.Usage,
	}
}

func memeLearnIDPrefix(today string) string { return "meme-learned-" + today + "-" }

func memeLearnID(today, title string) string {
	sum := sha256.Sum256([]byte(normalize(title)))
	return fmt.Sprintf("%s%x", memeLearnIDPrefix(today), sum[:6])
}

// loadMemeDoc 读取并完整校验现有 learned.md；文件不存在时返回空文档。
func loadMemeDoc(ctx context.Context, path string) (diskFile, []entry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		empty := []diskEntry{}
		return diskFile{Schema: "fish-knowledge/v1", Entries: &empty}, nil, nil
	}
	if err != nil {
		return diskFile{}, nil, fmt.Errorf("读取梗白学习文件：%w", err)
	}
	if len(data) > maxFileBytes {
		return diskFile{}, nil, errors.New("梗白学习文件超过单文件上限")
	}
	doc, entries, err := parseDocument(ctx, data, true)
	if err != nil {
		return diskFile{}, nil, fmt.Errorf("校验梗白学习文件：%w", err)
	}
	return doc, entries, nil
}

// appendMemeDoc 先追加再编码；编码失败时回滚内存切片，磁盘保持原样。
func appendMemeDoc(ctx context.Context, path string, doc *diskFile, entry diskEntry) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	entries := append(*doc.Entries, entry)
	doc.Entries = &entries
	// 超出条数/字节上限时删最旧的（id 前缀带日期，按字典序≈时间序）
	if trimmed, dropped := trimMemeDoc(entries); dropped > 0 {
		doc.Entries = &trimmed
	}
	if err := saveMemeDoc(path, *doc); err != nil {
		trimmed := entries[:len(entries)-1]
		doc.Entries = &trimmed
		return fmt.Errorf("写入梗白学习文件：%w", err)
	}
	return nil
}

// trimMemeDoc 按条数与字节上限裁掉最旧条目，返回保留的条目与被删条数。
func trimMemeDoc(entries []diskEntry) ([]diskEntry, int) {
	if len(entries) <= maxLearnedEntries {
		body, err := yaml.Marshal(diskFile{Entries: &entries})
		if err == nil && len(body) <= maxLearnedBytes {
			return entries, 0
		}
	}
	sorted := make([]diskEntry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	keep := sorted
	dropped := 0
	for len(keep) > maxLearnedEntries {
		keep = keep[1:]
		dropped++
	}
	for len(keep) > 1 {
		body, err := yaml.Marshal(diskFile{Entries: &keep})
		if err != nil || len(body) <= maxLearnedBytes {
			break
		}
		keep = keep[1:]
		dropped++
	}
	out := make([]diskEntry, len(keep))
	copy(out, keep)
	return out, dropped
}

func saveMemeDoc(path string, doc diskFile) error {
	body, err := yaml.Marshal(doc)
	if err != nil {
		return errors.New("编码梗白学习文件失败")
	}
	data := make([]byte, 0, len(body)+8)
	data = append(data, "---\n"...)
	data = append(data, body...)
	data = append(data, "---\n"...)
	if len(data) > maxFileBytes {
		return errors.New("梗白学习文件超过单文件上限")
	}
	return writeMemeFile(path, data)
}

func writeMemeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("创建梗白目录：%w", err)
	}
	tmp, err := os.CreateTemp(dir, ".learned-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件：%w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写临时文件：%w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("同步临时文件：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件：%w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("替换梗白学习文件：%w", err)
	}
	return nil
}

// extractMemeCandidates 统计 2-8 字重复短语，按出现次数从高到低取前 maxCandidates；
// 纯数字、标点和单字重复不算候选。
func extractMemeCandidates(events []event.Event, minCount, maxCandidates int) []memeCandidate {
	counts := make(map[string]int)
	samples := make(map[string][]string)
	scannedRunes := 0
	for _, ev := range events {
		if ev.Dir != event.DirIn || ev.Private {
			continue
		}
		clean := stripMemeNoise(ev.Text)
		cleanRunes := []rune(clean)
		if scannedRunes+len(cleanRunes) > maxMemeSourceRunes {
			break
		}
		scannedRunes += len(cleanRunes)
		sample := clipRunes(clean, 80)
		for _, token := range strings.FieldsFunc(clean, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			runes := []rune(token)
			if len(runes) < 2 || len(runes) > maxMemeTokenRunes {
				continue
			}
			// 英文/数字词按整词计数，避免从 hello 里切出 ell、llo 这类噪声。
			if !hasHan(token) {
				addMemeGram(counts, samples, token, sample)
				continue
			}
			for n := 2; n <= 8 && n <= len(runes); n++ {
				for i := 0; i+n <= len(runes); i++ {
					addMemeGram(counts, samples, string(runes[i:i+n]), sample)
				}
			}
		}
	}
	ranked := make([]memeCandidate, 0, len(counts))
	for text, count := range counts {
		if count < minCount {
			continue
		}
		ranked = append(ranked, memeCandidate{text: text, count: count, samples: samples[text]})
	}
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.count != b.count {
			return a.count > b.count
		}
		if utf8.RuneCountInString(a.text) != utf8.RuneCountInString(b.text) {
			return utf8.RuneCountInString(a.text) > utf8.RuneCountInString(b.text)
		}
		return a.text < b.text
	})
	out := make([]memeCandidate, 0, maxCandidates)
	for _, cand := range ranked {
		redundant := false
		for _, kept := range out {
			if kept.count >= cand.count && strings.Contains(kept.text, cand.text) {
				redundant = true
				break
			}
		}
		if redundant {
			continue
		}
		out = append(out, cand)
		if len(out) >= maxCandidates {
			break
		}
	}
	return out
}

func addMemeGram(counts map[string]int, samples map[string][]string, gram, sample string) {
	if !candidateShapeOK(gram) {
		return
	}
	key := normalize(gram)
	counts[key]++
	if len(samples[key]) < 2 && !containsString(samples[key], sample) {
		samples[key] = append(samples[key], sample)
	}
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func titleCovered(titles []string, candidate string) bool {
	for _, title := range titles {
		if strings.Contains(title, candidate) || strings.Contains(candidate, title) {
			return true
		}
	}
	return false
}

func candidateShapeOK(s string) bool {
	runes := []rune(s)
	if len(runes) < 2 || len(runes) > 8 || !utf8.ValidString(s) {
		return false
	}
	hasLetter := false
	same := true
	for _, r := range runes {
		if unicode.IsLetter(r) {
			hasLetter = true
		}
		if r != runes[0] {
			same = false
		}
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			return false
		}
	}
	return hasLetter && !same
}

// stripMemeNoise 去掉 URL 与 @提及，保留其余原文供示例展示。
func stripMemeNoise(text string) string {
	text = memeURLPattern.ReplaceAllString(text, " ")
	var sb strings.Builder
	sb.Grow(len(text))
	skipMention := false
	for _, r := range text {
		if r == '@' || r == '＠' {
			skipMention = true
			continue
		}
		if skipMention {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
				continue
			}
			skipMention = false
		}
		sb.WriteRune(r)
	}
	return strings.TrimSpace(sb.String())
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func clipRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
