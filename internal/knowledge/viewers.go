package knowledge

// 观众侧写知识条目：把定时生成的公开侧写写进 viewers.md，供 RAG 召回。
//
// 边界：
//   - 只写 visibility=public 的自动条目（viewer-auto-*），不碰人工条目；
//   - 侧写正文放 usage（embeddingText = title+usage+tags），统计与来源放 summary；
//   - 同一 UID 用稳定 ID 覆盖，重复侧写不会堆出多条；
//   - 先完整解析校验再原子替换，写坏文件时旧文件保持不变。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const (
	viewerKnowledgeFile   = "viewers.md"
	viewerKnowledgeSource = "viewer-summary:auto"
	viewerKnowledgePrefix = "viewer-auto-"
	viewerKnowledgePrio   = 35
	// viewerKnowledgeFooter 写在 front matter 之后；解析器只读 front matter，不影响条目。
	viewerKnowledgeFooter = `<!-- 由 fishd 观众侧写任务自动维护：viewer-auto-* 来自公开弹幕，可被后续侧写覆盖；
     人工审核的观众资料请使用 viewer-manual-* 标识。
     禁止把私信、私密画像或合成测试人物写入此公开知识文件。 -->
`
)

// ViewerEntry 是一份待写入知识库的公开观众侧写。
type ViewerEntry struct {
	UID        string
	Name       string
	Summary    string
	Stats      string
	Keywords   []string
	UpdatedAt  time.Time
	SourceHash string
	Generator  string
}

// ViewerWriter 串行维护 viewers.md；同一进程内不并发写同一文件。
type ViewerWriter struct {
	dir string
	mu  sync.Mutex
}

// NewViewerWriter 只校验目录字符串；实际读到路径在写入时检查。
func NewViewerWriter(dir string) (*ViewerWriter, error) {
	if !textValid(dir) {
		return nil, errors.New("观众知识目录不能为空")
	}
	return &ViewerWriter{dir: dir}, nil
}

// Upsert 写入或覆盖某位观众的公开侧写条目。
func (w *ViewerWriter) Upsert(ctx context.Context, entry ViewerEntry) error {
	if w == nil {
		return errors.New("观众知识写入器不能为空")
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	if !textValid(entry.UID) || !textValid(entry.Name) || !textValid(entry.Summary) {
		return errors.New("观众侧写知识条目缺少必需字段")
	}
	entry.Name = cleanViewerText(entry.Name)
	entry.Summary = cleanViewerText(entry.Summary)
	entry.Stats = cleanViewerText(entry.Stats)
	if entry.Name == "" || entry.Summary == "" {
		return errors.New("观众侧写知识条目清洗后为空")
	}
	id := viewerKnowledgeID(entry.UID)
	newEntry := viewerDiskEntry(entry, id)

	w.mu.Lock()
	defer w.mu.Unlock()

	path := filepath.Join(w.dir, viewerKnowledgeFile)
	doc, entries, err := loadViewerDoc(ctx, path)
	if err != nil {
		return err
	}
	list := make([]diskEntry, 0, len(entries)+1)
	replaced := false
	for _, existing := range *doc.Entries {
		if existing.ID != id {
			list = append(list, existing)
			continue
		}
		if existing.Source != viewerKnowledgeSource {
			return fmt.Errorf("观众知识条目标识 %s 已被人工内容占用", id)
		}
		list = append(list, newEntry)
		replaced = true
	}
	if !replaced {
		list = append(list, newEntry)
	}
	doc.Entries = &list
	data, err := encodeViewerDoc(doc)
	if err != nil {
		return err
	}
	// 落盘前按 Reader 的同一套规则完整解析一次，避免把不可读文件交给热重载。
	if _, _, err := parseDocument(ctx, data, false); err != nil {
		return fmt.Errorf("校验待写入的观众知识文件：%w", err)
	}
	if err := writeKnowledgeFile(path, data); err != nil {
		return fmt.Errorf("写入观众知识文件：%w", err)
	}
	return nil
}

// Remove 删除某位观众的自动侧写条目；文件不存在或条目不存在时幂等成功。
//
// 只删 viewer-auto-* 且来源为本次自动生成的条目；人工占用的同 ID 条目保持原样。
// 读取/解析失败时不改写旧文件，交由投影任务下轮重试。
func (w *ViewerWriter) Remove(ctx context.Context, uid string) error {
	if w == nil {
		return errors.New("观众知识写入器不能为空")
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return errors.New("观众知识条目缺少 UID")
	}
	id := viewerKnowledgeID(uid)

	w.mu.Lock()
	defer w.mu.Unlock()

	path := filepath.Join(w.dir, viewerKnowledgeFile)
	doc, _, err := loadViewerDoc(ctx, path)
	if err != nil {
		return err
	}
	list := make([]diskEntry, 0, len(*doc.Entries))
	removed := false
	for _, existing := range *doc.Entries {
		if existing.ID != id {
			list = append(list, existing)
			continue
		}
		if existing.Source != viewerKnowledgeSource {
			// 人工条目不受自动撤回影响，保持原样。
			list = append(list, existing)
			continue
		}
		removed = true
	}
	if !removed {
		return nil
	}
	doc.Entries = &list
	data, err := encodeViewerDoc(doc)
	if err != nil {
		return err
	}
	if _, _, err := parseDocument(ctx, data, false); err != nil {
		return fmt.Errorf("校验待写入的观众知识文件：%w", err)
	}
	if err := writeKnowledgeFile(path, data); err != nil {
		return fmt.Errorf("写入观众知识文件：%w", err)
	}
	return nil
}

// viewerKnowledgeID 是同一 UID 的稳定条目 ID；不把 UID 明文写进公开文件。
func viewerKnowledgeID(uid string) string {
	sum := sha256.Sum256([]byte(uid))
	return viewerKnowledgePrefix + hex.EncodeToString(sum[:6])
}

// viewerDiskEntry 把侧写转成严格 YAML 条目。
func viewerDiskEntry(entry ViewerEntry, id string) diskEntry {
	scenes := []string{"live", "comment", "dm", "qq_private", "qq_group"}
	keywords := viewerKeywords(entry)
	tags := []string{"观众", "侧写"}
	priority := viewerKnowledgePrio
	return diskEntry{
		ID:         id,
		Title:      "观众侧写 · " + clipViewerRunes(strings.TrimSpace(entry.Name), 20),
		Scenes:     scenes,
		Keywords:   keywords,
		Tags:       &tags,
		Priority:   &priority,
		Visibility: "public",
		Source:     viewerKnowledgeSource,
		Summary:    viewerEntrySummary(entry),
		Usage:      "聊到/认出这位观众时参考：" + entry.Summary,
	}
}

// viewerEntrySummary 是来源与统计（不参与 embedding），不含 UID。
func viewerEntrySummary(entry ViewerEntry) string {
	stats := strings.TrimSpace(entry.Stats)
	if stats == "" {
		stats = "统计未知"
	}
	at := entry.UpdatedAt.UTC().Format("2006-01-02")
	version := strings.TrimSpace(entry.Generator)
	if version == "" {
		version = "未知"
	}
	hash := strings.TrimSpace(entry.SourceHash)
	if len(hash) > 12 {
		hash = hash[:12]
	}
	if hash == "" {
		hash = "无"
	}
	return fmt.Sprintf("统计：%s；侧写时间：%s；来源版本：%s/%s。由公开弹幕自动生成，非人工审核。",
		stats, at, version, hash)
}

// viewerKeywords 用昵称与口头禅做词法召回；去重并限制长度。
func viewerKeywords(entry ViewerEntry) []string {
	out := make([]string, 0, 6)
	seen := make(map[string]bool, 6)
	add := func(word string) {
		word = strings.TrimSpace(word)
		if word == "" || utf8.RuneCountInString(word) > 20 || strings.ContainsAny(word, "\r\n\t\x00") || seen[word] {
			return
		}
		seen[word] = true
		out = append(out, word)
	}
	add(entry.Name)
	for _, word := range entry.Keywords {
		add(word)
		if len(out) == 6 {
			break
		}
	}
	if len(out) == 0 {
		// 昵称为空已在入口拒绝；这里只保证 validateItem 不会拿到空关键词列表。
		out = append(out, "观众")
	}
	return out
}

// loadViewerDoc 读取并完整校验 viewers.md；文件不存在时返回空文档。
func loadViewerDoc(ctx context.Context, path string) (diskFile, []entry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		empty := []diskEntry{}
		return diskFile{Schema: "fish-knowledge/v1", Entries: &empty}, nil, nil
	}
	if err != nil {
		return diskFile{}, nil, fmt.Errorf("读取观众知识文件：%w", err)
	}
	if len(data) > maxFileBytes {
		return diskFile{}, nil, errors.New("观众知识文件超过单文件上限")
	}
	doc, entries, err := parseDocument(ctx, data, false)
	if err != nil {
		return diskFile{}, nil, fmt.Errorf("校验观众知识文件：%w", err)
	}
	return doc, entries, nil
}

// encodeViewerDoc 生成 front matter + 固定说明；body 不参与知识解析。
func encodeViewerDoc(doc diskFile) ([]byte, error) {
	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, errors.New("编码观众知识文件失败")
	}
	data := make([]byte, 0, len(body)+len(viewerKnowledgeFooter)+8)
	data = append(data, "---\n"...)
	data = append(data, body...)
	data = append(data, "---\n"...)
	data = append(data, viewerKnowledgeFooter...)
	if len(data) > maxFileBytes {
		return nil, errors.New("观众知识文件超过单文件上限")
	}
	return data, nil
}

func clipViewerRunes(text string, limit int) string {
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// cleanViewerText 把换行/控制字符折叠成空格，避免把多行正文带进标题与 usage。
func cleanViewerText(raw string) string {
	var sb strings.Builder
	space := false
	for _, r := range strings.TrimSpace(raw) {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			space = true
			continue
		}
		if space && sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		space = false
		sb.WriteRune(r)
	}
	return sb.String()
}
