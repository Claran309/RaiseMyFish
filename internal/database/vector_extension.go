package database

// 本文件保证 pgvector 扩展固定在 public schema 初始化（D2/N16）。
//
// 集成测试在随机 schema 下迁移（search_path 首位是测试 schema）。
// 空库首次 CREATE EXTENSION 若不指定 schema，扩展会落进当时的第一位 schema：
// 其他 schema 解析不到 vector 类型，测试清理 DROP SCHEMA CASCADE 还会
// 连同数据库级扩展一起删除，破坏同一数据库上的其他使用者。
//
// 0006/0007 已应用，不能原地修改（checksum 会变）。因此把安装动作前移到
// 迁移基础设施：显式装到 public 后，0006/0007 的 CREATE EXTENSION
// IF NOT EXISTS 变成无副作用。

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// vectorExtensionSchema 是向量扩展唯一允许安装的位置。
const vectorExtensionSchema = "public"

// ensureVectorExtension 把 vector 扩展固定安装到 public。
//
// 并发：不同 schema 可能同时首迁，CREATE EXTENSION 本身不是并发安全的。
// 用事务级 advisory lock 按数据库串行化；锁随事务提交自动释放，
// 不阻塞后续各自 schema 的迁移。
func ensureVectorExtension(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(current_database()), hashtext('vector-extension:schema-public'))").Error; err != nil {
			return fmt.Errorf("锁定向量扩展初始化：%w", err)
		}
		var schemas []string
		if err := tx.Raw("SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'vector'").Scan(&schemas).Error; err != nil {
			return fmt.Errorf("查询向量扩展状态：%w", err)
		}
		switch len(schemas) {
		case 0:
			if err := tx.Exec("CREATE EXTENSION vector SCHEMA " + vectorExtensionSchema).Error; err != nil {
				return fmt.Errorf("安装向量扩展到 %s：%w", vectorExtensionSchema, err)
			}
			return nil
		case 1:
			if schemas[0] != vectorExtensionSchema {
				return fmt.Errorf("向量扩展已安装在 %q，必须位于 %q", schemas[0], vectorExtensionSchema)
			}
			return nil
		default:
			return errors.New("向量扩展状态异常：存在多个安装记录")
		}
	})
}
