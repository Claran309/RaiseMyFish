# K2 RAG 派发单（pgvector + 智谱 embedding）

> 背景：docs/06-memory.md / 09-data-model.md / 14-decisions.md 定了 P2 用 pgvector 做向量检索。
> 本批：知识库语义检索（K2 第一步）——梗库 6834 条 embedding 入库，Recall 升级为语义+关键词混合。
> 依赖：pgvector 扩展（本机安装中）+ 智谱 embedding-3 API。

## 一、pgvector（本机已装，本鱼负责）

- `CREATE EXTENSION vector;`
- 表：`knowledge_embeddings`（见下）

## 二、embedding 客户端

`internal/embedding/client.go`：
- OpenAI 兼容：`POST <url> {model, input: string|[]string}` → `{data: [{embedding: []float64}]}`
- 配置：
  ```toml
  [embedding]
  enabled = false
  url = "https://open.bigmodel.cn/api/paas/v4/embeddings"
  model = "embedding-3"
  key_env = "FISH_EMBEDDING_KEY"   # 密钥从环境变量读（不写配置）
  dimension = 0                     # 0 = 自动探测（首次返回长度）
  batch_size = 32                   # 批量请求条数
  ```
- 超时 30s；失败分类（网络/认证/限流/协议）
- 测试：httptest mock（断言 URL/Bearer/批量/解析）

## 三、知识 embedding 入库

`internal/knowledge/embed.go`：
- 表：
  ```sql
  CREATE TABLE knowledge_embeddings (
      id        TEXT PRIMARY KEY,          -- 条目 id（meme-batch-xxx 等）
      source    TEXT NOT NULL,             -- 文件来源（memes.md/learned.md）
      title     TEXT NOT NULL,
      updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
      embedding vector(2048)               -- 维度随 embedding-3
  );
  CREATE INDEX knowledge_embeddings_vec_idx ON knowledge_embeddings USING hnsw (embedding vector_cosine_ops);
  ```
- 任务：`fishctl knowledge embed`（或 fishd 子命令）：
  - 读知识库条目 → 批量调 embedding → 写表（幂等 upsert）
  - 只算「新增/变更」条目（按 updated_at 与知识库 revision 对比）
  - 进度日志（每批 N 条）

## 四、Recall 升级（语义 + 关键词混合）

`internal/knowledge/recall.go`：
- 现有关键词档位保留（title 4 > keywords 3 > tags 2 > summary 1）
- 新增语义档：查询先 embedding → `SELECT id, title, 1 - (embedding <=> query_vec) AS sim FROM knowledge_embeddings ORDER BY embedding <=> query_vec LIMIT N`
- 混合打分：`score = keyword_score + α * sim`（α 可配，默认 0.5）；同条目去重取最高
- embedding 不可用时降级纯关键词（不报错）
- 测试：mock embedding + 真实 PG（集成测试）

## 五、定时重建

- `extractworker` 或独立 goroutine：每小时检查知识库 revision 变化 → 增量 embed 新条目
- 配置 `[embedding] auto_refresh = true` / `refresh_interval_seconds = 3600`

## 六、测试

- embedding 客户端（mock）
- 入库幂等/增量
- Recall 混合打分（mock + 集成）
- 降级（embedding 挂 → 纯关键词）

## 七、禁止

- 不改 contracts、不碰调度/Brain/发送语义、不执行 git
- 密钥只从环境变量（`FISH_EMBEDDING_KEY`），不写配置/日志

## 八、交付格式

- 文件列表、迁移、测试结果、配置说明、未做项、是否暂停写入
