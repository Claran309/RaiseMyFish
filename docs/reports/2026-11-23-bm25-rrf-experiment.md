# B8 检索实验：BM25 + RRF（2026-11-23）

## 结论

**不采用**。在同一批 100 条冻结用例上，BM25+RRF 相对基座关键词检索的提升为 **+0.0 点**（71.7 → 71.7），未达到计划要求的 +3 点门槛。

## 实验设置

| 项 | 值 |
|---|---|
| 语料 | `personas/dafeiyu/knowledge` |
| 用例 | `internal/knowledgeeval/testdata/cases.jsonl`（60 正例 / 20 负例 / 20 权限反例） |
| 基座 | `knowledge.FileReader`（现有关键词档位） |
| 实验组 | `knowledgeeval.RRFReader`：基座候选 + BM25 重排 + RRF 融合 |
| 参数 | BM25 k1=1.5, b=0.75；RRF k=60 |
| 复现 | `go test ./internal/knowledgeeval/... -run TestBM25RRFCompareBaseline -v` |

## 实测结果

```
baseline  positive=43/60 (71.7) relevant=44/110 neg_false=3
bm25+rrf  positive=43/60 (71.7) relevant=44/110 neg_false=3  delta=+0.0
```

## 分析

- 基座候选已经很小（Top3），BM25 只在同一批候选内重排，无法把未召回的 17 条正例拉回来；
- 正例失败主要是「无目标标题语义场景」的词面边界问题，不是排序问题；
- 因此下一步应优先做**候选召回**（查询扩展/别名/语义），而不是在 Top3 内 rerank。

## 安全底线

实验未增加负例误返（3 → 3），未增加越权返回（0 → 0）。实验代码保留在 `internal/knowledgeeval/bm25.go`，仅作为离线对照，不接入生产检索路径。

## 后续

- 若要继续，应先用评测集量化「查询扩展」或「别名归一」的召回增益；
- rerank（LLM/外部 API）需先做成本评估与主人授权，本轮不做。
