package dryrun

import (
	"context"
	"net/http"
	"testing"
)

type panicTransport struct {
	t *testing.T
}

func (p panicTransport) RoundTrip(*http.Request) (*http.Response, error) {
	p.t.Fatal("dry-run 不应触发 http.DefaultTransport")
	return nil, nil
}

// TestSendDoesNotUseDefaultNetwork 证明 dry-run 不经过默认网络客户端。
func TestSendDoesNotUseDefaultNetwork(t *testing.T) {
	old := http.DefaultTransport
	http.DefaultTransport = panicTransport{t: t}
	defer func() { http.DefaultTransport = old }()

	c := claim()
	if _, err := New("network-test").Send(context.Background(), c, reply(c)); err != nil {
		t.Fatal(err)
	}
}
