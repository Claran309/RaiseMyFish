package reasoning

// 本文件测 5 个副作用工具的护栏：开关、额度、限频、白名单、不取关主人/自己、dry-run。

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type fakeGift struct {
	calls int
	last  GiftRequest
	err   error
}

func (f *fakeGift) SendGift(_ context.Context, req GiftRequest) (string, error) {
	f.calls++
	f.last = req
	if f.err != nil {
		return "", f.err
	}
	return "礼物已送出", nil
}

type fakeSocial struct {
	follows, unfollows, likes, dms, posts int
	lastMID, lastText                     string
	err                                   error
}

func (f *fakeSocial) Follow(_ context.Context, mid string) error {
	f.follows++
	f.lastMID = mid
	return f.err
}
func (f *fakeSocial) Unfollow(_ context.Context, mid string) error {
	f.unfollows++
	f.lastMID = mid
	return f.err
}
func (f *fakeSocial) Like(context.Context, string, int) error {
	f.likes++
	return f.err
}
func (f *fakeSocial) SendDM(_ context.Context, mid, text string) error {
	f.dms++
	f.lastMID, f.lastText = mid, text
	return f.err
}
func (f *fakeSocial) PostDynamic(_ context.Context, text string) error {
	f.posts++
	f.lastText = text
	return f.err
}

type fakeQuota struct {
	used, released int
	err            error
	limit          int
}

func (f *fakeQuota) Reserve(_ string, num, limit int) error {
	if f.err != nil {
		return f.err
	}
	f.limit = limit
	f.used += num
	return nil
}
func (f *fakeQuota) Release(_ string, num int) {
	f.released += num
	f.used -= num
}
func (f *fakeQuota) Used(string) (int, error) { return f.used, nil }

func sideDeps() (SideEffectDeps, *fakeGift, *fakeSocial, *fakeQuota) {
	g := &fakeGift{}
	s := &fakeSocial{}
	q := &fakeQuota{}
	clock := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	d := SideEffectDeps{
		Enabled:      true,
		Gift:         g,
		Social:       s,
		Quota:        q,
		QuotaView:    q,
		GiftEnabled:  true,
		GiftReal:     true,
		GiftID:       "31036",
		MaxNum:       1,
		DailyLimit:   2,
		AllowedRooms: []string{"30308220"},
		OwnerMID:     "313591854",
		SelfMID:      "3747574135655903",
		AllowedDMMIDs: []string{
			"777",
		},
		Room: "30308220",
		Now:  func() time.Time { return clock },
		Log:  zap.NewNop(),
	}
	return d, g, s, q
}

// TestSideEffectToolsRegistered 5 个副作用工具全部注册。
func TestSideEffectToolsRegistered(t *testing.T) {
	d, _, _, _ := sideDeps()
	box, err := BuildSideEffectTools(d)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	names := box.Names()
	want := []string{ToolGift, ToolFollow, ToolLike, ToolDM, ToolPostDynamic}
	if len(names) != len(want) {
		t.Fatalf("工具数 = %d：%v", len(names), names)
	}
	for i, name := range want {
		if names[i] != name {
			t.Errorf("第 %d 个 = %q，期望 %q", i, names[i], name)
		}
	}
	desc := box.Descriptions()
	for _, name := range want {
		if !strings.Contains(desc, name) {
			t.Errorf("描述缺少 %s", name)
		}
	}
}

// TestSideEffectSwitchOffRejectsAll 总开关关闭时全部拒绝，且不触碰执行器。
func TestSideEffectSwitchOffRejectsAll(t *testing.T) {
	d, g, s, q := sideDeps()
	d.Enabled = false
	box, _ := BuildSideEffectTools(d)

	cases := []struct{ name, args string }{
		{ToolGift, `{"gift_id":"31036","num":1}`},
		{ToolFollow, `{"mid":"123","action":"follow"}`},
		{ToolLike, `{"count":1}`},
		{ToolDM, `{"mid":"313591854","text":"在吗"}`},
		{ToolPostDynamic, `{"text":"今天也在钓鱼"}`},
	}
	for _, tc := range cases {
		if _, err := box.Run(context.Background(), tc.name, tc.args); err == nil || !strings.Contains(err.Error(), "未授权") {
			t.Errorf("%s 未授权时应拒绝，实际 %v", tc.name, err)
		}
	}
	if g.calls+s.follows+s.likes+s.dms+s.posts != 0 {
		t.Error("开关关闭时不应调用任何执行器")
	}
	if q.used != 0 {
		t.Error("开关关闭时不应占额度")
	}
}

// TestGiftWhitelistAndMaxNum 房间白名单与单次数量上限。
func TestGiftWhitelistAndMaxNum(t *testing.T) {
	d, g, _, _ := sideDeps()
	d.AllowedRooms = []string{"999"}
	box, _ := BuildSideEffectTools(d)
	if _, err := box.Run(context.Background(), ToolGift, `{"gift_id":"31036","num":1}`); err == nil {
		t.Error("房间不在白名单应拒绝")
	}

	d2, g2, _, _ := sideDeps()
	box2, _ := BuildSideEffectTools(d2)
	if _, err := box2.Run(context.Background(), ToolGift, `{"gift_id":"31036","num":9}`); err == nil {
		t.Error("超过 MaxNum 应拒绝")
	}
	if g.calls+g2.calls != 0 {
		t.Error("护栏拦截后不应调用送礼执行器")
	}
}

// TestGiftQuotaDenied 额度不足直接拒绝，不调用执行器。
func TestGiftQuotaDenied(t *testing.T) {
	d, g, _, q := sideDeps()
	q.err = errors.New("今日额度已用完")
	box, _ := BuildSideEffectTools(d)

	if _, err := box.Run(context.Background(), ToolGift, `{"gift_id":"31036","num":1}`); err == nil {
		t.Fatal("额度不足应拒绝")
	}
	if g.calls != 0 {
		t.Error("额度不足时不应送礼")
	}
}

// TestGiftDryRunWhenNotReal 未开真实送礼时只说明，不执行、不占额度。
func TestGiftDryRunWhenNotReal(t *testing.T) {
	d, g, _, q := sideDeps()
	d.GiftReal = false
	box, _ := BuildSideEffectTools(d)

	got, err := box.Run(context.Background(), ToolGift, `{"gift_id":"31036","num":1}`)
	if err != nil {
		t.Fatalf("dry-run 不应报错：%v", err)
	}
	if !strings.Contains(got, "dry-run") {
		t.Errorf("应说明 dry-run：%q", got)
	}
	if g.calls != 0 {
		t.Error("dry-run 不应真实送礼")
	}
	if q.used != 0 {
		t.Errorf("dry-run 不应占额度，实际 used=%d", q.used)
	}
}

// TestGiftRealSendsAndReserves 真实送礼：占额度并调用执行器。
func TestGiftRealSendsAndReserves(t *testing.T) {
	d, g, _, q := sideDeps()
	box, _ := BuildSideEffectTools(d)

	if _, err := box.Run(context.Background(), ToolGift, `{"gift_id":"31036","num":1}`); err != nil {
		t.Fatalf("送礼失败：%v", err)
	}
	if g.calls != 1 || g.last.GiftID != "31036" || g.last.Num != 1 || g.last.Room != "30308220" {
		t.Errorf("送礼参数不符：%+v", g.last)
	}
	if q.used != 1 || q.limit != 2 {
		t.Errorf("额度占用不符：used=%d limit=%d", q.used, q.limit)
	}
}

// TestGiftDynamicSwitch 面板热切换：同一个工具箱按开→关→开跟随门控，无需重建。
func TestGiftDynamicSwitch(t *testing.T) {
	d, g, _, q := sideDeps()
	var enabled atomic.Bool
	enabled.Store(true)
	d.GiftEnabledFunc = enabled.Load
	box, _ := BuildSideEffectTools(d)

	// 开：真实送礼
	if _, err := box.Run(context.Background(), ToolGift, `{"gift_id":"31036","num":1}`); err != nil {
		t.Fatalf("开启时送礼失败：%v", err)
	}
	if g.calls != 1 || q.used != 1 {
		t.Fatalf("开启时应真实送礼：calls=%d used=%d", g.calls, q.used)
	}

	// 关：立即拒绝，不碰执行器、不占/不退额度
	enabled.Store(false)
	if _, err := box.Run(context.Background(), ToolGift, `{"gift_id":"31036","num":1}`); err == nil || !strings.Contains(err.Error(), "关闭") {
		t.Fatalf("关闭后应立即拒绝，实际 err=%v", err)
	}
	if g.calls != 1 || q.used != 1 || q.released != 0 {
		t.Fatalf("关闭后不应执行/动额度：calls=%d used=%d released=%d", g.calls, q.used, q.released)
	}

	// 再开：同一个工具箱恢复执行
	enabled.Store(true)
	if _, err := box.Run(context.Background(), ToolGift, `{"gift_id":"31036","num":1}`); err != nil {
		t.Fatalf("重新开启后送礼失败：%v", err)
	}
	if g.calls != 2 || q.used != 2 {
		t.Errorf("重新开启后应再送礼：calls=%d used=%d", g.calls, q.used)
	}
}

// blockingGift 在 SendGift 内阻塞，用于制造「已进入执行器」的在途窗口。
type blockingGift struct {
	entered chan struct{}
	release chan struct{}
	calls   int
	last    GiftRequest
}

func (b *blockingGift) SendGift(_ context.Context, req GiftRequest) (string, error) {
	b.calls++
	b.last = req
	close(b.entered)
	<-b.release
	return "礼物已送出", nil
}

// TestGiftInFlightKeepsAccountingWhenGateCloses 在途请求不撤回：
// 进入执行器后关开关，请求仍完成并按成功记账；后续调用才被门控拒绝。
func TestGiftInFlightKeepsAccountingWhenGateCloses(t *testing.T) {
	d, _, _, q := sideDeps()
	var enabled atomic.Bool
	enabled.Store(true)
	d.GiftEnabledFunc = enabled.Load
	core, logs := observer.New(zap.InfoLevel)
	d.Log = zap.New(core)
	g := &blockingGift{entered: make(chan struct{}), release: make(chan struct{})}
	d.Gift = g
	box, err := BuildSideEffectTools(d)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := box.Run(context.Background(), ToolGift, `{"gift_id":"31036","num":1}`)
		done <- result{out: out, err: err}
	}()

	select {
	case <-g.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("送礼未进入执行器")
	}
	// 已进入执行器：关开关不撤回在途请求。
	enabled.Store(false)
	close(g.release)
	got := <-done

	if got.err != nil {
		t.Fatalf("在途送礼不应报错：%v", got.err)
	}
	if !strings.Contains(got.out, "已送出") {
		t.Errorf("在途送礼应完成：%q", got.out)
	}
	if g.calls != 1 || q.used != 1 || q.released != 0 {
		t.Errorf("在途送礼应记账且不退款：calls=%d used=%d released=%d", g.calls, q.used, q.released)
	}
	entries := logs.FilterMessage("副作用工具调用").All()
	if len(entries) != 1 {
		t.Fatalf("应有一条副作用日志，实际 %d", len(entries))
	}
	if got := entries[0].ContextMap()["result"]; got != "ok" {
		t.Errorf("日志 result = %v，期望 ok", got)
	}

	// 关闭后的下一次调用被门控拦下，已记账额度保持不变。
	if _, err := box.Run(context.Background(), ToolGift, `{"gift_id":"31036","num":1}`); err == nil {
		t.Error("关闭后下一次调用应被拒绝")
	}
	if g.calls != 1 || q.used != 1 || q.released != 0 {
		t.Errorf("拒绝不应再执行/退额度：calls=%d used=%d released=%d", g.calls, q.used, q.released)
	}
}

// TestFollowNeverUnfollowOwnerOrSelf 永不取关主人/自己，其余照常执行。
func TestFollowNeverUnfollowOwnerOrSelf(t *testing.T) {
	d, _, s, _ := sideDeps()
	box, _ := BuildSideEffectTools(d)

	got, err := box.Run(context.Background(), ToolFollow, `{"mid":"313591854,3747574135655903,123","action":"unfollow"}`)
	if err != nil {
		t.Fatalf("取关失败：%v", err)
	}
	if s.unfollows != 1 || s.lastMID != "123" {
		t.Errorf("只应取关 123，实际 unfollows=%d last=%s", s.unfollows, s.lastMID)
	}
	if !strings.Contains(got, "跳过 2 个") {
		t.Errorf("应报告跳过主人与自己：%q", got)
	}
}

// TestFollowBatchLimitAndRate 单批 ≤5、1 分钟限频。
func TestFollowBatchLimitAndRate(t *testing.T) {
	d, _, s, _ := sideDeps()
	box, _ := BuildSideEffectTools(d)

	if _, err := box.Run(context.Background(), ToolFollow, `{"mid":"1,2,3,4,5,6","action":"follow"}`); err == nil {
		t.Error("超过 5 个应拒绝")
	}
	if _, err := box.Run(context.Background(), ToolFollow, `{"mid":"1","action":"follow"}`); err != nil {
		t.Fatalf("首次关注失败：%v", err)
	}
	if _, err := box.Run(context.Background(), ToolFollow, `{"mid":"2","action":"follow"}`); err == nil {
		t.Error("1 分钟内第二次应被限频")
	}
	if s.follows != 1 {
		t.Errorf("应只关注 1 次，实际 %d", s.follows)
	}
}

// TestLikeCountAndRate 点赞数量 1-3，1 分钟限频。
func TestLikeCountAndRate(t *testing.T) {
	d, _, s, _ := sideDeps()
	box, _ := BuildSideEffectTools(d)

	if _, err := box.Run(context.Background(), ToolLike, `{"count":5}`); err == nil {
		t.Error("count 超限应拒绝")
	}
	if _, err := box.Run(context.Background(), ToolLike, `{"count":2}`); err != nil {
		t.Fatalf("首次点赞失败：%v", err)
	}
	if _, err := box.Run(context.Background(), ToolLike, `{"count":1}`); err == nil {
		t.Error("1 分钟内第二次点赞应被限频")
	}
	if s.likes != 1 {
		t.Errorf("应只点赞 1 次，实际 %d", s.likes)
	}
}

// TestDMWhitelistAndLength 只允许主人/白名单，长度 ≤200。
func TestDMWhitelistAndLength(t *testing.T) {
	d, _, s, _ := sideDeps()
	box, _ := BuildSideEffectTools(d)

	// 非白名单
	if _, err := box.Run(context.Background(), ToolDM, `{"mid":"999","text":"你好"}`); err == nil {
		t.Error("非白名单应拒绝")
	}
	// 白名单放行
	if _, err := box.Run(context.Background(), ToolDM, `{"mid":"777","text":"你好"}`); err != nil {
		t.Fatalf("白名单失败：%v", err)
	}
	if s.dms != 1 || s.lastMID != "777" || s.lastText != "你好" {
		t.Errorf("私信参数不符：%+v", s)
	}
	// 限频
	if _, err := box.Run(context.Background(), ToolDM, `{"mid":"777","text":"再来"}`); err == nil {
		t.Error("1 分钟内第二条私信应被限频")
	}
	// 超长
	d2, _, _, _ := sideDeps()
	box2, _ := BuildSideEffectTools(d2)
	if _, err := box2.Run(context.Background(), ToolDM, `{"mid":"777","text":"`+strings.Repeat("鱼", 201)+`"}`); err == nil {
		t.Error("超 200 字应拒绝")
	}
}

// TestPostDynamicDefaultOffAndRate 发动态默认关；开了也要 10 分钟限频。
func TestPostDynamicDefaultOffAndRate(t *testing.T) {
	d, _, s, _ := sideDeps()
	box, _ := BuildSideEffectTools(d)
	if _, err := box.Run(context.Background(), ToolPostDynamic, `{"text":"今天也在钓鱼"}`); err == nil {
		t.Error("默认关闭时应拒绝")
	}
	if s.posts != 0 {
		t.Error("关闭时不应发动态")
	}

	d2, _, s2, _ := sideDeps()
	d2.AllowPostDynamic = true
	box2, _ := BuildSideEffectTools(d2)
	if _, err := box2.Run(context.Background(), ToolPostDynamic, `{"text":"今天也在钓鱼"}`); err != nil {
		t.Fatalf("开启后应能发：%v", err)
	}
	if s2.posts != 1 {
		t.Errorf("应发 1 次，实际 %d", s2.posts)
	}
	if _, err := box2.Run(context.Background(), ToolPostDynamic, `{"text":"再来一条"}`); err == nil {
		t.Error("10 分钟内第二条应被限频")
	}
}

// TestSideEffectLimiterSharedAcrossToolboxes O15：两个事件各建工具箱但共用一个限频表，
// 第二事件同动作在窗口内被拒；跨过窗口后按原窗口长度重新放行。
func TestSideEffectLimiterSharedAcrossToolboxes(t *testing.T) {
	limiter := NewSideEffectLimiter()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	// 两个「事件」各取一份依赖，但注入同一个 limiter/时钟/执行器。
	d1, _, social, _ := sideDeps()
	d1.Limiter = limiter
	d1.Now = clock
	box1, err := BuildSideEffectTools(d1)
	if err != nil {
		t.Fatalf("第一事件装配失败：%v", err)
	}
	d2, _, _, _ := sideDeps()
	d2.Social = social
	d2.Limiter = limiter
	d2.Now = clock
	box2, err := BuildSideEffectTools(d2)
	if err != nil {
		t.Fatalf("第二事件装配失败：%v", err)
	}

	for _, tc := range []struct{ name, args string }{
		{ToolFollow, `{"mid":"123","action":"follow"}`},
		{ToolLike, `{"count":1}`},
		{ToolDM, `{"mid":"777","text":"你好"}`},
	} {
		if _, err := box1.Run(context.Background(), tc.name, tc.args); err != nil {
			t.Fatalf("第一事件 %s 应放行：%v", tc.name, err)
		}
	}

	// 第二事件同一窗口内被限：必须不触碰执行器。
	for _, tc := range []struct{ name, args string }{
		{ToolFollow, `{"mid":"456","action":"follow"}`},
		{ToolLike, `{"count":1}`},
		{ToolDM, `{"mid":"777","text":"再来"}`},
	} {
		if _, err := box2.Run(context.Background(), tc.name, tc.args); err == nil || !strings.Contains(err.Error(), "频繁") {
			t.Errorf("第二事件 %s 应被跨事件限频，实际 err=%v", tc.name, err)
		}
	}
	if social.follows != 1 || social.likes != 1 || social.dms != 1 {
		t.Errorf("执行器只应各执行一次：follows=%d likes=%d dms=%d", social.follows, social.likes, social.dms)
	}

	// 窗口不变：1 分钟后重新放行。
	now = now.Add(61 * time.Second)
	if _, err := box2.Run(context.Background(), ToolFollow, `{"mid":"456","action":"follow"}`); err != nil {
		t.Fatalf("跨过窗口后应放行：%v", err)
	}
	if social.follows != 2 {
		t.Errorf("窗口过后应再执行一次：follows=%d", social.follows)
	}
}

// TestSideEffectLimiterScopeByActorAndAction 限频键是 actor+动作：
// 不同 SelfMID 与不同动作互不占用窗口。
func TestSideEffectLimiterScopeByActorAndAction(t *testing.T) {
	limiter := NewSideEffectLimiter()
	clock := func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) }

	d1, _, social, _ := sideDeps()
	d1.SelfMID = "111"
	d1.Limiter = limiter
	d1.Now = clock
	box1, _ := BuildSideEffectTools(d1)
	d2, _, _, _ := sideDeps()
	d2.SelfMID = "222"
	d2.Social = social
	d2.Limiter = limiter
	d2.Now = clock
	box2, _ := BuildSideEffectTools(d2)

	// 同动作、不同 actor：各自放行。
	if _, err := box1.Run(context.Background(), ToolFollow, `{"mid":"123","action":"follow"}`); err != nil {
		t.Fatalf("actor1 关注失败：%v", err)
	}
	if _, err := box2.Run(context.Background(), ToolFollow, `{"mid":"456","action":"follow"}`); err != nil {
		t.Fatalf("不同 actor 不应共用窗口：%v", err)
	}
	if social.follows != 2 {
		t.Errorf("两个 actor 各应执行一次：follows=%d", social.follows)
	}

	// 同 actor、不同动作：follow 的窗口不影响 dm。
	if _, err := box1.Run(context.Background(), ToolDM, `{"mid":"777","text":"你好"}`); err != nil {
		t.Fatalf("不同动作不应共用窗口：%v", err)
	}
}

// TestSideEffectNilLimiterKeepsToolboxesIndependent 未注入共享 limiter 时每次装配独立，
// 保持 fishmcp 单箱与既有调用方的行为。
func TestSideEffectNilLimiterKeepsToolboxesIndependent(t *testing.T) {
	d1, _, s1, _ := sideDeps()
	box1, _ := BuildSideEffectTools(d1)
	d2, _, s2, _ := sideDeps()
	box2, _ := BuildSideEffectTools(d2)

	if _, err := box1.Run(context.Background(), ToolFollow, `{"mid":"123","action":"follow"}`); err != nil {
		t.Fatalf("第一箱应放行：%v", err)
	}
	if _, err := box2.Run(context.Background(), ToolFollow, `{"mid":"456","action":"follow"}`); err != nil {
		t.Fatalf("未注入共享 limiter 时两箱应互不影响：%v", err)
	}
	if s1.follows != 1 || s2.follows != 1 {
		t.Errorf("两箱应各执行一次：s1=%d s2=%d", s1.follows, s2.follows)
	}
}

// TestSideEffectNotConfiguredExecutors 执行器缺失时明确说「未配置」，不假装成功。
func TestSideEffectNotConfiguredExecutors(t *testing.T) {
	d, _, _, _ := sideDeps()
	d.Gift = nil
	d.Social = nil
	box, _ := BuildSideEffectTools(d)

	for _, tc := range []struct{ name, args string }{
		{ToolGift, `{"gift_id":"31036","num":1}`},
		{ToolFollow, `{"mid":"123","action":"follow"}`},
		{ToolLike, `{"count":1}`},
		{ToolDM, `{"mid":"777","text":"你好"}`},
	} {
		got, err := box.Run(context.Background(), tc.name, tc.args)
		if err != nil {
			t.Errorf("%s 未配置时不应报错：%v", tc.name, err)
			continue
		}
		if !strings.Contains(got, "未配置") {
			t.Errorf("%s 应说明未配置，实际 %q", tc.name, got)
		}
	}
}
