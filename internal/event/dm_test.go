package event

import "testing"

func TestSplitDMSession(t *testing.T) {
	actor, peer, ok := SplitDMSession(DMSession("1001", "2002"))
	if !ok || actor != "1001" || peer != "2002" {
		t.Fatalf("拆分=%q/%q ok=%v", actor, peer, ok)
	}
	for _, bad := range []string{"", "2002", "1001:", ":2002", "a:2002", "1001:b", "1:2:3"} {
		if _, _, ok := SplitDMSession(bad); ok {
			t.Errorf("非法会话键被接受：%q", bad)
		}
	}
}

func TestDMRecvActor(t *testing.T) {
	consistent := Event{
		Session: DMSession("1001", "2002"),
		Meta:    []byte(`{"recv_actor":"1001","platform":"bili"}`),
	}
	if actor, ok := DMRecvActor(consistent); !ok || actor != "1001" {
		t.Fatalf("一致的事件=%q ok=%v", actor, ok)
	}
	mismatch := Event{
		Session: DMSession("1001", "2002"),
		Meta:    []byte(`{"recv_actor":"1002"}`),
	}
	if actor, ok := DMRecvActor(mismatch); ok {
		t.Fatalf("Session 与 Meta 冲突仍返回账号 %q", actor)
	}
	if actor, ok := DMRecvActor(Event{Meta: []byte(`{"recv_actor":"1003"}`)}); !ok || actor != "1003" {
		t.Fatalf("只有 Meta 时=%q ok=%v", actor, ok)
	}
	if actor, ok := DMRecvActor(Event{Session: DMSession("1004", "2002")}); !ok || actor != "1004" {
		t.Fatalf("只有复合 Session 时=%q ok=%v", actor, ok)
	}
	if actor, ok := DMRecvActor(Event{Session: "2002"}); ok {
		t.Fatalf("旧裸会话不应推断接收账号：%q", actor)
	}
	if actor, ok := DMRecvActor(Event{Session: DMSession("1005", "2002"), Meta: []byte(`{"recv_actor":"bad"}`)}); !ok || actor != "1005" {
		t.Fatalf("非法 Meta 应忽略：%q ok=%v", actor, ok)
	}
}

func TestDMRecvActorConflict(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   Event
		want bool
	}{
		{"一致", Event{Session: DMSession("1001", "2002"), Meta: []byte(`{"recv_actor":"1001"}`)}, false},
		{"明确冲突", Event{Session: DMSession("1001", "2002"), Meta: []byte(`{"recv_actor":"1002"}`)}, true},
		{"仅Meta", Event{Meta: []byte(`{"recv_actor":"1003"}`)}, false},
		{"仅复合Session", Event{Session: DMSession("1004", "2002")}, false},
		{"旧裸Session", Event{Session: "2002"}, false},
		{"非法Meta", Event{Session: DMSession("1005", "2002"), Meta: []byte(`{"recv_actor":"bad"}`)}, false},
		{"无归属", Event{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DMRecvActorConflict(tc.ev); got != tc.want {
				t.Fatalf("conflict=%v want %v", got, tc.want)
			}
		})
	}
}

func TestDMPeer(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   Event
		peer string
	}{
		{"composite", Event{Session: DMSession("1001", "2002"), FromID: "1001"}, "2002"},
		{"legacy_session", Event{Session: "2002", FromID: "2002"}, "2002"},
		{"from_id_only", Event{FromID: "2003"}, "2003"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer, ok := DMPeer(tc.ev)
			if !ok || peer != tc.peer {
				t.Fatalf("peer=%q ok=%v want %q", peer, ok, tc.peer)
			}
		})
	}
	if peer, ok := DMPeer(Event{Session: "not-a-uid"}); ok {
		t.Fatalf("非法事件解析出收信人 %q", peer)
	}
}
