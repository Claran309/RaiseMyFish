package personaeval

import (
	"RaiseMyFish/internal/persona"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// FileSnapshot 仅序列化哈希元数据，内容只用于保存用例原始副本。
type FileSnapshot struct {
	Name   string `json:"name"`
	Path   string `json:"-"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
	Data   []byte `json:"-"`
}

// Fixture 是实际使用的人格与用例快照，不能混入baseline回复。
type Fixture struct {
	Pack  persona.Pack
	Cases []Case
	Files []FileSnapshot
	Rules []byte
}

// Digest 返回原始字节SHA256。
func Digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Prepare 在模型调用前固定文件与规则，并核验Load前后没有源文件变化。
func Prepare(ctx context.Context, packDir, casesPath string, scorer *Scorer) (Fixture, error) {
	var result Fixture
	if scorer == nil {
		return result, errors.New("评分器不能为空")
	}
	paths := []struct{ name, path string }{{"meta.yaml", filepath.Join(packDir, "meta.yaml")}, {"soul.md", filepath.Join(packDir, "soul.md")}, {"rules.md", filepath.Join(packDir, "rules.md")}, {"samples.md", filepath.Join(packDir, "samples.md")}, {"context.md", filepath.Join(packDir, "context.md")}, {"cases.jsonl", casesPath}}
	for _, item := range paths {
		if err := ctx.Err(); err != nil {
			return Fixture{}, err
		}
		data, err := os.ReadFile(item.path)
		if err != nil {
			return Fixture{}, fmt.Errorf("读取评估输入%s：%w", item.name, err)
		}
		if len(data) > 4<<20 {
			return Fixture{}, errors.New("评估输入文件过大")
		}
		result.Files = append(result.Files, FileSnapshot{Name: item.name, Path: item.path, Bytes: len(data), SHA256: Digest(data), Data: data})
	}
	pack, err := persona.Load(ctx, packDir)
	if err != nil {
		return Fixture{}, err
	}
	result.Pack = pack
	cases, err := parseCases(result.Files[len(result.Files)-1].Data)
	if err != nil {
		return Fixture{}, err
	}
	if err := scorer.ValidateCases(cases); err != nil {
		return Fixture{}, err
	}
	result.Cases = cases
	result.Rules = scorer.Snapshot()
	if err := result.VerifySources(); err != nil {
		return Fixture{}, err
	}
	return result, nil
}

func parseCases(data []byte) ([]Case, error) {
	var cases []Case
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" {
			continue
		}
		var c Case
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&c); err != nil {
			return nil, errors.New("评估JSONL格式或字段错误")
		}
		var extra json.RawMessage
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return nil, errors.New("每行只允许一个用例")
		}
		cases = append(cases, c)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取评估JSONL：%w", err)
	}
	return cases, nil
}

// VerifySources 防止在配置/生成器装配期间偷换人设或用例。
func (f Fixture) VerifySources() error {
	for _, file := range f.Files {
		data, err := os.ReadFile(file.Path)
		if err != nil {
			return fmt.Errorf("复核输入%s：%w", file.Name, err)
		}
		if Digest(data) != file.SHA256 {
			return errors.New("评估输入已变化，拒绝使用错配快照")
		}
	}
	return nil
}
