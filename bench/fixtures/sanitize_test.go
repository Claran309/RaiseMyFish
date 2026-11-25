package main

import (
	"bufio"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
)

func TestSanitizeEventHashesUIDsAndTruncatesText(t *testing.T) {
	ev := event.Event{
		ID: "dm:1001:2002:7", Channel: event.ChDM, Dir: event.DirIn,
		Session: "1001:2002", FromID: "2002", FromName: "真实昵称",
		Text: strings.Repeat("鱼", 120), Private: true, At: time.Now(), Status: event.StatusPending,
		Meta: json.RawMessage(`{"recv_actor":"1001","uid":"2002","pics":["https://real/x.jpg"]}`),
	}
	clean := SanitizeEvent(ev, DefaultSanitizeOptions())
	if strings.Contains(clean.Text, "真实") || len([]rune(clean.Text)) != 80 {
		t.Fatalf("正文未截断：%d", len([]rune(clean.Text)))
	}
	if clean.FromID == ev.FromID || !strings.HasPrefix(clean.FromID, "u") {
		t.Fatalf("UID 未哈希：%q", clean.FromID)
	}
	if clean.Session == ev.Session || strings.Contains(clean.Session, "1001") {
		t.Fatalf("Session 未脱敏：%q", clean.Session)
	}
	if !strings.HasPrefix(clean.ID, "dm:"+clean.Session+":") {
		t.Fatalf("ID 未同步：%q", clean.ID)
	}
	var meta map[string]any
	if err := json.Unmarshal(clean.Meta, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["recv_actor"] == "1001" || meta["uid"] == "2002" {
		t.Fatalf("Meta 身份未脱敏：%v", meta)
	}
	if _, ok := meta["pics"]; ok {
		t.Fatal("图片外链未删除")
	}
	if err := store.ValidateEvent(clean); err != nil {
		t.Fatalf("脱敏后事件不合法：%v", err)
	}
}

func TestGenerateRecordsAreValidAndOrdered(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	now := time.Now().UTC()
	previous := time.Time{}
	for i := 0; i < 200; i++ {
		record := generateRecord(i, rnd, now, 70, 15, 15, false, 4*time.Minute, 1)
		if err := validateRecord(record); err != nil {
			t.Fatal(err)
		}
		if err := store.ValidateEvent(record.Event()); err != nil {
			t.Fatalf("第 %d 条不合法：%v", i, err)
		}
		if record.At.Before(previous) {
			t.Fatalf("第 %d 条时间倒序", i)
		}
		previous = record.At
	}
}

func TestSanitizeFile(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.jsonl")
	out := filepath.Join(dir, "out.jsonl")
	longText := strings.Repeat("真实弹幕", 40)
	raw := `{"id":"live:10000001:1","channel":"live","dir":"in","session":"10000001","from_id":"42","text":"` + longText + `","at":"2026-11-24T10:00:00Z"}`
	if err := os.WriteFile(in, []byte(raw+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	count, err := sanitizeFile(in, w, DefaultSanitizeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count=%d", count)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"from_id":"42"`) || strings.Contains(string(data), longText) {
		t.Fatalf("未脱敏：%s", data)
	}
}
