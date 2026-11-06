// Package comment 轮询视频评论区并把新评论写入事件总线。
//
// 边界：只做 HTTP 采集与事件构造，不调用模型、不发送回复；
// 去重以 rpid 为键，事件 ID 使用契约里的 `comment:{oid}:{rpid}`。
//
// 覆盖承诺：
//   - 顶层评论按时间倒序翻页，每个对象每轮最多 MaxPages 页；到上限时用
//     comment_page_limit 记录截断，只保证已翻页前缀内不漏。
//   - 仍有楼中楼的根评论进入有界跟踪表；每轮先看首页并优先追新增区间，
//     稳态核验末页，历史中间页逐轮回填；单根每轮最多 MaxReplyPages 页，
//     未追平时记录 comment_reply_limit。
//   - 水位、根跟踪与去重只保存在进程内；重启后从头重扫，事件 ID 幂等由持久层兜底。
package comment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/collector"
	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
)

const (
	defaultAPIRoot = "https://api.bilibili.com"
	pageSize       = 20
	maxTextRunes   = 4000
	maxSeen        = 8192
	maxBodyBytes   = 1 << 20

	defaultMaxPages      = 5
	maxPagesUpper        = 50
	defaultMaxRoots      = 50
	maxRootsUpper        = 500
	defaultMaxReplyPages = 3
	maxReplyPagesUpper   = 20
)

// Kind 只展示受控类别，平台报文与凭据不进入错误文本。
type Kind string

const (
	KindInput    Kind = "input"
	KindAuth     Kind = "auth"
	KindNetwork  Kind = "network"
	KindProtocol Kind = "protocol"
	KindRate     Kind = "rate_limit"
	KindTimeout  Kind = "timeout"
)

// Error 保留程序可判断的原因链，不打印响应正文。
type Error struct {
	Kind  Kind
	Cause error `json:"-"`
}

func (e *Error) Error() string {
	kind := e.Kind
	switch kind {
	case KindInput, KindAuth, KindNetwork, KindProtocol, KindRate, KindTimeout:
	default:
		kind = KindProtocol
	}
	return "评论采集异常（" + string(kind) + "）"
}
func (e *Error) Unwrap() error { return e.Cause }

// 评论对象类型：视频用 type=1，动态用 type=17。
const (
	TypeVideo   = 1
	TypeDynamic = 17
)

// Target 是一个可轮询评论区的对象。
type Target struct {
	OID  string
	Type int
}

// Discoverer 发现自己账号当前需要轮询的视频/动态；失败时上层退回手动列表。
type Discoverer interface {
	Discover(ctx context.Context) ([]Target, error)
}

// SpaceSource 是 B站空间列表的最小接口（*bili.Client 实现，测试可替换）。
type SpaceSource interface {
	SpaceVideos(ctx context.Context, mid string, limit int) ([]bili.SpaceVideo, error)
	SpaceDynamics(ctx context.Context, mid string, limit int) ([]bili.SpaceDynamic, error)
}

// BiliDiscoverer 把空间视频/动态列表映射成评论 target。
type BiliDiscoverer struct {
	Client       SpaceSource
	MID          string
	VideoLimit   int
	DynamicLimit int
}

// Discover 拉两类列表；任意一类成功就返回可用的 target（部分失败不拖垮另一类）。
func (d BiliDiscoverer) Discover(ctx context.Context) ([]Target, error) {
	if d.Client == nil {
		return nil, errors.New("空间列表来源不能为 nil")
	}
	videoLimit, dynamicLimit := d.VideoLimit, d.DynamicLimit
	if videoLimit <= 0 {
		videoLimit = 50
	}
	if dynamicLimit <= 0 {
		dynamicLimit = 20
	}
	targets := make([]Target, 0, videoLimit+dynamicLimit)
	var errs []error
	if videos, err := d.Client.SpaceVideos(ctx, d.MID, videoLimit); err != nil {
		errs = append(errs, err)
	} else {
		for _, v := range videos {
			targets = append(targets, Target{OID: v.AID, Type: TypeVideo})
		}
	}
	if dynamics, err := d.Client.SpaceDynamics(ctx, d.MID, dynamicLimit); err != nil {
		errs = append(errs, err)
	} else {
		for _, dyn := range dynamics {
			targets = append(targets, Target{OID: dyn.ID, Type: TypeDynamic})
		}
	}
	if len(targets) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return targets, nil
}

// Options 由可信装配提供；CookieFile 只保存路径，内容不进日志。
type Options struct {
	// Videos 是手动追加的视频 oid；AutoDiscover 关闭时必须非空。
	Videos   []string
	Interval time.Duration
	// AutoDiscover 开启后按 DiscoverInterval 周期刷新自己账号的视频/动态列表。
	AutoDiscover     bool
	DiscoverInterval time.Duration
	Discoverer       Discoverer
	SelfID           string
	Cookie           string `json:"-"`
	CookieFile       string `json:"-"`
	HTTPTimeout      time.Duration
	APIRoot          string
	// MaxPages 限制单个对象每轮最多翻多少页顶层评论（时间倒序，0 用默认 5）。
	// 超限的旧评论不承诺本轮覆盖，按 comment_page_limit 记录截断。
	MaxPages int
	// MaxRoots 限制每个对象跟踪楼中楼的根评论数量（0 用默认 50，按最近活跃淘汰）。
	MaxRoots int
	// MaxReplyPages 限制单个根评论每轮最多翻多少页楼中楼（0 用默认 3，至少 2）。
	MaxReplyPages int
}

func DefaultOptions() Options {
	return Options{
		Interval:         time.Minute,
		DiscoverInterval: 10 * time.Minute,
		HTTPTimeout:      10 * time.Second,
		APIRoot:          defaultAPIRoot,
		MaxPages:         defaultMaxPages,
		MaxRoots:         defaultMaxRoots,
		MaxReplyPages:    defaultMaxReplyPages,
	}
}

// targetState 是单个视频/动态的轮询水位，只在 Start 的循环里读写。
type targetState struct {
	// covered 表示至少完成过一次到达列表末尾（或旧水位）的扫描。
	covered bool
	// resume 表示上一轮因错误或页数上限中断，下一轮不能凭「本页全是旧评论」早停。
	resume bool
	// frontier 是已连续覆盖到的最旧评论 (ctime, rpid)。
	frontierAt int64
	frontierID string
	// truncatedCount 避免页数上限日志每轮重复刷屏；列表变化时再记一次。
	truncatedCount int
	roots          map[string]*rootState
	rootOrder      []string
	clock          uint64
}

// rootState 是单个根评论的楼中楼回填进度。
type rootState struct {
	// count 是上一次观察到的回复总数，用于定位新增区间。
	count int
	// catchup 是新增区间下一待取的页码；0 表示没有待追的新增。
	catchup int
	// backfill 是历史回填下一待取的页码（从尾部向中间递减）；0 表示无需回填。
	backfill int
	// coveredAll 表示当前列表的所有页都已被首末页或回填覆盖。
	coveredAll bool
	// lagged 避免未追平日志每轮重复刷屏。
	lagged  bool
	touched uint64
}

// Collector 只有一个 Start 拥有轮询循环；Health 可并发读取。
type Collector struct {
	options Options
	log     *zap.Logger
	client  *http.Client
	mu      sync.RWMutex
	health  collector.Health
	seen    map[string]struct{}
	targets map[string]*targetState
	started atomic.Bool
}

var _ collector.Collector = (*Collector)(nil)

var positiveID = regexp.MustCompile(`^[1-9][0-9]{0,127}$`)
var numericID = regexp.MustCompile(`^[0-9]{1,128}$`)

// canonicalID 接受平台十进制 ID，去掉前导零后要求非零。
func canonicalID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !numericID.MatchString(value) {
		return "", false
	}
	value = strings.TrimLeft(value, "0")
	return value, value != ""
}

// New 只做静态校验，不读凭据文件、不访问平台。
func New(o Options, log *zap.Logger) (*Collector, error) {
	if log == nil || o.Interval <= 0 || o.HTTPTimeout <= 0 || (o.Cookie == "" && o.CookieFile == "") || strings.ContainsAny(o.Cookie+o.CookieFile, "\r\n\x00") {
		return nil, &Error{Kind: KindInput}
	}
	videos := make([]string, 0, len(o.Videos))
	unique := make(map[string]struct{}, len(o.Videos))
	for _, raw := range o.Videos {
		oid, ok := canonicalID(raw)
		if !ok {
			return nil, &Error{Kind: KindInput}
		}
		if _, duplicate := unique[oid]; duplicate {
			continue
		}
		unique[oid] = struct{}{}
		videos = append(videos, oid)
	}
	if len(videos) == 0 && !o.AutoDiscover {
		return nil, &Error{Kind: KindInput}
	}
	if o.AutoDiscover {
		if o.Discoverer == nil {
			return nil, &Error{Kind: KindInput}
		}
		if o.DiscoverInterval <= 0 {
			o.DiscoverInterval = 10 * time.Minute
		}
		if o.DiscoverInterval < time.Minute || o.DiscoverInterval > 24*time.Hour {
			return nil, &Error{Kind: KindInput}
		}
	}
	if o.SelfID != "" {
		self, ok := canonicalID(o.SelfID)
		if !ok {
			return nil, &Error{Kind: KindInput}
		}
		o.SelfID = self
	}
	if o.MaxPages == 0 {
		o.MaxPages = defaultMaxPages
	}
	if o.MaxPages < 0 || o.MaxPages > maxPagesUpper {
		return nil, &Error{Kind: KindInput}
	}
	if o.MaxRoots == 0 {
		o.MaxRoots = defaultMaxRoots
	}
	if o.MaxRoots < 0 || o.MaxRoots > maxRootsUpper {
		return nil, &Error{Kind: KindInput}
	}
	if o.MaxReplyPages == 0 {
		o.MaxReplyPages = defaultMaxReplyPages
	}
	// 首末页是「旧线程新回复」的检测底线，只给 1 页会永远看不到另一端。
	if o.MaxReplyPages < 2 || o.MaxReplyPages > maxReplyPagesUpper {
		return nil, &Error{Kind: KindInput}
	}
	o.Videos = videos
	if strings.TrimSpace(o.APIRoot) == "" {
		o.APIRoot = defaultAPIRoot
	}
	o.APIRoot = strings.TrimRight(strings.TrimSpace(o.APIRoot), "/")
	return &Collector{
		options: o,
		log:     log,
		client:  &http.Client{Timeout: o.HTTPTimeout},
		seen:    make(map[string]struct{}),
		targets: make(map[string]*targetState),
	}, nil
}

func (c *Collector) Name() string { return "bili-comment" }

func (c *Collector) Health() collector.Health {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.health
}

func (c *Collector) setOK(ok bool) {
	c.mu.Lock()
	c.health.OK = ok
	c.mu.Unlock()
}

func (c *Collector) report(errc chan<- error, err *Error) {
	c.mu.Lock()
	c.health.LastError = err.Error()
	c.mu.Unlock()
	select {
	case errc <- err:
	default:
	}
}

// Start 先立即轮询一轮，之后按 Interval 轮询；取消返回 nil，不关闭调用方 channel。
func (c *Collector) Start(ctx context.Context, out chan<- event.Event, errc chan<- error) error {
	if ctx == nil || out == nil || errc == nil || !c.started.CompareAndSwap(false, true) {
		return &Error{Kind: KindInput}
	}
	defer c.setOK(false)

	// 凭据在启动时读取一次：失败属于不可自愈错误，直接停采集等人工处理。
	cookie, err := bili.LoadCookie(c.options.Cookie, c.options.CookieFile)
	if err != nil {
		fatal := &Error{Kind: KindAuth, Cause: err}
		c.report(errc, fatal)
		return fatal
	}
	c.log.Info("评论采集已启动",
		zap.String("action", "comment_collector_start"),
		zap.Int("manual_videos", len(c.options.Videos)),
		zap.Bool("auto_discover", c.options.AutoDiscover),
		zap.Duration("interval", c.options.Interval),
	)
	targets := c.manualTargets()
	var lastDiscover time.Time
	for {
		if ctx.Err() != nil {
			return nil
		}
		if c.options.AutoDiscover && (lastDiscover.IsZero() || time.Since(lastDiscover) >= c.options.DiscoverInterval) {
			targets = c.refreshTargets(ctx)
			lastDiscover = time.Now()
		}
		if fatal := c.cycle(ctx, cookie, targets, out, errc); fatal != nil {
			return fatal
		}
		timer := time.NewTimer(c.options.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// manualTargets 把手动 videos 规整成视频类型 target。
func (c *Collector) manualTargets() []Target {
	out := make([]Target, 0, len(c.options.Videos))
	for _, oid := range c.options.Videos {
		out = append(out, Target{OID: oid, Type: TypeVideo})
	}
	return out
}

// refreshTargets 发现失败只记日志并退回手动列表，不向 err 通道报错。
func (c *Collector) refreshTargets(ctx context.Context) []Target {
	manual := c.manualTargets()
	discoverCtx, cancel := context.WithTimeout(ctx, c.options.HTTPTimeout)
	defer cancel()
	discovered, err := c.options.Discoverer.Discover(discoverCtx)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Warn("评论自动发现失败，仅轮询手动列表",
				zap.String("action", "comment_discover"),
				zap.Error(err),
			)
		}
		return manual
	}
	merged := mergeTargets(manual, discovered)
	c.log.Info("评论自动发现完成",
		zap.String("action", "comment_discover"),
		zap.Int("targets", len(merged)),
		zap.Int("discovered", len(discovered)),
	)
	return merged
}

// mergeTargets 按 (oid,type) 去重，手动列表优先。
func mergeTargets(manual, discovered []Target) []Target {
	out := make([]Target, 0, len(manual)+len(discovered))
	seen := make(map[string]struct{}, len(manual)+len(discovered))
	for _, group := range [][]Target{manual, discovered} {
		for _, target := range group {
			oid, ok := canonicalID(target.OID)
			if !ok || (target.Type != TypeVideo && target.Type != TypeDynamic) {
				continue
			}
			key := fmt.Sprintf("%d:%s", target.Type, oid)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, Target{OID: oid, Type: target.Type})
		}
	}
	return out
}

func targetKey(target Target) string {
	return fmt.Sprintf("%d:%s", target.Type, target.OID)
}

func (c *Collector) stateFor(target Target) *targetState {
	key := targetKey(target)
	st := c.targets[key]
	if st == nil {
		st = &targetState{roots: make(map[string]*rootState)}
		c.targets[key] = st
	}
	return st
}

// trackRoot 把根评论放进按最近活跃淘汰的有界集合。
func (st *targetState) trackRoot(root string, max int) *rootState {
	st.clock++
	if rs := st.roots[root]; rs != nil {
		rs.touched = st.clock
		return rs
	}
	if len(st.roots) >= max {
		oldest := 0
		for i, key := range st.rootOrder {
			state := st.roots[key]
			if state != nil && state.touched < st.roots[st.rootOrder[oldest]].touched {
				oldest = i
			}
		}
		delete(st.roots, st.rootOrder[oldest])
		st.rootOrder = append(st.rootOrder[:oldest], st.rootOrder[oldest+1:]...)
	}
	rs := &rootState{touched: st.clock}
	st.roots[root] = rs
	st.rootOrder = append(st.rootOrder, root)
	return rs
}

// cycle 轮询全部 target；只有凭据类错误会让采集退出，其余错误留给下一轮。
func (c *Collector) cycle(ctx context.Context, cookie string, targets []Target, out chan<- event.Event, errc chan<- error) *Error {
	active := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		active[targetKey(target)] = struct{}{}
	}
	for key := range c.targets {
		if _, ok := active[key]; !ok {
			delete(c.targets, key)
		}
	}
	ok := true
	for _, target := range targets {
		if ctx.Err() != nil {
			return nil
		}
		st := c.stateFor(target)
		fatal, healthy := c.pollTop(ctx, cookie, target, st, out, errc)
		if !healthy {
			ok = false
		}
		if fatal != nil {
			return fatal
		}
		if ctx.Err() != nil {
			return nil
		}
		fatal, healthy = c.pollRoots(ctx, cookie, target, st, out, errc)
		if !healthy {
			ok = false
		}
		if fatal != nil {
			return fatal
		}
	}
	c.setOK(ok)
	return nil
}

// pollTop 按时间倒序翻页顶层评论；(ctime,rpid) 水位确认追平后早停。
func (c *Collector) pollTop(ctx context.Context, cookie string, target Target, st *targetState, out chan<- event.Event, errc chan<- error) (*Error, bool) {
	healthy := true
	for pn := 1; pn <= c.options.MaxPages; pn++ {
		page, err := c.fetchPage(ctx, cookie, target, "", pn)
		if err != nil {
			if ctx.Err() != nil {
				return nil, healthy
			}
			healthy = false
			c.report(errc, err)
			st.resume = true
			if err.Kind == KindAuth {
				return err, healthy
			}
			return nil, healthy
		}
		_, done, stopped := c.processTopPage(ctx, target, st, page, out, errc)
		if stopped {
			return nil, healthy
		}
		if done {
			st.covered = true
			st.resume = false
			st.truncatedCount = 0
			return nil, healthy
		}
		if pn < c.options.MaxPages {
			continue
		}
		truncated := page.Count > pn*pageSize || (page.Count <= 0 && len(page.Replies) >= pageSize)
		if !truncated {
			// page.Count 已知且不超过本轮页容量：其实已经到末尾。
			st.covered = true
			st.resume = false
			st.truncatedCount = 0
			continue
		}
		mark := page.Count
		if mark <= 0 {
			mark = -1
		}
		if st.truncatedCount != mark {
			st.truncatedCount = mark
			c.log.Warn("顶层评论超过单轮页数上限，已截断",
				zap.String("action", "comment_page_limit"),
				zap.String("oid", target.OID),
				zap.Int("comment_type", target.Type),
				zap.Int("pages", pn),
				zap.Int("page_count", page.Count),
			)
		}
		// 已翻的 1..MaxPages 是从最新评论开始的连续前缀，可据此水位早停；
		// 更旧的页本来就在覆盖承诺之外。
		st.covered = true
		st.resume = false
	}
	return nil, healthy
}

// processTopPage 处理一页顶层评论，返回本页新事件数、是否已到列表末尾、是否因取消停止。
func (c *Collector) processTopPage(ctx context.Context, target Target, st *targetState, page replyPage, out chan<- event.Event, errc chan<- error) (int, bool, bool) {
	newCount := 0
	for i := range page.Replies {
		reply := page.Replies[i]
		if root := string(reply.Rpid); positiveID.MatchString(root) && (reply.Rcount > 0 || len(reply.Replies) > 0) {
			st.trackRoot(root, c.options.MaxRoots)
		}
		emitted, stopped := c.emitReply(ctx, target, reply, out, errc)
		if emitted {
			newCount++
		}
		if stopped {
			return newCount, false, true
		}
		for _, child := range reply.Replies {
			emitted, stopped = c.emitReply(ctx, target, child, out, errc)
			if emitted {
				newCount++
			}
			if stopped {
				return newCount, false, true
			}
		}
	}
	if len(page.Replies) > 0 {
		last := page.Replies[len(page.Replies)-1]
		if rpid := string(last.Rpid); positiveID.MatchString(rpid) && last.Ctime > 0 {
			if st.frontierAt == 0 || atOrBefore(last.Ctime, rpid, st.frontierAt, st.frontierID) {
				st.frontierAt, st.frontierID = last.Ctime, rpid
			}
		}
	}
	if len(page.Replies) < pageSize {
		return newCount, true, false
	}
	if st.covered && !st.resume {
		if newCount == 0 {
			return newCount, true, false
		}
		first := page.Replies[0]
		if rpid := string(first.Rpid); positiveID.MatchString(rpid) && atOrBefore(first.Ctime, rpid, st.frontierAt, st.frontierID) {
			return newCount, true, false
		}
	}
	return newCount, false, false
}

func (c *Collector) emitReply(ctx context.Context, target Target, reply apiReply, out chan<- event.Event, errc chan<- error) (bool, bool) {
	ev, keep, err := c.normalize(target, reply)
	if err != nil {
		c.report(errc, err)
		return false, false
	}
	if !keep {
		return false, false
	}
	select {
	case out <- ev:
		c.mu.Lock()
		c.health.LastEventAt = time.Now().UTC()
		c.mu.Unlock()
		return true, false
	case <-ctx.Done():
		return true, true
	}
}

func (c *Collector) processReplies(ctx context.Context, target Target, replies []apiReply, out chan<- event.Event, errc chan<- error) (int, bool) {
	newCount := 0
	for _, reply := range replies {
		emitted, stopped := c.emitReply(ctx, target, reply, out, errc)
		if emitted {
			newCount++
		}
		if stopped {
			return newCount, true
		}
	}
	return newCount, false
}

// pollRoots 轮询已跟踪根评论的楼中楼；单个根失败不拖垮其他根。
func (c *Collector) pollRoots(ctx context.Context, cookie string, target Target, st *targetState, out chan<- event.Event, errc chan<- error) (*Error, bool) {
	healthy := true
	for _, root := range append([]string(nil), st.rootOrder...) {
		rs := st.roots[root]
		if rs == nil {
			continue
		}
		fatal, ok := c.pollRoot(ctx, cookie, target, root, rs, out, errc)
		if !ok {
			healthy = false
		}
		if fatal != nil {
			return fatal, healthy
		}
		if ctx.Err() != nil {
			return nil, healthy
		}
	}
	return nil, healthy
}

// pollRoot 增量核验一个根评论的楼中楼：首页必看，新增区间优先追，
// 稳态核验末页，历史中间页逐轮回填。
func (c *Collector) pollRoot(ctx context.Context, cookie string, target Target, root string, rs *rootState, out chan<- event.Event, errc chan<- error) (*Error, bool) {
	healthy := true
	fetched := make(map[int]struct{}, c.options.MaxReplyPages)
	fetch := func(pn int) (replyPage, *Error) {
		fetched[pn] = struct{}{}
		return c.fetchPage(ctx, cookie, target, root, pn)
	}
	report := func(err *Error) (*Error, bool) {
		if ctx.Err() != nil {
			return nil, healthy
		}
		c.report(errc, err)
		healthy = false
		if err.Kind == KindAuth {
			return err, healthy
		}
		return nil, healthy
	}

	first, err := fetch(1)
	if err != nil {
		return report(err)
	}
	if _, stopped := c.processReplies(ctx, target, first.Replies, out, errc); stopped {
		return nil, healthy
	}
	count := first.Count
	if len(first.Replies) > count {
		count = len(first.Replies)
	}
	lastPage := (count + pageSize - 1) / pageSize
	if lastPage < 1 {
		lastPage = 1
	}

	// 时间正序的新回复在尾部：从旧总数所在页开始追，跨页也不会只取末页。
	if rs.count > 0 && count > rs.count {
		growStart := rs.count/pageSize + 1
		if rs.catchup == 0 || growStart < rs.catchup {
			rs.catchup = growStart
		}
	}
	rs.count = count
	if rs.catchup > lastPage {
		rs.catchup = 0
	}
	if rs.backfill > lastPage-1 {
		rs.backfill = lastPage - 1
	}
	if rs.backfill < 2 {
		rs.backfill = 0
	}
	if lastPage <= 2 {
		rs.coveredAll = true
		rs.backfill = 0
	}

	budget := c.options.MaxReplyPages - 1
	for budget > 0 && rs.catchup > 0 && rs.catchup <= lastPage {
		pn := rs.catchup
		if _, ok := fetched[pn]; ok {
			rs.catchup++
			if rs.catchup > lastPage {
				rs.catchup = 0
			}
			continue
		}
		page, err := fetch(pn)
		if err != nil {
			return report(err)
		}
		if _, stopped := c.processReplies(ctx, target, page.Replies, out, errc); stopped {
			return nil, healthy
		}
		budget--
		if pn >= lastPage {
			rs.catchup = 0
		} else {
			rs.catchup = pn + 1
		}
	}
	// 稳态每轮核验末页；历史回填未完成时把预算让给回填。
	if budget > 0 && lastPage > 1 && rs.backfill == 0 {
		if _, ok := fetched[lastPage]; !ok {
			page, err := fetch(lastPage)
			if err != nil {
				return report(err)
			}
			if _, stopped := c.processReplies(ctx, target, page.Replies, out, errc); stopped {
				return nil, healthy
			}
			budget--
		}
	}
	if rs.backfill == 0 && !rs.coveredAll && rs.catchup == 0 && lastPage > 2 {
		rs.backfill = lastPage - 1
	}
	for budget > 0 && rs.backfill > 1 {
		pn := rs.backfill
		if _, ok := fetched[pn]; ok {
			rs.backfill--
			if rs.backfill <= 1 {
				rs.backfill = 0
			}
			continue
		}
		page, err := fetch(pn)
		if err != nil {
			return report(err)
		}
		if _, stopped := c.processReplies(ctx, target, page.Replies, out, errc); stopped {
			return nil, healthy
		}
		budget--
		rs.backfill--
		if rs.backfill <= 1 {
			rs.backfill = 0
		}
	}
	rs.coveredAll = rs.catchup == 0 && rs.backfill == 0
	if !rs.coveredAll {
		if !rs.lagged {
			rs.lagged = true
			c.log.Warn("楼中楼尚未追平，按页数上限分批回填",
				zap.String("action", "comment_reply_limit"),
				zap.String("oid", target.OID),
				zap.String("root", root),
				zap.Int("total", count),
				zap.Int("last_page", lastPage),
				zap.Int("fetched_pages", len(fetched)),
			)
		}
	} else {
		rs.lagged = false
	}
	return nil, healthy
}

// atOrBefore 判断 (ctime,rpid) 是否不晚于水位；rpid 是同秒内的单调游标。
func atOrBefore(ctime int64, rpid string, frontierAt int64, frontierID string) bool {
	if frontierAt == 0 || frontierID == "" {
		return false
	}
	if ctime != frontierAt {
		return ctime < frontierAt
	}
	return compareDecimal(rpid, frontierID) <= 0
}

func compareDecimal(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// decimal 接受平台返回的数字或数字字符串；只保留十进制并拒绝 0。
type decimal string

func (d *decimal) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		return nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		raw = strings.TrimSpace(text)
	}
	if raw == "" {
		return nil
	}
	if !positiveID.MatchString(raw) {
		return errors.New("平台ID不是正整数")
	}
	*d = decimal(raw)
	return nil
}

// optionalID 接受平台数字或数字字符串；0/空表示「没有」。
// 顶层评论的 root/parent 就是 0，不能用拒绝 0 的 decimal 解析。
type optionalID string

func (d *optionalID) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		return nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		raw = strings.TrimSpace(text)
	}
	if raw == "" {
		return nil
	}
	if !numericID.MatchString(raw) {
		return errors.New("平台ID不是非负整数")
	}
	// 全零等价于没有（顶层评论），其余去掉前导零。
	*d = optionalID(strings.TrimLeft(raw, "0"))
	return nil
}

type apiReply struct {
	Rpid    decimal    `json:"rpid"`
	Mid     decimal    `json:"mid"`
	Ctime   int64      `json:"ctime"`
	Root    optionalID `json:"root"`
	Parent  optionalID `json:"parent"`
	Rcount  int        `json:"rcount"`
	Replies []apiReply `json:"replies"`
	Member  struct {
		Mid   decimal `json:"mid"`
		UName string  `json:"uname"`
	} `json:"member"`
	Content struct {
		Message string `json:"message"`
	} `json:"content"`
}

type envelope struct {
	Code int `json:"code"`
	Data struct {
		Replies []apiReply `json:"replies"`
		Page    struct {
			Count int `json:"count"`
			Num   int `json:"num"`
			Size  int `json:"size"`
		} `json:"page"`
	} `json:"data"`
}

type replyPage struct {
	Replies []apiReply
	Count   int
}

// fetchPage 发一次只读请求，绝不重试；root 为空表示顶层评论，否则为楼中楼。
func (c *Collector) fetchPage(parent context.Context, cookie string, target Target, root string, pn int) (replyPage, *Error) {
	ctx, cancel := context.WithTimeout(parent, c.options.HTTPTimeout)
	defer cancel()
	query := url.Values{}
	query.Set("type", fmt.Sprint(target.Type))
	query.Set("oid", target.OID)
	query.Set("pn", fmt.Sprint(pn))
	query.Set("ps", fmt.Sprint(pageSize))
	path := "/x/v2/reply"
	if root == "" {
		// 时间倒序是分页水位成立的前提；热度排序会静默漏收。
		query.Set("sort", "0")
	} else {
		path = "/x/v2/reply/reply"
		query.Set("root", root)
	}
	endpoint := c.options.APIRoot + path + "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return replyPage{}, &Error{Kind: KindInput}
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) RaiseMyFish/comment")
	resp, err := c.client.Do(req)
	if err != nil {
		if parent.Err() != nil {
			return replyPage{}, &Error{Kind: KindNetwork, Cause: parent.Err()}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return replyPage{}, &Error{Kind: KindTimeout}
		}
		return replyPage{}, &Error{Kind: KindNetwork}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return replyPage{}, &Error{Kind: statusKind(resp.StatusCode), Cause: fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		return replyPage{}, &Error{Kind: KindProtocol}
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return replyPage{}, &Error{Kind: KindProtocol}
	}
	if env.Code != 0 {
		return replyPage{}, &Error{Kind: codeKind(env.Code), Cause: fmt.Errorf("平台返回 code %d", env.Code)}
	}
	return replyPage{Replies: env.Data.Replies, Count: env.Data.Page.Count}, nil
}

func statusKind(status int) Kind {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return KindAuth
	case http.StatusTooManyRequests, http.StatusPreconditionFailed:
		return KindRate
	default:
		return KindNetwork
	}
}

func codeKind(code int) Kind {
	switch code {
	case -101, -111, -403:
		return KindAuth
	case -352, -412:
		return KindRate
	default:
		return KindProtocol
	}
}

// normalize 校验单条评论；重复与自己的评论返回 keep=false。
func (c *Collector) normalize(target Target, r apiReply) (event.Event, bool, *Error) {
	invalid := func() (event.Event, bool, *Error) { return event.Event{}, false, &Error{Kind: KindProtocol} }
	rpid, mid := string(r.Rpid), string(r.Member.Mid)
	if mid == "" {
		mid = string(r.Mid)
	}
	if !positiveID.MatchString(rpid) || !positiveID.MatchString(mid) {
		return invalid()
	}
	if !c.firstSeen(target, rpid) {
		return event.Event{}, false, nil
	}
	if mid == c.options.SelfID {
		return event.Event{}, false, nil
	}
	text := r.Content.Message
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return invalid()
	}
	if runes := []rune(text); len(runes) > maxTextRunes {
		text = string(runes[:maxTextRunes])
	}
	name := r.Member.UName
	if !utf8.ValidString(name) || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		name = ""
	}
	if utf8.RuneCountInString(name) > 128 {
		name = string([]rune(name)[:128])
	}
	if r.Ctime <= 0 {
		return invalid()
	}
	meta, err := json.Marshal(struct {
		Source      string `json:"source"`
		CommentType int    `json:"comment_type"`
		Root        string `json:"root,omitempty"`
		Parent      string `json:"parent,omitempty"`
	}{"bili-comment", target.Type, string(r.Root), string(r.Parent)})
	if err != nil {
		return invalid()
	}
	return event.Event{
		ID:       "comment:" + target.OID + ":" + rpid,
		Channel:  event.ChComment,
		Dir:      event.DirIn,
		Session:  target.OID,
		FromID:   mid,
		FromName: name,
		Text:     text,
		Private:  false,
		ReplyTo:  rpid,
		At:       time.Unix(r.Ctime, 0).UTC(),
		Status:   event.StatusPending,
		Meta:     meta,
	}, true, nil
}

// firstSeen 以 type:oid:rpid 为键去重；超过上限清空重来，持久化层仍会按事件 ID 幂等。
func (c *Collector) firstSeen(target Target, rpid string) bool {
	key := fmt.Sprintf("%d:%s:%s", target.Type, target.OID, rpid)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.seen[key]; ok {
		return false
	}
	if len(c.seen) >= maxSeen {
		c.seen = make(map[string]struct{}, maxSeen)
	}
	c.seen[key] = struct{}{}
	return true
}
