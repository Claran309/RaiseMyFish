# 0.7b 放行单 · P0-PACK-1

> 开发者：WorkBuddy；技术裁决/依赖/Git集成：Codex；独立验收与会话转达：DSH。
> 主人已授权在0.1提交后确认Pack基线并放行；本单与contracts/persona.md属于同一基线提交。

## 必须先读

AGENTS.md → plans/coordination.md → contracts/persona.md（P0-PACK-1）→ 本单 → plans/reports/codex.md 的COMM-005。0.1的已验收提交号见COMM-005；Pack基线以包含本单与契约的Git提交为准。

接手前在自己的报告写COMM-005已读、基线提交号、获派文件、旧草稿位置与状态。DSH协助把本单带入会话；仓库发布不代表应用会话已收到。不执行Git写操作。

## 唯一开发范围

1. internal/persona/meta.go、pack.go、pack_test.go：按基线重作类型、YAML解析、Load与纯访问函数及对应测试；允许新增 internal/persona/meta_test.go、context_test.go 专注该基线测试。
2. personas/dafeiyu/meta.yaml：仅移除过时「P0不引入YAML库/只支持行内数组」注释，不改变字段值或场景；不改其他人格正文和eval数据。
3. plans/reports/workbuddy.md：本人回执、变更、自测、风险与暂停写入状态。
4. 旧 internal/persona/check.go、evalcase.go、runner.go、eval_test.go 超出本任务且依赖旧API。允许**仅将这四个未跟踪草稿**移到 `.workbuddy/persona-eval-draft-comm-005/` 原名留存，记录移动前后SHA256；禁止覆盖已有同名文件，存在冲突就停止该移动并报告。移动前确认源/目标绝对路径均位于本仓库，保留全部内容，不删除。点目录已忽略且Go全仓遍历会跳过，防止未放行回归草稿破坏0.7b构建。

不改go.mod/go.sum、cmd、infra、database、迁移、contracts、协作看板或其他Agent报告。YAML依赖由Codex预先加入；还需其他依赖先报告，不自行go mod tidy。可以正常只读查Git状态与差异。

## 工作内容

- 替换自写YAML子集解析器，严格按Meta的yaml标签解码；采用go.yaml.in/yaml/v3 v3.0.5，未知字段/重复字段/多文档和格式错误均应拒绝。
- Load加Context且返回Pack值；只读五份静态文件，不依赖Brain、PG、eval用例、baseline或真实模型。
- 去掉最小篇幅硬阈值、全局KnownChannels可变slice和动态Assemble；保留契约中的五种口径与纯方法，不新增接口方向。
- 按契约补边界测试；先运行针对性失败测试，再实现修订；不拿baseline 100%当人格达标。

## 交付与验收

报告基线、实际文件、移动草稿与哈希、导出符号对照、测试命令/结果、未验证项、是否暂停写入。至少完成go test ./...、go vet ./...、go build ./...和本模块lint；race在可用的64位编译器或WSL环境执行，失败/未跑如实写明。

DSH依contracts/persona.md验收，不只转抄开发者结果。交付只接收0.7b加载器，不接收本轮被留存的0.7c草稿。0.7b完成后0.6仍需Memory最小类型基线，0.7c仍等待Brain与真实模型；不宣布整项0.7完成。
