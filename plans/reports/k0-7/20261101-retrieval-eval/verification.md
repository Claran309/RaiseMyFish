# 验证记录

- RED：go test ./internal/knowledgeeval -count=1，exit1，缺QueryCase/Evaluator等实现；见red.txt。
- GREEN：go test ./internal/knowledgeeval -count=1，exit0；见green-unit.txt。
- Runner RED：增加输出独占保护测试后缺RunOffline，exit1；见red-runner.txt。
- Runner GREEN：go test ./internal/knowledgeeval ./cmd/knowledgeeval -count=1，exit0；见green-runner.txt。
- 最终：go test ./internal/knowledgeeval ./cmd/knowledgeeval ./internal/knowledge ./internal/brain -count=1，exit0（eval 0.995s、knowledge 1.311s、brain 1.030s；CLI无独立测试文件）。
- go vet ./internal/knowledgeeval ./cmd/knowledgeeval，exit0。
- go build ./internal/knowledgeeval ./cmd/knowledgeeval，exit0。
- gofmt -l internal/knowledgeeval cmd/knowledgeeval，无输出。
- git diff --check -- internal/knowledgeeval cmd/knowledgeeval plans/reports/k0-7/20261101-retrieval-eval，exit0；这些路径当前为新增未跟踪文件，命令不能代替内容审查。
- go test -race ./internal/knowledgeeval -count=1，exit1，运行前C工具链编译失败：cc1.exe: sorry, unimplemented: 64-bit mode not compiled in；未执行race测试，见race.txt。

首次唯一100条运行命令：

```powershell
go run ./cmd/knowledgeeval --corpus personas/dafeiyu/knowledge --cases internal/knowledgeeval/testdata/cases.jsonl --output plans/reports/k0-7/20261101-retrieval-eval/run
```

实际输出：positive=43/60 relevant=46/56 negative_false=0/20 forbidden=0 control=20/20 budget=100/100 top3=true passed=false。CLI返回2，go run打印exit status 2且外壳返回1，属于覆盖质量门禁失败；不是忽略失败后宣称通过。

重放必须使用另一个不存在的新输出目录；禁止复用本run。未再次运行同一冻结100条。freeze.json先于真实Reader写入，results.json内复制相同freeze；所有公开文件原字节复制临时目录，结束删除临时快照，生产目录未写入。

未执行：全仓测试/vet/lint/build、真实PG integration和真实人格35条开关对照。前者由主线程统一集成验证，PG/网络/模型超出本助手获派范围；不得以本目录通过代替。未读.env，无Git写操作，无会话创建，无生产链路启用。
