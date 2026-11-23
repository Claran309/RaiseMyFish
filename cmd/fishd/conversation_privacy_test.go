package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// recordingConversationRepo 记录面板/生成链路实际读了哪些会话。
type recordingConversationRepo struct {
	mu      sync.Mutex
	loads   []string
	touches []string
	states  map[string]store.ConversationState
}

func (r *recordingConversationRepo) LoadState(_ context.Context, id string, now time.Time) (store.ConversationState, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loads = append(r.loads, id)
	state, ok := r.states[id]
	if !ok || (!state.ExpiresAt.IsZero() && !state.ExpiresAt.After(now)) {
		return store.ConversationState{}, false, nil
	}
	return state, true, nil
}

func (r *recordingConversationRepo) Touch(_ context.Context, state store.ConversationState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.touches = append(r.touches, state.ConversationID)
	return nil
}

func (r *recordingConversationRepo) RecentTurns(_ context.Context, id string, _ int) ([]store.ConversationTurn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loads = append(r.loads, "turns:"+id)
	if _, ok := r.states[id]; !ok {
		return nil, nil
	}
	return []store.ConversationTurn{{Dir: "in", FromName: "阿明", Text: "私信暗号"}}, nil
}

// TestPublicChannelNeverReadsDMConversation C7：评论事件不能读到 DM 摘要。
func TestPublicChannelNeverReadsDMConversation(t *testing.T) {
	repo := &recordingConversationRepo{states: map[string]store.ConversationState{
		"dm:1001:2002": {
			ConversationID: "dm:1001:2002", Channel: "dm", Summary: "私信暗号：城西三号",
			TurnCount: 4, ExpiresAt: time.Now().Add(time.Hour),
		},
	}}
	cfg := config.Conversation{
		Enabled: true, DMEnabled: true, CommentEnabled: true,
		DMRolloutPercent: 100, CommentRolloutPercent: 100, RecentTurns: 8, SummaryMaxRunes: 300,
	}
	comment := event.Event{
		ID: "comment:1700001:555:1", Channel: event.ChComment, Dir: event.DirIn,
		Session: "1700001", ReplyTo: "555", Text: "你们刚聊什么",
	}
	got := conversationInput(context.Background(), repo, cfg, nil, comment, zap.NewNop())
	if got != nil {
		t.Fatalf("评论事件读到了会话状态：%+v", got)
	}
	for _, id := range repo.loads {
		if strings.HasPrefix(id, "dm:") || strings.HasPrefix(id, "turns:dm:") {
			t.Fatalf("公开通道读取了 DM 会话：%v", repo.loads)
		}
	}
	touchConversation(context.Background(), repo, cfg, comment, zap.NewNop())
	for _, id := range repo.touches {
		if strings.HasPrefix(id, "dm:") {
			t.Fatalf("公开通道写入了 DM 会话：%v", repo.touches)
		}
	}
}

// TestDMConversationIsIsolatedPerPeer C7：A 的摘要不会出现在 B 的上下文。
func TestDMConversationIsIsolatedPerPeer(t *testing.T) {
	repo := &recordingConversationRepo{states: map[string]store.ConversationState{
		"dm:1001:2002": {
			ConversationID: "dm:1001:2002", Channel: "dm", Summary: "A 的暗号",
			TurnCount: 4, ExpiresAt: time.Now().Add(time.Hour),
		},
	}}
	cfg := config.Conversation{
		Enabled: true, DMEnabled: true, DMRolloutPercent: 100,
		RecentTurns: 8, SummaryMaxRunes: 300,
	}
	peerB := event.Event{
		ID: "dm:1001:3003:1", Channel: event.ChDM, Dir: event.DirIn,
		Session: "1001:3003", FromID: "3003", Text: "在吗", Private: true,
	}
	if got := conversationInput(context.Background(), repo, cfg, nil, peerB, zap.NewNop()); got != nil {
		t.Fatalf("B 读到了别人的会话：%+v", got)
	}
	for _, id := range repo.loads {
		if strings.Contains(id, "2002") {
			t.Fatalf("B 读取了 A 的会话：%v", repo.loads)
		}
	}
}


