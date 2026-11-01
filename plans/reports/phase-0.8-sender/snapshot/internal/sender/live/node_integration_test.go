//go:build integration

package live

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"RaiseMyFish/internal/sender"
)

func TestNodeSyntheticFixtures(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node集成测试要求安装Node：", err)
	}
	for _, mode := range []string{"accepted", "unknown", "rejected", "unverified", "preflight-auth"} {
		t.Run(mode, func(t *testing.T) {
			script, err := filepath.Abs(filepath.Join("testdata", "node-"+mode+".mjs"))
			if err != nil {
				t.Fatal(err)
			}
			o := DefaultNodeOptions()
			o.NodePath, o.ScriptPath = node, script
			n, err := NewNodeTransport(o)
			if err != nil {
				t.Fatal(err)
			}
			s, err := n.Open(context.Background(), nodeAttempt())
			if mode == "preflight-auth" {
				var typed *sender.Error
				if !errors.As(err, &typed) || typed.Kind != sender.KindAuth {
					t.Fatalf("预检鉴权：%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer closeNodeTestSession(t, s)
			v, err := s.Post(context.Background(), nodePermit())
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"accepted": sender.OutcomeAccepted, "unknown": sender.OutcomeUnknown, "rejected": sender.OutcomeRejected, "unverified": sender.OutcomeAccepted}[mode]
			if v.Outcome != want || v.Verified != (mode == "accepted") {
				t.Fatalf("fixture结果：%+v", v)
			}
			verified, err := n.Verify(context.Background(), nodeAttempt())
			if err != nil {
				t.Fatal(err)
			}
			if verified.Verified != (mode != "unknown") {
				t.Fatalf("fixture核验：%+v", verified)
			}
		})
	}
}
