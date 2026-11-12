package memory

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
)

// repeatTestTime 是重复度用例的统一基准时间。
var repeatTestTime = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

// repeatEvent 构造一条公开直播弹幕；私密/DM 用例再自行改字段。
func repeatEvent(id string, at time.Time, text string) event.Event {
	return event.Event{
		ID: id, Channel: event.ChLive, Dir: event.DirIn, Session: "1",
		FromID: "2002", FromName: "熟客乙", Text: text, At: at,
	}
}

// TestCountRepeatMentionsPromotes 14 条「奇迹鱼」回放 → 恰有一条达标候选。
func TestCountRepeatMentionsPromotes(t *testing.T) {
	entities := RepeatEntities([]RepeatAlias{{Alias: "奇迹鱼", Name: "X奇迹与你X"}}, nil, nil)
	if len(entities) != 1 {
		t.Fatalf("实体数 = %d，期望 1：%+v", len(entities), entities)
	}

	events := make([]event.Event, 0, 14)
	for i := 0; i < 14; i++ {
		at := repeatTestTime.Add(time.Duration(i) * time.Minute)
		events = append(events, repeatEvent(fmt.Sprintf("live:1:%d", 1001+i), at, "奇迹鱼今天又来啦"))
	}
	since := repeatTestTime.Add(-RepeatWindowDays * 24 * time.Hour)
	got := CountRepeatMentions(entities, events, since, 5)
	if len(got) != 1 {
		t.Fatalf("候选数 = %d，期望 1：%+v", len(got), got)
	}
	c := got[0]
	if c.Count != 14 {
		t.Errorf("Count = %d，期望 14", c.Count)
	}
	if len(c.EventIDs) != repeatMaxRepresentative {
		t.Errorf("代表事件数 = %d，期望 %d：%v", len(c.EventIDs), repeatMaxRepresentative, c.EventIDs)
	}
	// 最新优先：14 条事件里取最后 5 条。
	for i, want := range []string{"live:1:1014", "live:1:1013", "live:1:1012", "live:1:1011", "live:1:1010"} {
		if c.EventIDs[i] != want {
			t.Errorf("EventIDs[%d] = %q，期望 %q", i, c.EventIDs[i], want)
		}
	}
	if !c.FirstAt.Equal(events[0].At) || !c.LastAt.Equal(events[13].At) {
		t.Errorf("时间窗口不符：first=%v last=%v", c.FirstAt, c.LastAt)
	}

	title := RepeatFactTitle(c.Entity.Alias, c.Entity.Target)
	if title != "称呼：奇迹鱼 = X奇迹与你X" {
		t.Errorf("Title = %q", title)
	}
	summary := RepeatFactSummary(c.Entity.Alias, c.Entity.Target, c.Count, RepeatWindowDays)
	if summary != "弹幕里「奇迹鱼」指的是 X奇迹与你X（7 天内提及 14 次）" {
		t.Errorf("Summary = %q", summary)
	}
}

// TestCountRepeatMentionsBelowThreshold 未达标不生成候选。
func TestCountRepeatMentionsBelowThreshold(t *testing.T) {
	entities := RepeatEntities([]RepeatAlias{{Alias: "奇迹鱼", Name: "X奇迹与你X"}}, nil, nil)
	events := make([]event.Event, 0, 4)
	for i := 0; i < 4; i++ {
		events = append(events, repeatEvent(fmt.Sprintf("live:1:20%02d", i), repeatTestTime.Add(time.Duration(i)*time.Minute), "奇迹鱼来了"))
	}
	if got := CountRepeatMentions(entities, events, repeatTestTime.Add(-time.Hour), 5); len(got) != 0 {
		t.Fatalf("4 次不应达标：%+v", got)
	}
}

// TestCountRepeatMentionsReplyTarget @ 目标按 reply_to_name 计数。
func TestCountRepeatMentionsReplyTarget(t *testing.T) {
	entities := RepeatEntities([]RepeatAlias{{Alias: "奇迹鱼", Name: "X奇迹与你X"}}, nil, nil)
	events := make([]event.Event, 0, 5)
	for i := 0; i < 5; i++ {
		ev := repeatEvent(fmt.Sprintf("live:1:30%02d", i), repeatTestTime.Add(time.Duration(i)*time.Minute), "晚上好")
		ev.Meta = json.RawMessage(`{"reply_to_name":"X奇迹与你X"}`)
		events = append(events, ev)
	}
	got := CountRepeatMentions(entities, events, repeatTestTime.Add(-time.Hour), 5)
	if len(got) != 1 || got[0].Count != 5 {
		t.Fatalf("@ 目标未计数：%+v", got)
	}
}

// TestCountRepeatMentionsASCIIStyle ASCII 称呼按词边界匹配，避免 B 命中 B站。
func TestCountRepeatMentionsASCIIStyle(t *testing.T) {
	entities := RepeatEntities(nil, []RepeatName{{UID: "7001", Name: "BB"}}, nil)
	events := []event.Event{
		repeatEvent("live:1:401", repeatTestTime, "B站今天好卡"),
		repeatEvent("live:1:402", repeatTestTime.Add(time.Minute), "bb来了"),
		repeatEvent("live:1:403", repeatTestTime.Add(2*time.Minute), "BB好"),
	}
	got := CountRepeatMentions(entities, events, repeatTestTime.Add(-time.Hour), 2)
	if len(got) != 1 || got[0].Count != 2 {
		t.Fatalf("ASCII 边界匹配不符：%+v", got)
	}
}

// TestRepeatEntitiesAliasAbsorbsTarget 别名目标不再被档案名重复建档。
func TestRepeatEntitiesAliasAbsorbsTarget(t *testing.T) {
	entities := RepeatEntities(
		[]RepeatAlias{{Alias: "奇迹鱼", Name: "X奇迹与你X"}},
		[]RepeatName{{UID: "7001", Name: "X奇迹与你X"}, {UID: "7001", Name: "奇迹鱼"}},
		[]RepeatName{{Name: "X奇迹与你X"}},
	)
	if len(entities) != 1 {
		t.Fatalf("别名目标应吸收档案名/@ 目标：%+v", entities)
	}
	if entities[0].Source != "alias" || entities[0].Target != "X奇迹与你X" {
		t.Errorf("实体不符：%+v", entities[0])
	}
}

// TestFilterRepeatEntities 过滤鱼自己的名字。
func TestFilterRepeatEntities(t *testing.T) {
	entities := RepeatEntities(nil, []RepeatName{{Name: "大肥鱼"}, {Name: "小海豹"}}, nil)
	got := FilterRepeatEntities(entities, []string{" 大肥鱼 "})
	if len(got) != 1 || got[0].Alias != "小海豹" {
		t.Fatalf("自名过滤不符：%+v", got)
	}
	if len(entities) != 2 {
		t.Fatalf("不应修改原切片：%+v", entities)
	}
}

// TestDedupeRepeatCandidates 同一目标多个别名只保留提及最多的一个。
func TestDedupeRepeatCandidates(t *testing.T) {
	entities := RepeatEntities([]RepeatAlias{
		{Alias: "仙主", Name: "终南仙主"},
		{Alias: "终南", Name: "终南仙主"},
	}, nil, nil)
	if len(entities) != 2 {
		t.Fatalf("别名实体数 = %d，期望 2", len(entities))
	}
	in := []RepeatCandidate{
		{Entity: entities[1], Count: 9}, // 终南
		{Entity: entities[0], Count: 6}, // 仙主
	}
	got := DedupeRepeatCandidates(in)
	if len(got) != 1 || got[0].Count != 9 {
		t.Fatalf("同实体去重不符：%+v", got)
	}
	if RepeatEntityKey(entities[0]) != RepeatEntityKey(entities[1]) {
		t.Errorf("同目标实体 key 应一致：%q / %q", RepeatEntityKey(entities[0]), RepeatEntityKey(entities[1]))
	}
}

// TestRepeatFactTitleNoDuplicate 档案名没有额外指向时不写「A = A」。
func TestRepeatFactTitleNoDuplicate(t *testing.T) {
	if title := RepeatFactTitle("大肥鱼", "大肥鱼"); title != "称呼：大肥鱼" {
		t.Errorf("Title = %q", title)
	}
}

// TestCountRepeatMentionsSkipsNonPublic 私密、DM、出向、空 ID 事件不参与统计。
func TestCountRepeatMentionsSkipsNonPublic(t *testing.T) {
	entities := RepeatEntities([]RepeatAlias{{Alias: "奇迹鱼", Name: "X奇迹与你X"}}, nil, nil)
	dm := repeatEvent("dm:1:1", repeatTestTime, "奇迹鱼")
	dm.Channel, dm.Session, dm.Private = event.ChDM, "dm-a", true
	private := repeatEvent("live:1:2", repeatTestTime, "奇迹鱼")
	private.Private = true
	out := repeatEvent("live:1:3", repeatTestTime, "奇迹鱼")
	out.Dir = event.DirOut
	noID := repeatEvent("", repeatTestTime, "奇迹鱼")

	events := []event.Event{dm, private, out, noID}
	if got := CountRepeatMentions(entities, events, repeatTestTime.Add(-time.Hour), 2); len(got) != 0 {
		t.Fatalf("非公开事件不应计数：%+v", got)
	}
}

// TestCountRepeatMentionsWindow 窗口外的事件不计数。
func TestCountRepeatMentionsWindow(t *testing.T) {
	entities := RepeatEntities([]RepeatAlias{{Alias: "奇迹鱼", Name: "X奇迹与你X"}}, nil, nil)
	old := repeatEvent("live:1:501", repeatTestTime.Add(-8*24*time.Hour), "奇迹鱼")
	fresh := repeatEvent("live:1:502", repeatTestTime, "奇迹鱼")
	since := repeatTestTime.Add(-RepeatWindowDays * 24 * time.Hour)
	got := CountRepeatMentions(entities, []event.Event{old, fresh}, since, 2)
	if len(got) != 0 {
		t.Fatalf("窗口外事件不应计数：%+v", got)
	}
}

// TestCorrectedRepeatAliases 主人明确纠正才降权；疑问句和路人不算。
func TestCorrectedRepeatAliases(t *testing.T) {
	entities := RepeatEntities([]RepeatAlias{{Alias: "奇迹鱼", Name: "X奇迹与你X"}}, nil, nil)
	key := RepeatEntityKey(entities[0])
	owner := "313591854"

	correction := repeatEvent("live:1:601", repeatTestTime, "记错了，奇迹鱼不是X奇迹与你X")
	correction.FromID = owner
	question := repeatEvent("live:1:602", repeatTestTime, "奇迹鱼不是X奇迹与你X吗？")
	question.FromID = owner
	other := repeatEvent("live:1:603", repeatTestTime, "记错了，奇迹鱼不是X奇迹与你X")
	other.FromID = "2002"

	got := CorrectedRepeatAliases([]event.Event{correction, question, other}, entities, owner)
	if len(got) != 1 || !got[key] {
		t.Fatalf("纠正识别不符：%+v", got)
	}

	if got := CorrectedRepeatAliases([]event.Event{question}, entities, owner); len(got) != 0 {
		t.Fatalf("疑问句不应降权：%+v", got)
	}
	if got := CorrectedRepeatAliases([]event.Event{correction}, entities, ""); len(got) != 0 {
		t.Fatalf("未配置主人 UID 不应降权：%+v", got)
	}
}

// TestRepeatEntityKey 实体按 UID/目标名识别；别名改称呼不影响实体身份。
func TestRepeatEntityKey(t *testing.T) {
	withUID := RepeatEntity{Alias: "奇迹鱼", Target: "X奇迹与你X", UID: "7001"}
	renamed := RepeatEntity{Alias: "奇鱼", Target: "X奇迹与你X", UID: "7001"}
	if RepeatEntityKey(withUID) != "uid:7001" || RepeatEntityKey(renamed) != RepeatEntityKey(withUID) {
		t.Errorf("UID key 不稳定：%q / %q", RepeatEntityKey(withUID), RepeatEntityKey(renamed))
	}
	byName := RepeatEntity{Alias: "奇迹鱼", Target: "X奇迹与你X"}
	if got := RepeatEntityKey(byName); got != "name:x奇迹与你x" {
		t.Errorf("名称 key = %q", got)
	}
	otherTarget := RepeatEntity{Alias: "奇迹鱼", Target: "别的鱼"}
	if RepeatEntityKey(otherTarget) == RepeatEntityKey(byName) {
		t.Errorf("同称呼不同指向实体 key 不应相同：%q", RepeatEntityKey(otherTarget))
	}
}

// TestRepeatFactKey 事实按称呼+指向识别：改称呼生成新事实，UID 不参与。
func TestRepeatFactKey(t *testing.T) {
	base := RepeatEntity{Alias: "奇迹鱼", Target: "X奇迹与你X", UID: "7001"}
	same := RepeatEntity{Alias: "奇迹鱼", Target: "X奇迹与你X"}
	if RepeatFactKey(base) != RepeatFactKey(same) {
		t.Errorf("UID 不应影响事实 key：%q / %q", RepeatFactKey(base), RepeatFactKey(same))
	}
	renamed := RepeatEntity{Alias: "奇鱼", Target: "X奇迹与你X", UID: "7001"}
	if RepeatFactKey(renamed) == RepeatFactKey(base) {
		t.Errorf("改称呼后事实 key 不应相同：%q", RepeatFactKey(renamed))
	}
	byName := RepeatEntity{Alias: "奇迹鱼", Target: "奇迹鱼"}
	if got := RepeatFactKey(byName); got != "name:奇迹鱼" {
		t.Errorf("名称事实 key = %q", got)
	}
}
