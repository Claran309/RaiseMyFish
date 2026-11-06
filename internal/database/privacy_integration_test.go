//go:build integration

package database

// T02：同一 UID 的公开弹幕、私密 DM（A/B）、QQ 私聊与跨平台公开数据，
// 在公开投影、公开可检索向量与工具里只能看到公开来源；私密召回只在原会话生效；
// 历史错误降密的 DM 即使 private=false 也不能进入任何公开视角。
//
// 本文件还覆盖 M01 的派生数据修复：旧污染档案、陈旧档案及其向量在重建时被清掉。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
	"gorm.io/gorm"
)

const (
	privacyUID  = "123456"
	privacyRoom = "30308220"
	privacyDM   = "dm-a"
	privacyDM2  = "dm-b"
	privacyQQ   = "qq-private-1"
	privacyVid  = "video-1"
)

// insertLegacyEvent 直接写一条不符合当前校验的旧数据（错误降密的 DM、私密直播行）。
func insertLegacyEvent(t *testing.T, db *gorm.DB, ctx context.Context, id string, private bool, text string, at time.Time) {
	t.Helper()
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO events (id, channel, dir, session, from_id, from_name, text, private, at, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, channelOf(id), "in", sessionOf(id), privacyUID, "脏数据昵称", text, private, at, "pending",
	).Error; err != nil {
		t.Fatalf("插入旧事件 %s 失败：%v", id, err)
	}
}

func channelOf(id string) string {
	parts := strings.SplitN(id, ":", 3)
	return parts[0]
}

func sessionOf(id string) string {
	parts := strings.SplitN(id, ":", 3)
	return parts[1]
}

func insertEvent(t *testing.T, repo *EventRepo, ev event.Event) {
	t.Helper()
	if _, err := repo.Insert(context.Background(), ev); err != nil {
		t.Fatalf("插入事件 %s 失败：%v", ev.ID, err)
	}
}

func containsText(rows []event.Event, text string) bool {
	for _, row := range rows {
		if row.Text == text {
			return true
		}
	}
	return false
}

func containsWebText(rows []store.WebEvent, text string) bool {
	for _, row := range rows {
		if row.Text == text {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.Contains(value, target) {
			return true
		}
	}
	return false
}

// TestPublicProjectionExcludesPrivateSources 是 T02 的主回归。
func TestPublicProjectionExcludesPrivateSources(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	viewers, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	memories, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	web, err := NewWebRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	// 公开：本房间弹幕 2 条 + 跨平台公开评论 2 条。
	insertEvent(t, events, event.Event{ID: "live:" + privacyRoom + ":pub1", Channel: event.ChLive, Dir: event.DirIn, Session: privacyRoom, FromID: privacyUID, FromName: "公开昵称", Text: "公开弹幕一", At: base, Status: event.StatusPending})
	insertEvent(t, events, event.Event{ID: "live:" + privacyRoom + ":pub2", Channel: event.ChLive, Dir: event.DirIn, Session: privacyRoom, FromID: privacyUID, FromName: "公开昵称", Text: "公开弹幕二", At: base.Add(time.Second), Status: event.StatusPending})
	insertEvent(t, events, event.Event{ID: "comment:" + privacyVid + ":pub1", Channel: event.ChComment, Dir: event.DirIn, Session: privacyVid, FromID: privacyUID, FromName: "公开昵称", Text: "跨平台公开评论一", At: base.Add(5 * time.Second), Status: event.StatusPending})
	insertEvent(t, events, event.Event{ID: "comment:" + privacyVid + ":pub2", Channel: event.ChComment, Dir: event.DirIn, Session: privacyVid, FromID: privacyUID, FromName: "公开昵称", Text: "跨平台公开评论二", At: base.Add(6 * time.Second), Status: event.StatusPending})
	// 私密：两个 DM 会话 + QQ 私聊。
	insertEvent(t, events, event.Event{ID: "dm:" + privacyDM + ":in1", Channel: event.ChDM, Dir: event.DirIn, Session: privacyDM, FromID: privacyUID, FromName: "私密昵称", Text: "DM-A私密一", Private: true, At: base.Add(2 * time.Second), Status: event.StatusPending})
	insertEvent(t, events, event.Event{ID: "dm:" + privacyDM2 + ":in1", Channel: event.ChDM, Dir: event.DirIn, Session: privacyDM2, FromID: privacyUID, FromName: "私密昵称", Text: "DM-B私密一", Private: true, At: base.Add(3 * time.Second), Status: event.StatusPending})
	insertEvent(t, events, event.Event{ID: "qq:" + privacyQQ + ":in1", Channel: event.ChQQ, Dir: event.DirIn, Session: privacyQQ, FromID: privacyUID, FromName: "私密昵称", Text: "QQ私聊一", Private: true, At: base.Add(4 * time.Second), Status: event.StatusPending})
	// 公开出向 1 条（计入档案 replies），私密出向 1 条（不能计入）。
	insertEvent(t, events, event.Event{ID: "live:out:" + privacyRoom + ":r1", Channel: event.ChLive, Dir: event.DirOut, Session: privacyRoom, FromID: "9", Text: "公开回复", ReplyTo: "live:" + privacyRoom + ":pub1", At: base.Add(7 * time.Second), Status: event.StatusDone})
	insertEvent(t, events, event.Event{ID: "dm:out:" + privacyDM + ":r1", Channel: event.ChDM, Dir: event.DirOut, Session: privacyDM, FromID: "9", Text: "私密回复", ReplyTo: "dm:" + privacyDM + ":in1", Private: true, At: base.Add(8 * time.Second), Status: event.StatusDone})
	// 历史脏数据：DM 错误降密成 public；直播行错误标成 private。
	insertLegacyEvent(t, db, ctx, "dm:"+privacyDM+":leak", false, "错误降密的DM", base.Add(9*time.Second))
	insertLegacyEvent(t, db, ctx, "live:"+privacyRoom+":leakpriv", true, "私密直播脏数据", base.Add(10*time.Second))
	// DM-only 观众：3 条私密后应当没有公开档案（放在独立会话，避免干扰 dm-a 范围断言）。
	for i := 0; i < 3; i++ {
		insertEvent(t, events, event.Event{
			ID: "dm:dm-c:other" + string(rune('a'+i)), Channel: event.ChDM, Dir: event.DirIn,
			Session: "dm-c", FromID: "999", FromName: "只有私信的人", Text: "私密批量",
			Private: true, At: base.Add(time.Duration(11+i) * time.Second), Status: event.StatusPending,
		})
	}

	// --- 工具：ViewerRecent 只读公开投影 ---
	viewerRows, err := events.ViewerRecent(ctx, privacyUID, 20)
	if err != nil {
		t.Fatalf("ViewerRecent 失败：%v", err)
	}
	if len(viewerRows) != 4 {
		t.Fatalf("ViewerRecent 应只返回 4 条公开入向，实际 %d 条：%+v", len(viewerRows), viewerRows)
	}
	for _, row := range viewerRows {
		if row.Private || row.Channel == event.ChDM {
			t.Fatalf("ViewerRecent 返回了私密来源：%+v", row)
		}
	}
	for _, leak := range []string{"DM-A私密一", "DM-B私密一", "QQ私聊一", "错误降密的DM", "私密直播脏数据"} {
		if containsText(viewerRows, leak) {
			t.Fatalf("ViewerRecent 泄漏 %q：%+v", leak, viewerRows)
		}
	}

	// --- history 工具：公开范围只读公开，私密范围只读本会话 ---
	liveRows, err := events.RecentEvents(ctx, memory.Scope{Channel: event.ChLive, Session: privacyRoom}, 20)
	if err != nil {
		t.Fatalf("RecentEvents(live) 失败：%v", err)
	}
	if len(liveRows) != 2 || containsText(liveRows, "私密直播脏数据") {
		t.Fatalf("公开 history 泄漏私密直播行：%+v", liveRows)
	}
	dmRows, err := events.RecentEvents(ctx, memory.Scope{Channel: event.ChDM, Session: privacyDM, Private: true}, 20)
	if err != nil {
		t.Fatalf("RecentEvents(dm-a) 失败：%v", err)
	}
	if len(dmRows) != 1 || dmRows[0].Text != "DM-A私密一" {
		t.Fatalf("私密 history 应只返回 DM-A 本会话合法私信：%+v", dmRows)
	}
	dm2Rows, err := events.RecentEvents(ctx, memory.Scope{Channel: event.ChDM, Session: privacyDM2, Private: true}, 20)
	if err != nil {
		t.Fatalf("RecentEvents(dm-b) 失败：%v", err)
	}
	if len(dm2Rows) != 1 || dm2Rows[0].Text != "DM-B私密一" {
		t.Fatalf("DM-B 会话召回越界：%+v", dm2Rows)
	}
	if _, err := events.RecentEvents(ctx, memory.Scope{Channel: event.ChLive, Session: privacyRoom, Private: true}, 10); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("公开通道声明私密范围应 ErrInvalid：%v", err)
	}
	replies, err := events.RecentReplies(ctx, event.ChLive, privacyRoom, 10)
	if err != nil || len(replies) != 1 || replies[0].Text != "公开回复" {
		t.Fatalf("RecentReplies 应只返回公开已确认回复：%+v / %v", replies, err)
	}

	// --- 公开窗口（梗白学习）也不含私密 ---
	since, err := events.PublicSince(ctx, event.ChLive, base.Add(-time.Hour), 100)
	if err != nil {
		t.Fatalf("PublicSince 失败：%v", err)
	}
	for _, leak := range []string{"私密直播脏数据", "错误降密的DM"} {
		if containsText(since, leak) {
			t.Fatalf("PublicSince 泄漏 %q：%+v", leak, since)
		}
	}

	// --- 档案重建：只聚合公开来源，并清理旧污染 ---
	seed := []store.ViewerProfile{
		{UID: privacyUID, Name: "旧污染名", Hits: 99, Replies: 9, LastSeen: base,
			RecentTexts: []string{"DM-A私密一", "错误降密的DM"}, RecentReplies: []string{"私密回复"}},
		{UID: "999", Name: "只有私信的人", Hits: 3, Replies: 0, LastSeen: base, RecentTexts: []string{"私密批量"}},
		{UID: "888", Name: "已经消失的人", Hits: 2, Replies: 0, LastSeen: base, RecentTexts: []string{"旧数据"}},
	}
	for _, profile := range seed {
		if err := viewers.UpsertProfile(ctx, profile); err != nil {
			t.Fatalf("写入旧档案 %s 失败：%v", profile.UID, err)
		}
	}
	vector := viewerVector(0)
	literal, err := vectorLiteral(vector, viewer.EmbeddingDimension)
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{privacyUID, "999", "888"} {
		// M10：用 facts 路演示「内容变化 → 旧向量失效」（重建比对的是 recent_texts）。
		if err := db.WithContext(ctx).Exec(`
			UPDATE viewer_profiles
			SET facts_embedding=?::vector, facts_embedding_hash='stale-hash', facts_embedding_updated_at=now()
			WHERE uid=?`, literal, uid).Error; err != nil {
			t.Fatalf("写入旧向量 %s 失败：%v", uid, err)
		}
	}

	stats, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10})
	if err != nil {
		t.Fatalf("重建档案失败：%v", err)
	}
	if stats.Profiles != 1 {
		t.Fatalf("重建后应只剩 1 个公开档案，实际 %d", stats.Profiles)
	}
	profile, err := viewers.GetProfile(ctx, privacyUID)
	if err != nil {
		t.Fatalf("读取公开档案失败：%v", err)
	}
	if profile.Hits != 4 || profile.Replies != 1 || profile.Name != "公开昵称" {
		t.Fatalf("档案计数/昵称不符：%+v", profile)
	}
	if len(profile.RecentTexts) != 4 || containsString(profile.RecentTexts, "私密") || containsString(profile.RecentTexts, "错误降密") {
		t.Fatalf("档案快照混入私密来源：%+v", profile.RecentTexts)
	}
	if len(profile.RecentReplies) != 1 || profile.RecentReplies[0] != "公开回复" {
		t.Fatalf("档案回复快照混入私密来源：%+v", profile.RecentReplies)
	}
	if _, err := viewers.GetProfile(ctx, "999"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DM-only 档案应被删除：%v", err)
	}
	if _, err := viewers.GetProfile(ctx, "888"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("陈旧档案应被删除：%v", err)
	}
	var embeddingCleared struct {
		EmbeddingNull bool
		HashEmpty     bool
	}
	if err := db.WithContext(ctx).Raw(`
		SELECT facts_embedding IS NULL AS embedding_null,
		       COALESCE(facts_embedding_hash,'') = '' AS hash_empty
		FROM viewer_profiles WHERE uid = ?`, privacyUID).Scan(&embeddingCleared).Error; err != nil {
		t.Fatal(err)
	}
	if !embeddingCleared.EmbeddingNull || !embeddingCleared.HashEmpty {
		t.Fatalf("档案内容变化后旧向量未失效：%+v", embeddingCleared)
	}
	for _, kind := range viewer.AllEmbeddingKinds() {
		keywords, err := viewers.KeywordProfiles(ctx, kind, "私密", 10)
		if err != nil || len(keywords) != 0 {
			t.Fatalf("%s 关键词检索不应命中私密来源：%+v / %v", kind, keywords, err)
		}
		semantic, err := viewers.SemanticProfiles(ctx, kind, vector, 10)
		if err != nil || len(semantic) != 0 {
			t.Fatalf("%s 失效向量不应再参与召回：%+v / %v", kind, semantic, err)
		}
	}
	inputs, err := viewers.ViewerEmbeddingInputs(ctx)
	if err != nil {
		t.Fatalf("读取向量化源失败：%v", err)
	}
	if len(inputs) != 1 || containsString(inputs[0].Texts, "私密") {
		t.Fatalf("向量化源混入私密来源：%+v", inputs)
	}

	// --- 记忆：公开事实、私密 DM 事实、错误降密事实 ---
	publicRecord := store.MemoryRecord{
		Record: memory.Record{ID: "fact:public", Channel: event.ChLive, Session: privacyRoom,
			Summary: "喜欢公开聊天", Importance: 0.9, Confidence: 0.9,
			EventIDs: []string{"live:" + privacyRoom + ":pub1"}, ValidFrom: base},
		Source: "persona", Kind: "fact", Title: "公开事实", FromID: privacyUID, FromName: "公开昵称",
	}
	dmRecord := store.MemoryRecord{
		Record: memory.Record{ID: "fact:dm-a", Channel: event.ChDM, Session: privacyDM,
			Summary: "DM-A说过的秘密", Importance: 0.9, Confidence: 0.9, Private: true,
			EventIDs: []string{"dm:" + privacyDM + ":in1"}, ValidFrom: base},
		Source: "persona", Kind: "fact", Title: "DM-A事实", FromID: privacyUID, FromName: "私密昵称",
	}
	dm2Record := store.MemoryRecord{
		Record: memory.Record{ID: "fact:dm-b", Channel: event.ChDM, Session: privacyDM2,
			Summary: "DM-B说过的秘密", Importance: 0.9, Confidence: 0.9, Private: true,
			EventIDs: []string{"dm:" + privacyDM2 + ":in1"}, ValidFrom: base},
		Source: "persona", Kind: "fact", Title: "DM-B事实", FromID: privacyUID, FromName: "私密昵称",
	}
	for _, record := range []store.MemoryRecord{publicRecord, dmRecord, dm2Record} {
		if _, err := memories.InsertRecord(ctx, record); err != nil {
			t.Fatalf("写入记忆 %s 失败：%v", record.ID, err)
		}
	}
	if err := db.WithContext(ctx).Exec(`
		INSERT INTO memory_records
			(id, source, kind, title, summary, body, importance, confidence, private, channel, session,
			 valid_from, event_ids, from_id, from_name)
		VALUES ('fact:leak', 'persona', 'fact', '错误降密事实', '错误降密的DM记忆', '', 0.9, 0.9,
		        false, 'dm', ?, ?, ARRAY[?], ?, '降密昵称')`,
		privacyDM, base, "dm:"+privacyDM+":leak", privacyUID).Error; err != nil {
		t.Fatalf("写入错误降密记忆失败：%v", err)
	}

	memVector := make([]float64, memory.EmbeddingDimension)
	memVector[0] = 1
	memLiteral, err := vectorLiteral(memVector, memory.EmbeddingDimension)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"fact:public", "fact:dm-a", "fact:dm-b", "fact:leak"} {
		if err := db.WithContext(ctx).Exec(
			`UPDATE memory_records SET embedding=?::vector, embedding_text_hash='src', embedding_updated_at=now() WHERE id=?`,
			memLiteral, id).Error; err != nil {
			t.Fatalf("写入记忆向量 %s 失败：%v", id, err)
		}
	}

	facts, err := memories.FactsByViewer(ctx, privacyUID, 10)
	if err != nil || len(facts) != 1 || facts[0].ID != "fact:public" {
		t.Fatalf("认人事实泄漏私密来源：%+v / %v", facts, err)
	}
	soul, err := memories.SoulCandidates(ctx, 0.5, 10)
	if err != nil {
		t.Fatalf("读取灵魂候选失败：%v", err)
	}
	for _, row := range soul {
		if row.Private || row.ID != "fact:public" {
			t.Fatalf("灵魂候选泄漏私密来源：%+v", row)
		}
	}
	publicScope := memory.Scope{Channel: event.ChLive, Session: privacyRoom}
	records, err := memories.Records(ctx, publicScope, 10)
	if err != nil || len(records) != 1 || records[0].ID != "fact:public" {
		t.Fatalf("公开记忆摘要泄漏私密来源：%+v / %v", records, err)
	}
	semanticPublic, err := memories.SemanticRecords(ctx, publicScope, memVector, 10)
	if err != nil {
		t.Fatalf("公开语义检索失败：%v", err)
	}
	if len(semanticPublic) != 1 || semanticPublic[0].Record.ID != "fact:public" {
		t.Fatalf("公开向量检索泄漏私密来源：%+v", semanticPublic)
	}
	dmScope := memory.Scope{Channel: event.ChDM, Session: privacyDM, Private: true}
	semanticDM, err := memories.SemanticRecords(ctx, dmScope, memVector, 10)
	if err != nil {
		t.Fatalf("私密语义检索失败：%v", err)
	}
	hasDM, hasDM2, hasLeak := false, false, false
	for _, row := range semanticDM {
		switch row.Record.ID {
		case "fact:dm-a":
			hasDM = true
		case "fact:dm-b":
			hasDM2 = true
		case "fact:leak":
			hasLeak = true
		}
	}
	if !hasDM || hasDM2 || hasLeak {
		t.Fatalf("私密索引应只在原会话召回：dm-a=%v dm-b=%v leak=%v", hasDM, hasDM2, hasLeak)
	}

	// --- 面板导出：公开视角 ---
	panelMemory, err := web.Memory(ctx, "", "", 50)
	if err != nil || len(panelMemory) != 1 || panelMemory[0].ID != "fact:public" {
		t.Fatalf("面板记忆导出泄漏私密来源：%+v / %v", panelMemory, err)
	}
	panelEvents, err := web.Events(ctx, "", 100)
	if err != nil {
		t.Fatalf("面板事件导出失败：%v", err)
	}
	for _, row := range panelEvents {
		if row.Channel == "dm" || strings.Contains(row.Text, "私密") || strings.Contains(row.Text, "错误降密") {
			t.Fatalf("面板事件导出泄漏私密来源：%+v", row)
		}
	}
	livePanel, err := web.EventsByChannel(ctx, "live", "", 100)
	if err != nil {
		t.Fatalf("面板直播通道查询失败：%v", err)
	}
	if containsWebText(livePanel, "私密直播脏数据") {
		t.Fatalf("面板直播视图泄漏私密行：%+v", livePanel)
	}
	dmPanel, err := web.EventsByChannel(ctx, "dm", "", 100)
	if err != nil {
		t.Fatalf("面板私信视图查询失败：%v", err)
	}
	hasDMPanel := false
	for _, row := range dmPanel {
		if row.ID == "dm:"+privacyDM+":in1" {
			hasDMPanel = true
		}
		if row.Channel != "dm" {
			t.Fatalf("私信视图混入其他通道：%+v", row)
		}
	}
	if !hasDMPanel {
		t.Fatalf("私信视图应保留原私密 DM：%+v", dmPanel)
	}
	if detail, err := web.Event(ctx, "live:"+privacyRoom+":pub1"); err != nil || detail.Text != "公开弹幕一" {
		t.Fatalf("公开事件详情读取失败：%+v / %v", detail, err)
	}
	for _, id := range []string{"dm:" + privacyDM + ":in1", "dm:" + privacyDM + ":leak", "live:" + privacyRoom + ":leakpriv"} {
		if _, err := web.Event(ctx, id); !errors.Is(err, store.ErrWebNotFound) {
			t.Fatalf("公开详情接口不应读取 %s：%v", id, err)
		}
	}

	// --- 面板异常列表：失败状态也不能导出私密正文 ---
	errEvents := []struct {
		id      string
		private bool
		text    string
		at      time.Time
	}{
		{"live:" + privacyRoom + ":errpub", false, "公开失败", base.Add(20 * time.Second)},
		{"live:" + privacyRoom + ":errpriv", true, "私密失败", base.Add(21 * time.Second)},
		{"dm:" + privacyDM + ":errdm", false, "DM失败", base.Add(22 * time.Second)},
	}
	for _, ev := range errEvents {
		if err := db.WithContext(ctx).Exec(
			`INSERT INTO events (id, channel, dir, session, from_id, from_name, text, private, at, status)
			 VALUES (?, ?, 'in', ?, ?, '异常昵称', ?, ?, ?, 'dead')`,
			ev.id, channelOf(ev.id), sessionOf(ev.id), privacyUID, ev.text, ev.private, ev.at,
		).Error; err != nil {
			t.Fatalf("插入异常事件 %s 失败：%v", ev.id, err)
		}
	}
	errorsRows, err := web.Errors(ctx, 50)
	if err != nil {
		t.Fatalf("读取面板异常失败：%v", err)
	}
	for _, row := range errorsRows {
		if row.ID != "live:"+privacyRoom+":errpub" {
			t.Fatalf("异常列表泄漏私密来源：%+v", errorsRows)
		}
	}
	if len(errorsRows) != 1 {
		t.Fatalf("异常列表应只剩 1 条公开事件：%+v", errorsRows)
	}
}

// TestActiveSinceIgnoresPrivateOnly 只有私密事件时不算直播活跃，避免无公开互动也触发重建。
func TestActiveSinceIgnoresPrivateOnly(t *testing.T) {
	db, ctx := repositoryDB(t)
	viewers, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	insertLegacyEvent(t, db, ctx, "dm:"+privacyDM+":only", true, "只有私信", base)
	insertLegacyEvent(t, db, ctx, "live:"+privacyRoom+":onlypriv", true, "私密直播", base.Add(time.Second))

	active, err := viewers.ActiveSince(ctx, base.Add(-time.Minute))
	if err != nil {
		t.Fatalf("ActiveSince 失败：%v", err)
	}
	if active {
		t.Fatal("只有私密事件时不应判定公开活跃")
	}
	insertLegacyEvent(t, db, ctx, "live:"+privacyRoom+":public", false, "公开弹幕", base.Add(2*time.Second))
	active, err = viewers.ActiveSince(ctx, base.Add(-time.Minute))
	if err != nil || !active {
		t.Fatalf("有公开事件时应判定活跃：%v / %v", active, err)
	}
}
