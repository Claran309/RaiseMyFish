package main

// L1：死信查看与受保护重放。重放只重置从未碰过平台的 dead 事件。

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/infra"
)

func runDLQ(ctx context.Context, configPath, envPath string, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("用法：fishctl dlq list [--limit 20] [--all] | dlq replay --id N")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("dlq list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		limit := fs.Int("limit", 20, "返回条数")
		all := fs.Bool("all", false, "包含已重放")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		repo, cleanup, err := openDLQ(ctx, configPath, envPath)
		if err != nil {
			return err
		}
		defer cleanup()
		items, err := repo.List(ctx, *limit, !*all)
		if err != nil {
			return err
		}
		for _, item := range items {
			fmt.Fprintf(stdout, "%d\t%s\t%s\t%s\treplayed=%t\t%s\n", item.ID, item.EventID, item.Stage, item.Error, item.Replayed, item.At.Format(time.RFC3339))
		}
		return nil
	case "replay":
		fs := flag.NewFlagSet("dlq replay", flag.ContinueOnError)
		fs.SetOutput(stderr)
		id := fs.Int64("id", 0, "死信 ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id < 1 {
			return errors.New("必须指定 --id")
		}
		repo, cleanup, err := openDLQ(ctx, configPath, envPath)
		if err != nil {
			return err
		}
		defer cleanup()
		result, err := repo.Replay(ctx, *id)
		if err != nil {
			return err
		}
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, string(data))
		return err
	default:
		return errors.New("用法：fishctl dlq list|replay")
	}
}

func openDLQ(ctx context.Context, configPath, envPath string) (*database.DLQRepo, func(), error) {
	cfg, err := config.LoadForStatus(configPath, envPath)
	if err != nil {
		return nil, nil, err
	}
	probe, cancel := context.WithTimeout(ctx, time.Duration(cfg.Database.TimeoutSeconds)*time.Second)
	defer cancel()
	resources, err := infra.Setup(probe, cfg)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = resources.Close(context.Background()) }
	if err := database.Ready(probe, resources.DB); err != nil {
		cleanup()
		return nil, nil, err
	}
	repo, err := database.NewDLQRepo(resources.DB)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return repo, cleanup, nil
}
