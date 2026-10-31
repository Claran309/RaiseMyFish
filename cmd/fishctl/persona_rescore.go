package main

import (
	"RaiseMyFish/internal/personaeval"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
)

// runPersonaRescore 不加载.env、不连接PG、不构造模型生成器。
func runPersonaRescore(ctx context.Context, args []string, out, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs := flag.NewFlagSet("persona rescore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	source := fs.String("source", "", "原始真实run目录")
	target := fs.String("output", "", "独立新重评目录")
	version := fs.String("rules-version", "persona-lexical-v2", "评分版本")
	if err := fs.Parse(args); err != nil {
		return errors.New("离线重评参数无效")
	}
	if fs.NArg() != 0 || *source == "" || *target == "" {
		return errors.New("离线重评必须指定source和独立output")
	}
	scorer, err := personaeval.NewScorerVersion(*version)
	if err != nil {
		return err
	}
	report, err := personaeval.RescoreExisting(*source, *target, scorer)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "source_run=%s rules=%s raw=%d/%d (%.1f%%) delivered=%d/%d (%.1f%%) actual_model_calls=0 new_model_validation=false report=%s\n", report.SourceRunID, report.RulesVersion, report.RawPassed, report.Total, report.RawPassRate*100, report.DeliveredPassed, report.Total, report.DeliveredPassRate*100, filepath.Join(*target, "report.md"))
	return err
}
