package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"RaiseMyFish/migrations"
	"gorm.io/gorm"
)

// checksum 覆盖初始建表和种子；已落库迁移的内容禁止修改。
func migrationSource() (string, string, error) {
	ddl, err := migrations.Files.ReadFile("0001_init.sql")
	if err != nil {
		return "", "", fmt.Errorf("读取建表资源：%w", err)
	}
	seed, err := migrations.Files.ReadFile("seed.sql")
	if err != nil {
		return "", "", fmt.Errorf("读取种子资源：%w", err)
	}
	source := string(ddl) + "\n" + string(seed)
	hash := sha256.Sum256([]byte(source))
	return source, hex.EncodeToString(hash[:]), nil
}

type migrationStep struct {
	Version  int64
	Source   string
	Checksum string
}
type migrationReceipt struct {
	Version  int64
	Checksum string
}

func migrationSteps() ([]migrationStep, error) {
	source, checksum, err := migrationSource()
	if err != nil {
		return nil, err
	}
	leases, err := migrations.Files.ReadFile("0002_event_leases.sql")
	if err != nil {
		return nil, fmt.Errorf("读取租约迁移：%w", err)
	}
	hash := sha256.Sum256(leases)
	delivery, err := migrations.Files.ReadFile("0003_send_delivery.sql")
	if err != nil {
		return nil, fmt.Errorf("读取发送迁移：%w", err)
	}
	deliveryHash := sha256.Sum256(delivery)
	viewers, err := migrations.Files.ReadFile("0004_viewer_profiles.sql")
	if err != nil {
		return nil, fmt.Errorf("读取观众档案迁移：%w", err)
	}
	viewersHash := sha256.Sum256(viewers)
	memFrom, err := migrations.Files.ReadFile("0005_memory_from_id.sql")
	if err != nil {
		return nil, fmt.Errorf("读取记忆主体迁移：%w", err)
	}
	memFromHash := sha256.Sum256(memFrom)
	knowledgeEmbeddings, err := migrations.Files.ReadFile("0006_knowledge_embeddings.sql")
	if err != nil {
		return nil, fmt.Errorf("读取知识向量迁移：%w", err)
	}
	knowledgeEmbeddingsHash := sha256.Sum256(knowledgeEmbeddings)
	return []migrationStep{
		{1, source, checksum},
		{2, string(leases), hex.EncodeToString(hash[:])},
		{3, string(delivery), hex.EncodeToString(deliveryHash[:])},
		{4, string(viewers), hex.EncodeToString(viewersHash[:])},
		{5, string(memFrom), hex.EncodeToString(memFromHash[:])},
		{6, string(knowledgeEmbeddings), hex.EncodeToString(knowledgeEmbeddingsHash[:])},
	}, nil
}

func checkMigrationPrefix(rows []migrationReceipt, steps []migrationStep) error {
	if len(rows) > len(steps) {
		return errors.New("迁移包含未知版本")
	}
	for i, row := range rows {
		if row.Version != steps[i].Version || row.Checksum != steps[i].Checksum {
			return errors.New("迁移版本或checksum不匹配")
		}
	}
	return nil
}

// Migrate 校验已应用前缀后顺序升级；保留v1原校验值，失败整体回滚。
func Migrate(ctx context.Context, db *gorm.DB) error {
	steps, err := migrationSteps()
	if err != nil {
		return err
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(current_database()), hashtext(current_schema() || ':migrate'))").Error; err != nil {
			return err
		}
		if err := tx.Exec("CREATE TABLE IF NOT EXISTS schema_migrations (version bigint PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())").Error; err != nil {
			return err
		}
		var rows []migrationReceipt
		if err := tx.Raw("SELECT version,checksum FROM schema_migrations ORDER BY version").Scan(&rows).Error; err != nil {
			return err
		}
		if err := checkMigrationPrefix(rows, steps); err != nil {
			return err
		}
		for _, step := range steps[len(rows):] {
			if err := tx.Exec(step.Source).Error; err != nil {
				return err
			}
			if err := tx.Exec("INSERT INTO schema_migrations(version,checksum) VALUES (?,?)", step.Version, step.Checksum).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return &safeError{"应用迁移失败（检查版本、checksum或既有表冲突）", err}
	}
	return nil
}

// Ready 只验证迁移账本及必需表，不隐式改库。
func Ready(ctx context.Context, db *gorm.DB) error {
	steps, err := migrationSteps()
	if err != nil {
		return err
	}
	var rows []migrationReceipt
	if err := db.WithContext(ctx).Raw("SELECT version,checksum FROM schema_migrations ORDER BY version").Scan(&rows).Error; err != nil {
		return &safeError{"数据库尚未迁移，请执行 fishd migrate up", err}
	}
	if err := checkMigrationPrefix(rows, steps); err != nil {
		return err
	}
	if len(rows) != len(steps) {
		return errors.New("数据库需要升级，请执行 fishd migrate up")
	}
	var exists bool
	err = db.WithContext(ctx).Raw(`SELECT bool_and(to_regclass(name) IS NOT NULL) FROM unnest(ARRAY['events','tasks','memory_sources','memory_records','llm_usage','send_log','dlq']) AS name`).Scan(&exists).Error
	if err != nil {
		return &safeError{"检查数据库表失败", err}
	}
	if !exists {
		return errors.New("数据库业务表不完整")
	}
	if err := db.WithContext(ctx).Exec("SELECT stage,status_changed_at,lease_owner,lease_version,lease_until FROM events LIMIT 0").Error; err != nil {
		return &safeError{"数据库租约字段不完整", err}
	}
	if err := db.WithContext(ctx).Exec("SELECT p.event_id,a.id,l.attempt_id FROM send_plans p JOIN send_attempts a ON a.event_id=p.event_id LEFT JOIN send_log l ON l.attempt_id=a.id LIMIT 0").Error; err != nil {
		return &safeError{"发送迁移字段不完整", err}
	}
	if err := db.WithContext(ctx).Exec("SELECT id,source,title,updated_at,embedding FROM knowledge_embeddings LIMIT 0").Error; err != nil {
		return &safeError{"知识向量表不完整", err}
	}
	return nil
}
