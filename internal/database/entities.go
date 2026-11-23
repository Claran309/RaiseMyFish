package database

import (
	"context"
	"strings"

	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// EntityRepo 维护实体/关系并做 1 跳邻域查询。
type EntityRepo struct{ db *gorm.DB }

func NewEntityRepo(db *gorm.DB) (*EntityRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &EntityRepo{db: db}, nil
}

// Upsert 幂等写实体；同 type+uid 已存在时只补别名。
func (r *EntityRepo) Upsert(ctx context.Context, e memory.Entity) error {
	if ctx == nil || strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.Type) == "" || strings.TrimSpace(e.CanonicalName) == "" {
		return store.ErrInvalid
	}
	err := r.db.WithContext(ctx).Exec(`INSERT INTO memory_entities(id,type,canonical_name,aliases,uid)
VALUES(?,?,?,?,NULLIF(?,''))
ON CONFLICT (id) DO UPDATE SET
  canonical_name = EXCLUDED.canonical_name,
  aliases = ARRAY(SELECT DISTINCT unnest(memory_entities.aliases || EXCLUDED.aliases)),
  uid = COALESCE(EXCLUDED.uid, memory_entities.uid)`,
		e.ID, e.Type, e.CanonicalName, pgTextArray(e.Aliases), e.UID).Error
	if err != nil {
		return &safeError{"写入记忆实体失败", err}
	}
	return nil
}

// AddRelation 写关系；同一 from/predicate/to 有效边只保留一条。
func (r *EntityRepo) AddRelation(ctx context.Context, rel memory.Relation) error {
	if ctx == nil || strings.TrimSpace(rel.FromID) == "" || strings.TrimSpace(rel.ToID) == "" || strings.TrimSpace(rel.Predicate) == "" || rel.FromID == rel.ToID {
		return store.ErrInvalid
	}
	err := r.db.WithContext(ctx).Exec(`INSERT INTO memory_relations(from_entity_id,predicate,to_entity_id,valid_from,confidence,source_event_ids)
SELECT ?,?,?,clock_timestamp(),?,? WHERE NOT EXISTS (
  SELECT 1 FROM memory_relations WHERE from_entity_id=? AND predicate=? AND to_entity_id=? AND valid_to IS NULL
)`, rel.FromID, rel.Predicate, rel.ToID, rel.Confidence, pgTextArray(rel.SourceEventIDs),
		rel.FromID, rel.Predicate, rel.ToID).Error
	if err != nil {
		return &safeError{"写入记忆关系失败", err}
	}
	return nil
}

// Neighbors 返回 1 跳实体名与谓词；只返回公开实体名，不带记忆正文。
func (r *EntityRepo) Neighbors(ctx context.Context, entityID string) ([]string, error) {
	if ctx == nil || strings.TrimSpace(entityID) == "" {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		Name      string
		Predicate string
	}
	err := r.db.WithContext(ctx).Raw(`
SELECT e.canonical_name AS name, r.predicate
  FROM memory_relations r
  JOIN memory_entities e ON e.id = r.to_entity_id
 WHERE r.from_entity_id = ? AND r.valid_to IS NULL
 UNION ALL
SELECT e.canonical_name AS name, r.predicate
  FROM memory_relations r
  JOIN memory_entities e ON e.id = r.from_entity_id
 WHERE r.to_entity_id = ? AND r.valid_to IS NULL
 LIMIT 20`, entityID, entityID).Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"查询实体邻域失败", err}
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Name+"("+row.Predicate+")")
	}
	return out, nil
}

// pgTextArray 把 Go 切片转成 PG TEXT[] 参数，不拼接字符串。
func pgTextArray(items []string) string {
	if len(items) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteString("{")
	for i, item := range items {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"`)
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(item, `\`, `\\`), `"`, `\"`))
		b.WriteString(`"`)
	}
	b.WriteString("}")
	return b.String()
}
