package live

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNodeKeepsCancellationAndStartCauses(t *testing.T) {
	o := DefaultNodeOptions()
	o.NodePath = filepath.Join(t.TempDir(), "missing-private-runtime")
	n, err := NewNodeTransport(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := n.Open(ctx, nodeAttempt()); !errors.Is(err, context.Canceled) {
		t.Fatal("取消原因丢失", err)
	}
	// Windows查扩展名可返回exec.ErrNotFound，Unix启动则通常为os.ErrNotExist。
	if _, err := n.Open(context.Background(), nodeAttempt()); !errors.Is(err, os.ErrNotExist) && !errors.Is(err, exec.ErrNotFound) {
		t.Fatal("启动原因丢失", err)
	}
	if _, err := n.Open(context.Background(), nodeAttempt()); errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("普通启动失败被误报取消", err)
	}
}
