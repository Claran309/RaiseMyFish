package database

// 本文件集中定义「源版本 hash」的 SQL 表达式（C2/N08）。
//
// 源 hash 必须由数据库从原始字段直接派生，而不是由某个写入方顺手更新：
// memory 的 summary 更新、档案重建/清洗、raw SQL 修正都会让源 hash 自然变化，
// 不需要每个写入路径都记得清向量。查询用同一表达式判定 stale，
// 写回用同一表达式做 CAS，三方不可能各算各的。
//
// 表达式只引用当前行字段；调用处必须保证字段名不歧义（单表或无别名）。

import (
	"RaiseMyFish/internal/viewer"
)

// memorySourceHashExpr 覆盖记忆的向量化源字段：title + summary。
//
// 用 JSONB 数组序列化，保留字段边界与顺序，避免「标题末尾+摘要开头」
// 这种拼接歧义；jsonb 的文本表示是确定的，适合做版本 hash。
const memorySourceHashExpr = `encode(sha256(convert_to(jsonb_build_array(COALESCE(title,''), COALESCE(summary,''))::text, 'UTF8')), 'hex')`

// viewerSourceHashExprs 按索引路给出对应的源字段表达式。
//
// 三路各管自己的源：昵称变化不清 facts/interaction，发言变化不清昵称向量。
var viewerSourceHashExprs = map[viewer.EmbeddingKind]string{
	viewer.KindIdentity:    `encode(sha256(convert_to(jsonb_build_array(COALESCE(name,''))::text, 'UTF8')), 'hex')`,
	viewer.KindFacts:       `encode(sha256(convert_to(COALESCE(recent_texts,'[]'::jsonb)::text, 'UTF8')), 'hex')`,
	viewer.KindInteraction: `encode(sha256(convert_to(COALESCE(recent_replies,'[]'::jsonb)::text, 'UTF8')), 'hex')`,
}

// viewerSourceHashExpr 返回某一路的源 hash 表达式；未知 kind 返回 false。
func viewerSourceHashExpr(kind viewer.EmbeddingKind) (string, bool) {
	expr, ok := viewerSourceHashExprs[kind]
	return expr, ok
}
