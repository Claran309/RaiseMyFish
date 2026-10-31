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
	revision := fs.String("cases-revision", "", "只改变expect的修订用例，另存快照")
	revisionSHA := fs.String("cases-sha256", "", "修订用例必须匹配的SHA256")
	if err := fs.Parse(args); err != nil {
		return errors.New("离线重评参数无效")
	}
	if fs.NArg() != 0 || *source == "" || *target == "" {
		return errors.New("离线重评必须指定source和独立output")
	}
	if (*revision == "") != (*revisionSHA == "") {
		return errors.New("修订用例与SHA256必须同时指定")
	}
	scorer, err := personaeval.NewScorerVersion(*version)
	if err != nil {
		return err
	}
	var report personaeval.RescoreReport
	if *revision == "" {
		report, err = personaeval.RescoreExisting(*source, *target, scorer)
	} else {
		report, err = personaeval.RescoreWithExpectations(*source, *target, scorer, *revision, *revisionSHA)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "source_run=%s rules=%s raw=%d/%d (%.1f%%) delivered=%d/%d (%.1f%%) actual_model_calls=0 new_model_validation=false report=%s\n", report.SourceRunID, report.RulesVersion, report.RawPassed, report.Total, report.RawPassRate*100, report.DeliveredPassed, report.Total, report.DeliveredPassRate*100, filepath.Join(*target, "report.md"))
	return err
}
