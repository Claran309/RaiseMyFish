// knowledgeeval 仅运行离线检索；不会加载.env、配置或生产Provider。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"RaiseMyFish/internal/knowledgeeval"
)

func main() {
	corpus := flag.String("corpus", "personas/dafeiyu/knowledge", "公开知识目录")
	cases := flag.String("cases", "internal/knowledgeeval/testdata/cases.jsonl", "冻结100条查询JSONL")
	output := flag.String("output", "", "必须指定不存在的新输出目录；父目录须存在")
	flag.Parse()
	if *output == "" {
		fmt.Fprintln(os.Stderr, "必须指定--output新目录")
		os.Exit(1)
	}
	result, err := knowledgeeval.RunOffline(context.Background(), *corpus, *cases, *output)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	m := result.Report.Metrics
	fmt.Printf("positive=%d/60 relevant=%d/%d negative_false=%d/20 forbidden=%d control=%d/20 budget=%d/100 top3=%t passed=%t\n", m.PositiveHits, m.Relevant, m.Returned, m.NegativeFalse, m.ForbiddenReturns, m.ControlsPassed, m.BudgetPassed, result.Order.Passed, result.Passed)
	if !result.Passed {
		os.Exit(2)
	}
}
