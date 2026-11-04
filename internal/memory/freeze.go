package memory

// 记忆冻结：状态文件存在即冻结，删除即解冻。
//
// 为什么不改配置：冻结是运行时开关，改 TOML 需要重启 fishd 才生效；
// 用状态文件可在不停服的情况下让提炼 worker 立即停写（下一轮生效）。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultFreezePath 是记忆冻结状态文件的默认位置，fishd 与 fishctl 共用。
const DefaultFreezePath = "state/memory.frozen"

// IsFrozen 判断冻结标记是否存在；文件不存在表示未冻结。
func IsFrozen(path string) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return false, errors.New("冻结状态文件路径不能为空")
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("检查记忆冻结状态：%w", err)
	}
	return true, nil
}

// Freeze 创建冻结标记并记录冻结时间；已存在时覆盖时间，保持幂等。
func Freeze(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("冻结状态文件路径不能为空")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建冻结状态目录：%w", err)
		}
	}
	body := time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("写入冻结标记：%w", err)
	}
	return nil
}

// Unfreeze 删除冻结标记；标记不存在时视为已解冻，不报错。
func Unfreeze(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("冻结状态文件路径不能为空")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除冻结标记：%w", err)
	}
	return nil
}
