package memory

// 本文件是实体/关系的纯规则：只从明确句式抽取，低置信不写，不自动推断。

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Entity 是实体归一结果。
type Entity struct {
	ID            string
	Type          string
	CanonicalName string
	Aliases       []string
	UID           string
}

// Relation 是一条有向关系。
type Relation struct {
	FromID         string
	FromName       string
	Predicate      string
	ToID           string
	ToName         string
	Confidence     float64
	SourceEventIDs []string
}

var relationPatterns = []struct {
	pattern   *regexp.Regexp
	predicate string
}{
	{regexp.MustCompile(`^(.{1,20})是(.{1,20})的室友$`), "roommate"},
	{regexp.MustCompile(`^(.{1,20})是(.{1,20})的朋友$`), "friend"},
	{regexp.MustCompile(`^(.{1,20})是(.{1,20})的同学$`), "classmate"},
	{regexp.MustCompile(`^(.{1,20})是(.{1,20})的同事$`), "colleague"},
}

// ExtractRelations 只识别「X 是 Y 的室友/朋友/同学/同事」这类明确句式。
//
// 返回 nil 表示没有可信关系；不猜「一起玩」等模糊语义。
func ExtractRelations(text, sourceEventID string) []Relation {
	text = strings.TrimSpace(text)
	if text == "" || sourceEventID == "" {
		return nil
	}
	var out []Relation
	for _, item := range relationPatterns {
		match := item.pattern.FindStringSubmatch(text)
		if match == nil {
			continue
		}
		from, to := strings.TrimSpace(match[1]), strings.TrimSpace(match[2])
		if from == "" || to == "" || from == to {
			continue
		}
		out = append(out, Relation{
			FromID: EntityID("person", from), FromName: from,
			Predicate: item.predicate,
			ToID:      EntityID("person", to), ToName: to,
			Confidence: 0.8, SourceEventIDs: []string{sourceEventID},
		})
	}
	return out
}

// EntityID 由类型+规范名派生稳定 ID；不暴露原始 UID 到公开 ID。
func EntityID(entityType, canonical string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(canonical))))
	return "ent:" + entityType + ":" + hex.EncodeToString(sum[:])[:12]
}

// PersonEntity 从可信 UID/昵称构造 person 实体；无 UID 时仍可用昵称归一。
func PersonEntity(uid, name string) Entity {
	canonical := strings.TrimSpace(name)
	if canonical == "" {
		canonical = strings.TrimSpace(uid)
	}
	return Entity{
		ID: EntityID("person", canonical), Type: "person",
		CanonicalName: canonical, UID: strings.TrimSpace(uid),
	}
}
