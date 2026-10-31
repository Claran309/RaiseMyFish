package knowledge

import (
	"strings"
	"testing"

	"RaiseMyFish/internal/event"
)

func publicScope() Scope { return Scope{Channel: event.ChLive, Session: "room"} }

func validContext() Context {
	return Context{Scope: publicScope(), Revision: strings.Repeat("a", 64), Items: []Item{{ID: "one", Source: "test:one", Text: "知识正文", Scenes: []string{"live"}, Priority: 50}}}
}

func TestValidateScope(t *testing.T) {
	for _, scope := range []Scope{publicScope(), {Channel: event.ChComment, Session: "thread"}, {Channel: event.ChDM, Session: "alice", Private: true}, {Channel: event.ChQQ, Session: "group"}, {Channel: event.ChQQ, Session: "alice", Private: true}} {
		if err := ValidateScope(scope); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []Scope{{}, {Channel: event.ChLive, Session: " "}, {Channel: "bad", Session: "a"}, {Channel: event.ChDM, Session: "a"}, {Channel: event.ChLive, Session: "a", Private: true}, {Channel: event.ChComment, Session: "a", Private: true}, {Channel: event.ChQQ, Session: string([]byte{255})}} {
		if err := ValidateScope(scope); err == nil {
			t.Fatalf("非法范围未拒绝：%+v", scope)
		}
	}
}

func TestContextValidate(t *testing.T) {
	empty := Context{Scope: publicScope(), Revision: strings.Repeat("0", 64)}
	if err := empty.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := validContext().Validate(); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Context){
		"空范围":           func(c *Context) { c.Scope = Scope{} },
		"空版本":           func(c *Context) { c.Revision = "" },
		"大写版本":          func(c *Context) { c.Revision = strings.Repeat("A", 64) },
		"非hex版本":        func(c *Context) { c.Revision = strings.Repeat("z", 64) },
		"空ID":           func(c *Context) { c.Items[0].ID = " " },
		"空来源":           func(c *Context) { c.Items[0].Source = "" },
		"绝对路径来源":        func(c *Context) { c.Items[0].Source = `C:\secret\knowledge.md` },
		"空白绕过Windows路径": func(c *Context) { c.Items[0].Source = ` C:\secret\knowledge.md` },
		"空白绕过POSIX路径":   func(c *Context) { c.Items[0].Source = " /private/knowledge.md" },
		"POSIX来源":       func(c *Context) { c.Items[0].Source = "/private/knowledge.md" },
		"空正文":           func(c *Context) { c.Items[0].Text = " " },
		"非法UTF8":        func(c *Context) { c.Items[0].Text = string([]byte{255}) },
		"负优先级":          func(c *Context) { c.Items[0].Priority = -1 },
		"高优先级":          func(c *Context) { c.Items[0].Priority = 101 },
		"空场景":           func(c *Context) { c.Items[0].Scenes = nil },
		"错场景":           func(c *Context) { c.Items[0].Scenes = []string{"dm"} },
		"未知场景":          func(c *Context) { c.Items[0].Scenes = []string{"qq"} },
		"重复场景":          func(c *Context) { c.Items[0].Scenes = []string{"live", "live"} },
		"重复ID":          func(c *Context) { c.Items = append(c.Items, c.Items[0]) },
		"公开含私密": func(c *Context) {
			c.Items[0].Private = true
			c.Items[0].Origin = &Scope{Channel: event.ChDM, Session: "alice", Private: true}
		},
		"DM伪公开": func(c *Context) { c.Items[0].Origin = &Scope{Channel: event.ChDM, Session: "alice", Private: true} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			c := validContext()
			mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("非法上下文未拒绝")
			}
		})
	}
}

func TestPrivateContextValidate(t *testing.T) {
	scope := Scope{Channel: event.ChDM, Session: "alice", Private: true}
	c := validContext()
	c.Scope = scope
	c.Items[0].Scenes = []string{"dm"}
	c.Items[0].Private = true
	c.Items[0].Origin = &scope
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []*Scope{nil, {Channel: event.ChDM, Session: "bob", Private: true}, {Channel: event.ChQQ, Session: "alice", Private: true}, {Channel: event.ChDM, Session: "alice"}} {
		c.Items[0].Origin = origin
		if err := c.Validate(); err == nil {
			t.Fatal("跨会话或非法私密来源未拒绝")
		}
	}
}
