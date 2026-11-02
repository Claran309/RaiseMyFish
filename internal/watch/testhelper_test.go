package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type commandCall struct {
	Name string
	Args []string
}

type fakeCommander struct {
	calls []commandCall
	run   func(name string, args ...string) ([]byte, error)
}

func (f *fakeCommander) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, commandCall{Name: name, Args: append([]string(nil), args...)})
	if f.run != nil {
		return f.run(name, args...)
	}
	return nil, nil
}

func (f *fakeCommander) count(name string) int {
	total := 0
	for _, call := range f.calls {
		if call.Name == name {
			total++
		}
	}
	return total
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func createLastArg(t *testing.T, args []string, data []byte) error {
	t.Helper()
	if len(args) == 0 {
		t.Fatal("命令缺少参数")
	}
	return os.WriteFile(args[len(args)-1], data, 0o600)
}

func lastArg(t *testing.T, args []string) string {
	t.Helper()
	if len(args) == 0 {
		t.Fatal("命令缺少参数")
	}
	return args[len(args)-1]
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

func tempPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}
