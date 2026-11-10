package main

// 本文件测 Viewer 段的完整装配：
//   - 甲问乙：按 UID 直接取档案、按昵称找回、歧义/陌生人/自身都不注入；
//   - 房间名单：仅直播通道、排除当前发言人与被问到的乙、最多 5 条；
//   - 预算：当前观众档案 > 历史记忆 > 时间窗 > 乙档案 > 名单，总长 ≤400。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
	"go.uber.org/zap"
)

// injectProfiles 是「当前观众 + 房间里另外三个人」的档案表。
func injectProfiles() map[string]store.ViewerProfile {
	return map[string]store.ViewerProfile{
		"42":   viewerMemoryProfile(),
		"7001": injectViewerProfile("7001", "贞德", 7, "常在夜里出没"),
		"7002": injectViewerProfile("7002", "阿明", 4, "周末才来"),
		"7003": injectViewerProfile("7003", "今夜琴声温柔", 9, "开播就来"),
	}
}

// injectFocus 造一组房间里刚才说过话的观众（甲问乙的可见证据）。
func injectFocus(now time.Time) []memory.Message {
	return []memory.Message{
		{EventID: "live:focus:1", Channel: event.ChLive, Session: "30308220", Dir: event.DirIn, FromID: "7001", FromName: "贞德", Text: "晚上好", At: now.Add(-3 * time.Minute)},
		{EventID: "live:focus:2", Channel: event.ChLive, Session: "30308220", Dir: event.DirIn, FromID: "7003", FromName: "今夜琴声温柔", Text: "来了", At: now.Add(-2 * time.Minute)},
	}
}

func foundViewerResult(p store.ViewerProfile) viewer.Result {
	return viewer.Result{
		Status:     viewer.StatusFound,
		Candidates: []viewer.Candidate{{ViewerProfileHit: store.ViewerProfileHit{Profile: p}}},
	}
}

// TestViewerTargetProfileInjection 验收「甲问乙」：
// 乙在可见证据里时预取乙的档案；无证据、歧义、失配、本人都不注入。
func TestViewerTargetProfileInjection(t *testing.T) {
	now := viewerMemoryNow()
	profiles := injectProfiles()

	t.Run("按UID直接取乙档案", func(t *testing.T) {
		viewers := &fakeViewerRepo{profiles: profiles}
		builder := newContextBuilder(snapshotTestPack(), nil, nil, viewers, nil, nil, zap.NewNop())
		ev := viewerMemoryEvent(now)
		ev.Text = "贞德是谁"

		got := builder.viewerText(context.Background(), brain.ContextInput{
			Event: ev, Memory: memory.Context{Focus: injectFocus(now)},
		})

		for _, want := range []string{"【被问到的观众 · 贞德】", "说过 7 次", "常在夜里出没"} {
			if !strings.Contains(got, want) {
				t.Errorf("乙档案缺少 %q：\n%s", want, got)
			}
		}
		if viewers.gotUID != "7001" {
			t.Errorf("乙档案应按 UID 取：gotUID=%q", viewers.gotUID)
		}
		if n := utf8.RuneCountInString(formatTargetProfile("贞德", profiles["7001"])); n > 200 {
			t.Errorf("乙档案 %d 字，超过上限 200", n)
		}
	})

	t.Run("回复目标按昵称找回", func(t *testing.T) {
		viewers := &fakeViewerRepo{profiles: profiles}
		finder := &fakeProfileFinder{results: map[string]viewer.Result{"阿明": foundViewerResult(profiles["7002"])}}
		builder := newContextBuilder(snapshotTestPack(), nil, nil, viewers, nil, nil, zap.NewNop())
		builder.finder = finder
		ev := viewerMemoryEvent(now)
		ev.Text = "记得阿明吗"
		ev.Meta = []byte(`{"reply_to_name":"阿明"}`)

		got := builder.viewerText(context.Background(), brain.ContextInput{Event: ev})

		if !strings.Contains(got, "【被问到的观众 · 阿明】") || !strings.Contains(got, "周末才来") {
			t.Errorf("按昵称找回的乙档案未注入：\n%s", got)
		}
		if finder.calls != 1 || finder.gotQuery != "阿明" {
			t.Errorf("检索调用 = %d，query=%q", finder.calls, finder.gotQuery)
		}
	})

	t.Run("歧义候选不注入", func(t *testing.T) {
		viewers := &fakeViewerRepo{profiles: profiles}
		finder := &fakeProfileFinder{results: map[string]viewer.Result{
			"阿明": {
				Status: viewer.StatusAmbiguous,
				Candidates: []viewer.Candidate{
					{ViewerProfileHit: store.ViewerProfileHit{Profile: profiles["7002"]}},
					{ViewerProfileHit: store.ViewerProfileHit{Profile: profiles["7003"]}},
				},
			},
		}}
		builder := newContextBuilder(snapshotTestPack(), nil, nil, viewers, nil, nil, zap.NewNop())
		builder.finder = finder
		ev := viewerMemoryEvent(now)
		ev.Text = "记得阿明吗"
		ev.Meta = []byte(`{"reply_to_name":"阿明"}`)

		got := builder.viewerText(context.Background(), brain.ContextInput{Event: ev})

		if strings.Contains(got, "【被问到的观众") {
			t.Errorf("歧义时不能替用户认人：\n%s", got)
		}
		if finder.calls != 1 {
			t.Errorf("歧义也应只查一次：calls=%d", finder.calls)
		}
	})

	t.Run("主体是当前发言人不重复", func(t *testing.T) {
		viewers := &fakeViewerRepo{profiles: profiles}
		builder := newContextBuilder(snapshotTestPack(), nil, nil, viewers, nil, nil, zap.NewNop())
		ev := viewerMemoryEvent(now)
		ev.Text = "贞德是谁"
		ev.FromID = "7001"
		ev.FromName = "贞德"

		got := builder.viewerText(context.Background(), brain.ContextInput{
			Event: ev, Memory: memory.Context{Focus: injectFocus(now)},
		})

		if strings.Contains(got, "【被问到的观众") {
			t.Errorf("主体就是发言人时不应重复注乙档案：\n%s", got)
		}
		if viewers.calls != 1 {
			t.Errorf("只应读一次当前发言人档案：calls=%d", viewers.calls)
		}
	})

	t.Run("无可见证据不预取", func(t *testing.T) {
		viewers := &fakeViewerRepo{profiles: profiles}
		finder := &fakeProfileFinder{results: map[string]viewer.Result{"鲁迅": foundViewerResult(profiles["7002"])}}
		builder := newContextBuilder(snapshotTestPack(), nil, nil, viewers, nil, nil, zap.NewNop())
		builder.finder = finder
		ev := viewerMemoryEvent(now)
		ev.Text = "记得鲁迅吗"

		got := builder.viewerText(context.Background(), brain.ContextInput{Event: ev})

		if strings.Contains(got, "【被问到的观众") {
			t.Errorf("unresolved 主体不应预取观众档案：\n%s", got)
		}
		if finder.calls != 0 {
			t.Errorf("unresolved 主体不应查观众检索：calls=%d", finder.calls)
		}
	})

	t.Run("乙档案不存在不注入", func(t *testing.T) {
		viewers := &fakeViewerRepo{profiles: profiles}
		finder := &fakeProfileFinder{results: map[string]viewer.Result{"陌生人": {Status: viewer.StatusNotFound}}}
		builder := newContextBuilder(snapshotTestPack(), nil, nil, viewers, nil, nil, zap.NewNop())
		builder.finder = finder
		ev := viewerMemoryEvent(now)
		ev.Text = "记得陌生人吗"
		ev.Meta = []byte(`{"reply_to_name":"陌生人"}`)

		got := builder.viewerText(context.Background(), brain.ContextInput{Event: ev})

		if strings.Contains(got, "【被问到的观众") {
			t.Errorf("查不到乙时不应注入档案：\n%s", got)
		}
	})
}

// TestViewerRosterInjection 名单只用于直播房间，且不重复当前发言人与乙。
func TestViewerRosterInjection(t *testing.T) {
	now := viewerMemoryNow()
	profiles := injectProfiles()
	top := []store.ViewerProfile{
		profiles["42"], profiles["7001"], profiles["7002"], profiles["7003"],
		{UID: "8008", Name: "路人甲", Hits: 3, LastSeen: now},
		{UID: "9009", Name: "第六人", Hits: 2, LastSeen: now},
		{UID: "9010", Name: "第七人", Hits: 1, LastSeen: now},
	}

	t.Run("直播房间名单", func(t *testing.T) {
		viewers := &fakeViewerRepo{profiles: profiles, top: top}
		builder := newContextBuilder(snapshotTestPack(), nil, nil, viewers, nil, nil, zap.NewNop())

		got := builder.viewerText(context.Background(), brain.ContextInput{Event: viewerMemoryEvent(now)})

		if !strings.Contains(got, "## 房间最近活跃（按发言次数）") {
			t.Fatalf("直播房间缺少活跃名单：\n%s", got)
		}
		if strings.Contains(got, "- 娇娇（说过") {
			t.Errorf("当前发言人不应重复进名单：\n%s", got)
		}
		if !strings.Contains(got, "- 路人甲（说过 3 次）") {
			t.Errorf("名单应按 TopViewers 顺序带出：\n%s", got)
		}
		if strings.Contains(got, "第七人") {
			t.Errorf("名单超过 %d 条：\n%s", viewerRosterMax, got)
		}
		if n := strings.Count(got, "（说过"); n != viewerRosterMax {
			t.Errorf("名单 %d 条，期望 %d（当前发言人被排除）：\n%s", n, viewerRosterMax, got)
		}
		if viewers.gotTopLimit != viewerRosterQuery {
			t.Errorf("名单读取上限 = %d，期望 %d", viewers.gotTopLimit, viewerRosterQuery)
		}
	})

	t.Run("非直播通道不注入名单", func(t *testing.T) {
		viewers := &fakeViewerRepo{profiles: profiles, top: top}
		builder := newContextBuilder(snapshotTestPack(), nil, nil, viewers, nil, nil, zap.NewNop())
		ev := viewerMemoryEvent(now)
		ev.Channel = event.ChDM
		ev.Session = "dm"
		ev.Private = true

		got := builder.viewerText(context.Background(), brain.ContextInput{Event: ev})

		if strings.Contains(got, "房间最近活跃") {
			t.Errorf("私信通道不应注入房间名单：\n%s", got)
		}
		if viewers.topCalls != 0 {
			t.Errorf("非直播通道不应查活跃名单：calls=%d", viewers.topCalls)
		}
	})

	t.Run("被问到的乙不重复进名单", func(t *testing.T) {
		viewers := &fakeViewerRepo{profiles: profiles, top: top}
		builder := newContextBuilder(snapshotTestPack(), nil, nil, viewers, nil, nil, zap.NewNop())
		ev := viewerMemoryEvent(now)
		ev.Text = "阿明是谁"

		got := builder.viewerText(context.Background(), brain.ContextInput{
			Event: ev, Memory: memory.Context{Focus: append(injectFocus(now), memory.Message{
				EventID: "live:focus:3", Channel: event.ChLive, Session: "30308220",
				Dir: event.DirIn, FromID: "7002", FromName: "阿明", Text: "在", At: now.Add(-time.Minute),
			})},
		})

		if !strings.Contains(got, "【被问到的观众 · 阿明】") {
			t.Fatalf("乙档案应注入：\n%s", got)
		}
		if strings.Contains(got, "- 阿明（说过") {
			t.Errorf("乙不应重复进名单：\n%s", got)
		}
	})
}

// TestViewerTextBudgetPriority 预算按「档案 > 相关记忆 > 时间窗 > 乙 > 名单」裁剪。
func TestViewerTextBudgetPriority(t *testing.T) {
	now := viewerMemoryNow()

	t.Run("记忆吃满预算时低优先级全裁", func(t *testing.T) {
		records := make([]memory.Record, 0, 12)
		for i := 0; i < 6; i++ {
			records = append(records, memory.Record{
				ID: fmt.Sprintf("related-%02d", i), Summary: strings.Repeat("很久以前的高分记忆", 8),
				Importance: 0.9, Confidence: 0.9,
				ValidFrom: now.Add(-time.Duration(30+i) * 24 * time.Hour),
			})
		}
		for i := 0; i < 6; i++ {
			records = append(records, memory.Record{
				ID: fmt.Sprintf("recent-%02d", i), Summary: strings.Repeat("最近几天聊过的事", 8),
				Importance: 0.2, Confidence: 0.9,
				ValidFrom: now.Add(-time.Duration(i+1) * time.Hour),
			})
		}
		// 厚档案先把 viewerProfileRunes 吃满，确保留给相关记忆的预算很窄。
		heavy := viewerMemoryProfile()
		heavy.ProfileSummary = strings.Repeat("爱吃白饭", 40)
		profiles := injectProfiles()
		profiles["42"] = heavy
		base, _ := snapshotTestMemory()
		mem := &fakeViewerRecordReader{fakeMemoryReader: base, records: records}
		viewers := &fakeViewerRepo{
			profiles: profiles,
			top:      []store.ViewerProfile{profiles["7001"], profiles["7002"]},
		}
		builder := newContextBuilder(snapshotTestPack(), mem, nil, viewers, nil, nil, zap.NewNop())
		ev := viewerMemoryEvent(now)
		ev.Text = "贞德是谁"

		got := builder.viewerText(context.Background(), brain.ContextInput{
			Event: ev, Memory: memory.Context{Focus: injectFocus(now)},
		})

		if n := utf8.RuneCountInString(got); n > viewerTextMaxRunes {
			t.Fatalf("Viewer 段 %d 字，超过预算 %d", n, viewerTextMaxRunes)
		}
		for _, want := range []string{"【对方档案 · 老熟人】", "## 跟他相关的记忆"} {
			if !strings.Contains(got, want) {
				t.Errorf("高优先级内容被误裁 %q：\n%s", want, got)
			}
		}
		for _, low := range []string{"## 最近 7 天聊过的", "【被问到的观众", "## 房间最近活跃"} {
			if strings.Contains(got, low) {
				t.Errorf("低优先级 %q 本应被裁掉：\n%s", low, got)
			}
		}
	})

	t.Run("乙档案整段放不下时按剩余预算截断", func(t *testing.T) {
		profile := strings.Repeat("档", viewerProfileRunes)
		raw := "【被问到的观众 · 乙】" + strings.Repeat("侧写", 100)
		got := composeViewerText(viewerTextMaxRunes, profile, []viewerSegment{{raw: raw}})

		if n := utf8.RuneCountInString(got); n > viewerTextMaxRunes {
			t.Fatalf("预算未生效：%d 字", n)
		}
		if !strings.Contains(got, "【被问到的观众 · 乙】") {
			t.Errorf("截断后仍应保留乙的身份抬头：\n%s", got)
		}
	})

	t.Run("剩余预算不足40字时整段丢弃", func(t *testing.T) {
		raw := strings.Repeat("乙", 100)
		if got := composeViewerText(30, "", []viewerSegment{{raw: raw}}); got != "" {
			t.Errorf("剩余预算不足时不应硬塞半段：%q", got)
		}
	})
}
