// Package skills 加载本地技能包：每个子目录一个 SKILL.md，可选脚本入口。
//
// 技能是「给模型看的操作说明」，脚本是可选执行体；不把技能做成可任意访问网络的插件。
package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	maxArgsBytes      = 4096
	defaultTimeout    = 10 * time.Second
	defaultMaxOutput  = 8000
	defaultCheckEvery = 30 * time.Second
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

var scriptExtensions = map[string]bool{
	".ps1": true,
	".bat": true,
	".cmd": true,
	".sh":  true,
}

// manifest 是 SKILL.md 的 YAML frontmatter；正文是给模型的操作说明。
type manifest struct {
	Name           string `yaml:"name"`
	Description    string `yaml:"description"`
	Version        int    `yaml:"version"`
	Entry          string `yaml:"entry"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

// Skill 是一个已加载的技能快照。
type Skill struct {
	Name        string
	Description string
	Version     int
	Entry       string
	Timeout     time.Duration
	Body        string
	Dir         string
}

// Info 是给 skill_list 的轻量描述，不暴露脚本路径和正文。
type Info struct {
	Name        string
	Description string
	HasScript   bool
}

// Options 是技能目录的部署设置。
type Options struct {
	Dir            string
	AllowScripts   bool
	MaxTimeout     time.Duration
	MaxOutputBytes int
	Allowed        []string
	CheckInterval  time.Duration
}

// Registry 是并发安全的技能表；List/Get/Run 前按检查间隔热重载。
type Registry struct {
	opts     Options
	mu       sync.Mutex
	byName   map[string]Skill
	loadedAt time.Time
	execute  func(context.Context, Skill, string) (string, error)
}

// New 校验设置并创建空注册表；真正读盘由 Reload 完成。
func New(opts Options) (*Registry, error) {
	if strings.TrimSpace(opts.Dir) == "" || strings.ContainsAny(opts.Dir, "\r\n\x00") {
		return nil, errors.New("技能目录不能为空且不能含控制字符")
	}
	if abs, err := filepath.Abs(opts.Dir); err == nil {
		opts.Dir = abs
	}
	if opts.MaxTimeout <= 0 {
		opts.MaxTimeout = 30 * time.Second
	}
	if opts.MaxTimeout > 10*time.Minute {
		return nil, errors.New("技能最大超时不能超过10分钟")
	}
	if opts.MaxOutputBytes <= 0 {
		opts.MaxOutputBytes = defaultMaxOutput
	}
	if opts.MaxOutputBytes < 512 || opts.MaxOutputBytes > 1<<20 {
		return nil, errors.New("技能输出上限必须在512字节至1MB之间")
	}
	if opts.CheckInterval <= 0 {
		opts.CheckInterval = defaultCheckEvery
	}
	if opts.CheckInterval < time.Second {
		return nil, errors.New("技能检查间隔不能小于1秒")
	}
	for _, name := range opts.Allowed {
		if !namePattern.MatchString(name) {
			return nil, fmt.Errorf("技能白名单名称无效：%q", name)
		}
	}
	r := &Registry{opts: opts, byName: map[string]Skill{}}
	r.execute = r.executeScript
	return r, nil
}

// Reload 扫描目录并替换快照；单个坏技能只跳过并返回聚合错误，不拖垮其余技能。
func (r *Registry) Reload() error {
	entries, err := os.ReadDir(r.opts.Dir)
	if err != nil {
		return fmt.Errorf("读取技能目录失败：%w", err)
	}
	next := map[string]Skill{}
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skill, err := loadSkill(filepath.Join(r.opts.Dir, entry.Name()), r.opts)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s：%w", entry.Name(), err))
			continue
		}
		if !r.allowed(skill.Name) {
			continue
		}
		if _, dup := next[skill.Name]; dup {
			errs = append(errs, fmt.Errorf("%s：技能重名", skill.Name))
			continue
		}
		next[skill.Name] = skill
	}
	r.mu.Lock()
	r.byName = next
	r.loadedAt = time.Now()
	r.mu.Unlock()
	return errors.Join(errs...)
}

func (r *Registry) allowed(name string) bool {
	if len(r.opts.Allowed) == 0 {
		return true
	}
	for _, item := range r.opts.Allowed {
		if item == name {
			return true
		}
	}
	return false
}

// maybeReload 到达检查间隔才扫盘，避免每个事件都做 IO。
func (r *Registry) maybeReload() {
	r.mu.Lock()
	stale := time.Since(r.loadedAt) >= r.opts.CheckInterval
	r.mu.Unlock()
	if stale {
		_ = r.Reload()
	}
}

// List 返回按名称排序的技能摘要。
func (r *Registry) List() []Info {
	if r == nil {
		return nil
	}
	r.maybeReload()
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.byName))
	for name := range r.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Info, 0, len(names))
	for _, name := range names {
		skill := r.byName[name]
		out = append(out, Info{Name: skill.Name, Description: skill.Description, HasScript: skill.Entry != ""})
	}
	return out
}

// Get 返回指定技能快照。
func (r *Registry) Get(name string) (Skill, bool) {
	if r == nil {
		return Skill{}, false
	}
	r.maybeReload()
	r.mu.Lock()
	defer r.mu.Unlock()
	skill, ok := r.byName[name]
	return skill, ok
}

// Read 返回技能正文（SKILL.md 的 markdown 部分）。
func (r *Registry) Read(name string) (string, error) {
	skill, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("技能不存在：%s", name)
	}
	if strings.TrimSpace(skill.Body) == "" {
		return "", fmt.Errorf("技能没有说明正文：%s", name)
	}
	return skill.Body, nil
}

// Run 执行技能声明的脚本；args 通过 FISH_SKILL_ARGS 环境变量传给脚本。
func (r *Registry) Run(ctx context.Context, name, args string) (string, error) {
	if r == nil {
		return "", errors.New("技能系统未启用")
	}
	if len(args) > maxArgsBytes || strings.ContainsRune(args, 0) {
		return "", errors.New("技能参数过大或含非法字符")
	}
	skill, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("技能不存在：%s", name)
	}
	if skill.Entry == "" {
		return "", fmt.Errorf("技能只提供说明，没有可执行脚本：%s", name)
	}
	if !r.opts.AllowScripts {
		return "", errors.New("技能脚本执行未开启（[skills] allow_scripts=false）")
	}
	return r.execute(ctx, skill, args)
}

// loadSkill 解析一个技能目录；目录名必须与 frontmatter 的 name 一致。
func loadSkill(dir string, opts Options) (Skill, error) {
	path := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, fmt.Errorf("读取 SKILL.md 失败：%w", err)
	}
	raw := strings.ReplaceAll(strings.TrimPrefix(string(data), "\ufeff"), "\r\n", "\n")
	front, body, err := splitFrontmatter(raw)
	if err != nil {
		return Skill{}, err
	}
	var m manifest
	if err := yaml.Unmarshal([]byte(front), &m); err != nil {
		return Skill{}, fmt.Errorf("frontmatter 不是合法 YAML：%w", err)
	}
	name := strings.TrimSpace(m.Name)
	if name == "" || name != filepath.Base(dir) || !namePattern.MatchString(name) {
		return Skill{}, errors.New("name 必须是目录名且仅含小写字母/数字/短横线")
	}
	description := strings.TrimSpace(m.Description)
	if description == "" || len([]rune(description)) > 200 || strings.ContainsAny(description, "\r\n\x00") {
		return Skill{}, errors.New("description 不能为空、不能超过200字或含控制字符")
	}
	if m.Version < 1 {
		return Skill{}, errors.New("version 必须是正整数")
	}
	timeout := defaultTimeout
	if m.TimeoutSeconds > 0 {
		timeout = time.Duration(m.TimeoutSeconds) * time.Second
	}
	if timeout > opts.MaxTimeout {
		return Skill{}, fmt.Errorf("timeout_seconds 超过配置上限 %s", opts.MaxTimeout)
	}
	entry := strings.TrimSpace(m.Entry)
	if entry != "" {
		if err := validateEntry(dir, entry); err != nil {
			return Skill{}, err
		}
	}
	return Skill{Name: name, Description: description, Version: m.Version, Entry: entry, Timeout: timeout, Body: strings.TrimSpace(body), Dir: dir}, nil
}

func splitFrontmatter(raw string) (string, string, error) {
	if !strings.HasPrefix(raw, "---\n") {
		return "", "", errors.New("SKILL.md 必须以 --- 开头的 frontmatter 开始")
	}
	rest := raw[len("---\n"):]
	index := strings.Index(rest, "\n---\n")
	if index < 0 {
		return "", "", errors.New("SKILL.md frontmatter 缺少结束分隔线")
	}
	return rest[:index], rest[index+len("\n---\n"):], nil
}

func validateEntry(dir, entry string) error {
	if filepath.IsAbs(entry) {
		return errors.New("entry 必须是技能目录内的相对路径")
	}
	clean := filepath.Clean(filepath.FromSlash(entry))
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return errors.New("entry 不能跳出技能目录")
	}
	if !scriptExtensions[strings.ToLower(filepath.Ext(clean))] {
		return errors.New("entry 只支持 .ps1/.bat/.cmd/.sh")
	}
	path := filepath.Join(dir, clean)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return errors.New("技能目录不可用")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("entry 文件不存在或不是普通文件：%s", clean)
	}
	rel, err := filepath.Rel(realDir, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("entry 路径不合法")
	}
	info, err := os.Stat(realPath)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("entry 文件不存在或不是普通文件：%s", clean)
	}
	return nil
}

func (r *Registry) executeScript(ctx context.Context, skill Skill, args string) (string, error) {
	timeout := skill.Timeout
	if timeout <= 0 || timeout > r.opts.MaxTimeout {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	script := filepath.Join(skill.Dir, filepath.FromSlash(skill.Entry))
	var cmd *exec.Cmd
	switch strings.ToLower(filepath.Ext(script)) {
	case ".ps1":
		// 优先 PowerShell 7（UTF-8 默认）；没有 pwsh 时回落 Windows PowerShell。
		shell := "pwsh"
		if _, err := exec.LookPath(shell); err != nil {
			shell = "powershell"
		}
		// #nosec G204 -- 脚本路径来自已校验的技能目录，参数走环境变量而不是 shell 拼接。
		cmd = exec.CommandContext(runCtx, shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script)
	case ".bat", ".cmd":
		// #nosec G204 -- 同上；cmd /c 只执行已校验的脚本文件。
		cmd = exec.CommandContext(runCtx, "cmd", "/c", script)
	case ".sh":
		// #nosec G204 -- 同上。
		cmd = exec.CommandContext(runCtx, "bash", script)
	default:
		return "", errors.New("不支持的技能脚本类型")
	}
	cmd.Dir = skill.Dir
	cmd.Env = skillEnv(skill, args)
	var output limitedBuffer
	output.max = r.opts.MaxOutputBytes
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	text := strings.TrimSpace(output.String())
	if runCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("技能执行超时（%s）", timeout)
	}
	if err != nil {
		if text != "" {
			return "", fmt.Errorf("技能执行失败：%w；输出：%s", err, text)
		}
		return "", fmt.Errorf("技能执行失败：%w", err)
	}
	if text == "" {
		text = "（脚本执行完成，无输出）"
	}
	if output.truncated {
		text += "\n…（输出已截断）"
	}
	return text, nil
}

func skillEnv(skill Skill, args string) []string {
	env := make([]string, 0, 12)
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "PATHEXT", "TEMP", "TMP", "USERPROFILE", "HOME", "ComSpec", "SystemDrive"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	env = append(env,
		"FISH_SKILL_NAME="+skill.Name,
		"FISH_SKILL_DIR="+skill.Dir,
		"FISH_SKILL_ARGS="+args,
	)
	return env
}

type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 {
		b.max = defaultMaxOutput
	}
	remaining := b.max - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }
