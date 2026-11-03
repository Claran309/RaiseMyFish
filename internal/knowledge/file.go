package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
	"go.yaml.in/yaml/v3"
)

const (
	maxFileBytes    = 1 << 20
	maxEntryBytes   = 16 << 10
	maxLibraryBytes = 32 << 20
	maxEntries      = 10000
)

type entry struct {
	item     Item
	title    string
	keywords []string
	tags     []string
}

// FileReader 是构造时完整校验的不可变快照，无全局缓存或文件监听。
type FileReader struct {
	revision string
	entries  []entry
}

type diskScope struct {
	Channel event.Channel `yaml:"channel"`
	Session string        `yaml:"session"`
	Private *bool         `yaml:"private"`
}
type diskEntry struct {
	ID         string     `yaml:"id"`
	Title      string     `yaml:"title"`
	Scenes     []string   `yaml:"channel"`
	Keywords   []string   `yaml:"keywords"`
	Tags       *[]string  `yaml:"tags"`
	Priority   *int       `yaml:"priority"`
	Visibility string     `yaml:"visibility"`
	Scope      *diskScope `yaml:"scope,omitempty"`
	Source     string     `yaml:"source"`
	Summary    string     `yaml:"summary"`
	Usage      string     `yaml:"usage"`
}
type diskFile struct {
	Schema  string       `yaml:"schema"`
	Entries *[]diskEntry `yaml:"entries"`
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("知识操作ctx不能为空")
	}
	return ctx.Err()
}

func containedPath(root, path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("解析知识路径：%w", err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("知识路径逃逸根目录")
	}
	return resolved, nil
}

// NewFileReader 只读取四个必需根文件、memes/batch-*.md 与 memes/learned.md，不返回部分成功快照。
func NewFileReader(ctx context.Context, dir string) (*FileReader, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if !textValid(dir) {
		return nil, errors.New("知识目录不能为空")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("解析知识目录：%w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("定位知识目录：%w", err)
	}
	names := []string{"lore.md", "memes.md", "viewers.md", "games.md"}
	batchDir := filepath.Join(root, "memes")
	if _, statErr := os.Lstat(batchDir); statErr == nil {
		resolved, pathErr := containedPath(root, batchDir)
		if pathErr != nil {
			return nil, pathErr
		}
		f, openErr := os.Open(resolved)
		if openErr != nil {
			return nil, fmt.Errorf("读取知识批次目录：%w", openErr)
		}
		defer f.Close()
		for {
			if err := checkContext(ctx); err != nil {
				return nil, err
			}
			files, readErr := f.ReadDir(128)
			for _, file := range files {
				name := file.Name()
				if (strings.HasPrefix(name, "batch-") || name == "learned.md") && strings.HasSuffix(name, ".md") {
					if strings.ContainsAny(name, "\n\r\t") || len(names) >= maxEntries+4 {
						return nil, errors.New("知识批次路径或文件数量无效")
					}
					names = append(names, "memes/"+name)
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				return nil, fmt.Errorf("读取知识批次目录：%w", readErr)
			}
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("检查知识批次目录：%w", statErr)
	}
	sort.Strings(names)
	r := &FileReader{}
	seen := make(map[string]bool)
	version := sha256.New()
	_, _ = io.WriteString(version, "fish-knowledge/v1\n")
	total := 0
	for _, name := range names {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		resolved, err := containedPath(root, filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return nil, fmt.Errorf("定位%s：%w", name, err)
		}
		data, err := readBounded(ctx, resolved)
		if err != nil {
			return nil, fmt.Errorf("加载%s：%w", name, err)
		}
		total += len(data)
		if total > maxLibraryBytes {
			return nil, errors.New("知识库超过总字节上限")
		}
		_, _ = fmt.Fprintf(version, "%s\t%x\n", name, sha256.Sum256(data))
		items, err := parseFile(ctx, data, name == "memes.md" || strings.HasPrefix(name, "memes/"))
		if err != nil {
			return nil, fmt.Errorf("解析%s：%w", name, err)
		}
		for _, item := range items {
			if seen[item.item.ID] {
				return nil, errors.New("知识库条目标识重复")
			}
			seen[item.item.ID] = true
			r.entries = append(r.entries, item)
			if len(r.entries) > maxEntries {
				return nil, errors.New("知识库超过条目上限")
			}
		}
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	r.revision = fmt.Sprintf("%x", version.Sum(nil))
	return r, nil
}

func readBounded(ctx context.Context, path string) (data []byte, result error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("检查知识文件：%w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return nil, errors.New("知识文件类型或大小无效")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开知识文件：%w", err)
	}
	defer func() { result = errors.Join(result, f.Close()) }()
	var out bytes.Buffer
	buffer := make([]byte, 32<<10)
	for {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		n, readErr := f.Read(buffer)
		out.Write(buffer[:n])
		if out.Len() > maxFileBytes {
			return nil, errors.New("知识文件超过单文件上限")
		}
		if errors.Is(readErr, io.EOF) {
			return out.Bytes(), nil
		}
		if readErr != nil {
			return nil, fmt.Errorf("读取知识文件：%w", readErr)
		}
	}
}

func frontMatter(data []byte) (string, error) {
	if !utf8.Valid(data) {
		return "", errors.New("知识文件不是UTF-8")
	}
	s := strings.TrimPrefix(string(data), "\ufeff")
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return "", errors.New("知识文件缺少front matter")
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] != "---" {
			continue
		}
		for _, line := range lines[i+1:] {
			if line == "---" || line == "..." {
				return "", errors.New("知识文件不允许多个文档")
			}
		}
		return strings.Join(lines[1:i], "\n"), nil
	}
	return "", errors.New("知识文件front matter未闭合")
}

func rejectAliases(ctx context.Context, node *yaml.Node) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" || node.Tag == "!!merge" {
		return errors.New("知识YAML不允许锚点、别名或合并键")
	}
	for _, child := range node.Content {
		if err := rejectAliases(ctx, child); err != nil {
			return err
		}
	}
	return nil
}

// yaml.v3会把数字标量转成string；先核对节点标签，避免悄悄接受错误的条目类型。
func validateWireTypes(node *yaml.Node) error {
	bad := errors.New("知识YAML字段类型无效")
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return bad
	}
	root := node.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i].Value, root.Content[i+1]
		if key == "schema" && value.Tag != "!!str" {
			return bad
		}
		if key != "entries" {
			continue
		}
		if value.Kind != yaml.SequenceNode {
			return bad
		}
		for _, row := range value.Content {
			if row.Kind != yaml.MappingNode {
				return bad
			}
			for j := 0; j < len(row.Content); j += 2 {
				field, v := row.Content[j].Value, row.Content[j+1]
				switch field {
				case "id", "title", "visibility", "source", "summary", "usage":
					if v.Tag != "!!str" {
						return bad
					}
				case "priority":
					if v.Tag != "!!int" {
						return bad
					}
				case "channel", "keywords", "tags":
					if v.Kind != yaml.SequenceNode {
						return bad
					}
					for _, word := range v.Content {
						if word.Tag != "!!str" {
							return bad
						}
					}
				case "scope":
					if v.Kind != yaml.MappingNode {
						return bad
					}
					for k := 0; k < len(v.Content); k += 2 {
						name, scalar := v.Content[k].Value, v.Content[k+1]
						if (name == "channel" || name == "session") && scalar.Tag != "!!str" || name == "private" && scalar.Tag != "!!bool" {
							return bad
						}
					}
				}
			}
		}
	}
	return nil
}

func parseFile(ctx context.Context, data []byte, meme bool) ([]entry, error) {
	_, items, err := parseDocument(ctx, data, meme)
	return items, err
}

// parseDocument 同时返回原始条目与转换结果，供需要原样回写的机器管理文件使用。
func parseDocument(ctx context.Context, data []byte, meme bool) (diskFile, []entry, error) {
	front, err := frontMatter(data)
	if err != nil {
		return diskFile{}, nil, err
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(front), &node); err != nil {
		return diskFile{}, nil, errors.New("知识YAML语法无效")
	}
	if err := rejectAliases(ctx, &node); err != nil {
		return diskFile{}, nil, err
	}
	if err := validateWireTypes(&node); err != nil {
		return diskFile{}, nil, err
	}
	var raw diskFile
	decoder := yaml.NewDecoder(strings.NewReader(front))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return diskFile{}, nil, errors.New("知识YAML字段、类型或重复键无效")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return diskFile{}, nil, errors.New("知识YAML必须只有一个文档")
	}
	if raw.Schema != "fish-knowledge/v1" || raw.Entries == nil {
		return diskFile{}, nil, errors.New("知识schema或entries无效")
	}
	if len(*raw.Entries) > maxEntries {
		return diskFile{}, nil, errors.New("知识文件超过条目上限")
	}
	items := make([]entry, 0, len(*raw.Entries))
	for _, d := range *raw.Entries {
		if err := checkContext(ctx); err != nil {
			return diskFile{}, nil, err
		}
		item, err := convertEntry(d, meme)
		if err != nil {
			return diskFile{}, nil, err
		}
		items = append(items, item)
	}
	return raw, items, nil
}

func convertEntry(d diskEntry, meme bool) (entry, error) {
	if !textValid(d.Title) || d.Tags == nil || d.Priority == nil || (meme && !textValid(d.Usage)) || (!meme && !textValid(d.Summary)) {
		return entry{}, errors.New("知识条目缺少必需字段")
	}
	item := Item{ID: d.ID, Source: d.Source, Scenes: append([]string(nil), d.Scenes...), Priority: *d.Priority}
	switch d.Visibility {
	case "public":
	case "private":
		item.Private = true
	default:
		return entry{}, errors.New("知识visibility无效")
	}
	if d.Scope != nil {
		if d.Scope.Private == nil {
			return entry{}, errors.New("知识来源缺少private标记")
		}
		item.Origin = &Scope{Channel: d.Scope.Channel, Session: d.Scope.Session, Private: *d.Scope.Private}
	}
	item.Text = "主题：" + d.Title
	if d.Summary != "" {
		item.Text += "\n资料：" + d.Summary
	}
	if d.Usage != "" {
		item.Text += "\n接法：" + d.Usage
	}
	if err := validateItem(item); err != nil {
		return entry{}, err
	}
	n := len(item.Text) + len(d.ID) + len(d.Source)
	for _, list := range [][]string{d.Keywords, *d.Tags, d.Scenes} {
		for _, word := range list {
			if !textValid(word) {
				return entry{}, errors.New("知识触发词或标签无效")
			}
			n += len(word)
		}
	}
	if d.Scope != nil {
		n += len(d.Scope.Session)
	}
	if n > maxEntryBytes {
		return entry{}, errors.New("知识条目超过资料上限")
	}
	return entry{item: item, title: d.Title, keywords: append([]string(nil), d.Keywords...), tags: append([]string(nil), (*d.Tags)...)}, nil
}
