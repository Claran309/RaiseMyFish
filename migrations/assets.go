// Package migrations 将迁移作为二进制资源交付，避免工作目录影响SQL查找。
package migrations

import "embed"

// Files 是只读的迁移源；生产环境禁止AutoMigrate。
//
//go:embed *.sql
var Files embed.FS
