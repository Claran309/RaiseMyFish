package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"RaiseMyFish/migrations"
	"github.com/jackc/pgx/v5"
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
	memoryViewerEmbeddings, err := migrations.Files.ReadFile("0007_memory_viewer_embeddings.sql")
	if err != nil {
		return nil, fmt.Errorf("读取记忆档案向量迁移：%w", err)
	}
	memoryViewerEmbeddingsHash := sha256.Sum256(memoryViewerEmbeddings)
	accounts, err := migrations.Files.ReadFile("0008_accounts.sql")
	if err != nil {
		return nil, fmt.Errorf("读取账号管理迁移：%w", err)
	}
	accountsHash := sha256.Sum256(accounts)
	extractionJobs, err := migrations.Files.ReadFile("0009_extraction_jobs.sql")
	if err != nil {
		return nil, fmt.Errorf("读取提炼任务迁移：%w", err)
	}
	extractionJobsHash := sha256.Sum256(extractionJobs)
	viewerEmbeddingKinds, err := migrations.Files.ReadFile("0010_viewer_embedding_kinds.sql")
	if err != nil {
		return nil, fmt.Errorf("读取档案向量拆分迁移：%w", err)
	}
	viewerEmbeddingKindsHash := sha256.Sum256(viewerEmbeddingKinds)
	deliveryPlans, err := migrations.Files.ReadFile("0011_delivery_plans.sql")
	if err != nil {
		return nil, fmt.Errorf("读取投递计划迁移：%w", err)
	}
	deliveryPlansHash := sha256.Sum256(deliveryPlans)
	sendLogPlannedAttempt, err := migrations.Files.ReadFile("0012_send_log_planned_attempt.sql")
	if err != nil {
		return nil, fmt.Errorf("读取发送审计迁移：%w", err)
	}
	sendLogPlannedAttemptHash := sha256.Sum256(sendLogPlannedAttempt)
	memoryProvenance, err := migrations.Files.ReadFile("0013_memory_provenance.sql")
	if err != nil {
		return nil, fmt.Errorf("读取记忆主体迁移：%w", err)
	}
	memoryProvenanceHash := sha256.Sum256(memoryProvenance)
	return []migrationStep{
		{1, source, checksum},
		{2, string(leases), hex.EncodeToString(hash[:])},
		{3, string(delivery), hex.EncodeToString(deliveryHash[:])},
		{4, string(viewers), hex.EncodeToString(viewersHash[:])},
		{5, string(memFrom), hex.EncodeToString(memFromHash[:])},
		{6, string(knowledgeEmbeddings), hex.EncodeToString(knowledgeEmbeddingsHash[:])},
		{7, string(memoryViewerEmbeddings), hex.EncodeToString(memoryViewerEmbeddingsHash[:])},
		{8, string(accounts), hex.EncodeToString(accountsHash[:])},
		{9, string(extractionJobs), hex.EncodeToString(extractionJobsHash[:])},
		{10, string(viewerEmbeddingKinds), hex.EncodeToString(viewerEmbeddingKindsHash[:])},
		{11, string(deliveryPlans), hex.EncodeToString(deliveryPlansHash[:])},
		{12, string(sendLogPlannedAttempt), hex.EncodeToString(sendLogPlannedAttemptHash[:])},
		{13, string(memoryProvenance), hex.EncodeToString(memoryProvenanceHash[:])},
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

// migrationSchema 返回当前schema；账本与表检查都锁定它，避免search_path中其他schema的残留对象干扰。
func migrationSchema(db *gorm.DB) (string, error) {
	var schema string
	if err := db.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
		return "", err
	}
	if schema == "" {
		return "", errors.New("无法确定当前schema")
	}
	return schema, nil
}

func migrationLedger(schema string) string {
	return pgx.Identifier{schema, "schema_migrations"}.Sanitize()
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
		schema, err := migrationSchema(tx)
		if err != nil {
			return err
		}
		ledger := migrationLedger(schema)
		if err := tx.Exec("CREATE TABLE IF NOT EXISTS " + ledger + " (version bigint PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())").Error; err != nil {
			return err
		}
		var rows []migrationReceipt
		if err := tx.Raw("SELECT version,checksum FROM " + ledger + " ORDER BY version").Scan(&rows).Error; err != nil {
			return err
		}
		if err := checkMigrationPrefix(rows, steps); err != nil {
			return err
		}
		for _, step := range steps[len(rows):] {
			if err := tx.Exec(step.Source).Error; err != nil {
				return err
			}
			if err := tx.Exec("INSERT INTO "+ledger+"(version,checksum) VALUES (?,?)", step.Version, step.Checksum).Error; err != nil {
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
	db = db.WithContext(ctx)
	schema, err := migrationSchema(db)
	if err != nil {
		return &safeError{"检查数据库表失败", err}
	}
	ledger := migrationLedger(schema)
	var rows []migrationReceipt
	if err := db.Raw("SELECT version,checksum FROM " + ledger + " ORDER BY version").Scan(&rows).Error; err != nil {
		return &safeError{"数据库尚未迁移，请执行 fishd migrate up", err}
	}
	if err := checkMigrationPrefix(rows, steps); err != nil {
		return err
	}
	if len(rows) != len(steps) {
		return errors.New("数据库需要升级，请执行 fishd migrate up")
	}
	var exists bool
	err = db.Raw(`SELECT bool_and(to_regclass(format('%I.%I', ?::text, name)) IS NOT NULL) FROM unnest(ARRAY['events','tasks','memory_sources','memory_records','llm_usage','send_log','dlq','extraction_jobs']) AS name`, schema).Scan(&exists).Error
	if err != nil {
		return &safeError{"检查数据库表失败", err}
	}
	if !exists {
		return errors.New("数据库业务表不完整")
	}
	table := func(name string) string { return pgx.Identifier{schema, name}.Sanitize() }
	if err := db.Exec("SELECT stage,status_changed_at,lease_owner,lease_version,lease_until FROM " + table("events") + " LIMIT 0").Error; err != nil {
		return &safeError{"数据库租约字段不完整", err}
	}
	if err := db.Exec("SELECT p.event_id,a.id,l.attempt_id FROM " + table("send_plans") + " p JOIN " + table("send_attempts") + " a ON a.event_id=p.event_id LEFT JOIN " + table("send_log") + " l ON l.attempt_id=a.id LIMIT 0").Error; err != nil {
		return &safeError{"发送迁移字段不完整", err}
	}
	if err := db.Exec("SELECT id,source,title,updated_at,embedding FROM " + table("knowledge_embeddings") + " LIMIT 0").Error; err != nil {
		return &safeError{"知识向量表不完整", err}
	}
	if err := db.Exec("SELECT embedding,embedding_text_hash,embedding_updated_at FROM " + table("memory_records") + " LIMIT 0").Error; err != nil {
		return &safeError{"记忆向量字段不完整", err}
	}
	if err := db.Exec("SELECT asserted_by_id,asserted_by_name,subject FROM " + table("memory_records") + " LIMIT 0").Error; err != nil {
		return &safeError{"记忆主体字段不完整", err}
	}
	if err := db.Exec("SELECT identity_embedding,identity_embedding_hash,identity_embedding_updated_at,facts_embedding,facts_embedding_hash,facts_embedding_updated_at,interaction_embedding,interaction_embedding_hash,interaction_embedding_updated_at FROM " + table("viewer_profiles") + " LIMIT 0").Error; err != nil {
		return &safeError{"档案向量字段不完整", err}
	}
	if err := db.Exec("SELECT id,mid,name,cookie,csrf,status,last_check,note,is_active FROM " + table("accounts") + " LIMIT 0").Error; err != nil {
		return &safeError{"账号管理表不完整", err}
	}
	if err := db.Exec("SELECT event_id,extraction_version,status,lease_owner,lease_until,attempts,last_error,created_at,updated_at FROM " + table("extraction_jobs") + " LIMIT 0").Error; err != nil {
		return &safeError{"提炼任务表不完整", err}
	}
	return nil
}
