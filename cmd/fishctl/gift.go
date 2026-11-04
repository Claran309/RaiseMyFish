package main

// 本文件实现 `fishctl gift send` 手动送礼命令。
//
// 边界：
//   - 只读 [gift] 配置，不在命令层写死房间/价格/限额；
//   - 默认 dry-run（real=false 时只打印，不发送）；
//   - 真实送礼的每日额度写入 state 目录下的文件账本，跨进程生效。

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/gift"
)

var errGiftUsage = errors.New("用法：fishctl gift send --room <房间> [--gift-id <ID>] [--num N]")

// giftCommand 是解析后的子命令参数；Num 为 -1 表示沿用 [gift] num。
type giftCommand struct {
	Action string
	Room   string
	GiftID string
	Num    int
}

type giftSender interface {
	Send(ctx context.Context, room string, giftID string, num int) (gift.Result, error)
}

type giftSenderFactory func(config.Gift, string) (giftSender, error)

// giftSendOutput 是给运维看的单行 JSON；dry-run 与真实发送用 dry_run/sent 区分。
type giftSendOutput struct {
	DryRun bool   `json:"dry_run"`
	Room   string `json:"room"`
	GiftID string `json:"gift_id"`
	Num    int    `json:"num"`
	Sent   bool   `json:"sent"`
	// PendingReview 表示结果未知：额度保留，需要人工核对是否真的送出。
	PendingReview bool   `json:"pending_review"`
	Message       string `json:"message"`
	Error         string `json:"error,omitempty"`
}

// runGift 解析参数、加载配置并执行；factory 仅测试替换。
func runGift(ctx context.Context, configPath, envPath string, args []string, out, stderr io.Writer) error {
	return runGiftWith(ctx, configPath, envPath, args, out, stderr, newGiftSender)
}

func runGiftWith(ctx context.Context, configPath, envPath string, args []string, out, stderr io.Writer, factory giftSenderFactory) error {
	spec, err := parseGiftArgs(args, stderr)
	if err != nil {
		return err
	}
	if spec.Action == "help" {
		return nil
	}
	// CLI 送礼不依赖直播采集；live 配置缺失不应挡住手动 dry-run/发送。
	cfg, err := config.LoadForCLI(configPath, envPath)
	if err != nil {
		return err
	}
	if !cfg.Gift.Enabled {
		return errors.New("送礼未启用：请先在 [gift] 设置 enabled=true")
	}
	giftID := cfg.Gift.GiftID
	if spec.GiftID != "" {
		giftID = spec.GiftID
	}
	num := cfg.Gift.Num
	if spec.Num > 0 {
		num = spec.Num
	}
	if factory == nil {
		return errors.New("送礼装配缺失")
	}
	sender, err := factory(cfg.Gift, giftLedgerPath(cfg))
	if err != nil {
		return err
	}
	result, sendErr := sender.Send(ctx, spec.Room, giftID, num)

	output := giftSendOutput{
		DryRun:        !cfg.Gift.Real,
		Room:          spec.Room,
		GiftID:        giftID,
		Num:           num,
		PendingReview: result.PendingReview,
	}
	switch {
	case sendErr != nil:
		output.Error = sendErr.Error()
	case !cfg.Gift.Real:
		output.Message = "dry-run：未发送（real=false）"
	case result.Sent:
		output.Sent = true
		output.Message = "已发送"
	default:
		output.Message = "发送结果未确认"
	}
	if err := json.NewEncoder(out).Encode(output); err != nil {
		return fmt.Errorf("输出送礼结果：%w", err)
	}
	if sendErr != nil {
		return fmt.Errorf("送礼失败：%w", sendErr)
	}
	return nil
}

// parseGiftArgs 只接受 send 子命令；--help 返回 help 动作。
func parseGiftArgs(args []string, stderr io.Writer) (giftCommand, error) {
	if len(args) == 0 || args[0] != "send" {
		return giftCommand{}, errGiftUsage
	}
	fs := flag.NewFlagSet("gift send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "用法：fishctl gift send --room <房间> [--gift-id <ID>] [--num N]")
	}
	room := fs.String("room", "", "房间号")
	giftID := fs.String("gift-id", "", "礼物ID（默认取 [gift] gift_id）")
	num := fs.Int("num", -1, "礼物数量（默认取 [gift] num）")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return giftCommand{Action: "help"}, nil
		}
		return giftCommand{}, errGiftUsage
	}
	if fs.NArg() != 0 {
		return giftCommand{}, errGiftUsage
	}
	roomText := strings.TrimSpace(*room)
	if roomText == "" {
		return giftCommand{}, errors.New("gift send 需要非空 --room 房间号")
	}
	if *num == 0 || *num < -1 {
		return giftCommand{}, errors.New("--num 必须为正整数")
	}
	return giftCommand{
		Action: "send",
		Room:   roomText,
		GiftID: strings.TrimSpace(*giftID),
		Num:    *num,
	}, nil
}

// giftOptions 只在配置层做映射；不读取凭据、不启动 Node。
func giftOptions(cfg config.Gift, ledgerPath string) (gift.Options, error) {
	quota, err := gift.NewFileQuota(ledgerPath)
	if err != nil {
		return gift.Options{}, err
	}
	return gift.Options{
		NodePath:     cfg.NodePath,
		ScriptPath:   cfg.ScriptPath,
		Cookie:       cfg.Cookie,
		CookieFile:   cfg.CookieFile,
		Timeout:      time.Duration(cfg.TimeoutMS) * time.Millisecond,
		DryRun:       !cfg.Real,
		DailyLimit:   cfg.DailyLimit,
		MaxPrice:     cfg.MaxPrice,
		MaxNum:       cfg.MaxNum,
		AllowedRooms: cfg.AllowedRooms,
		Quota:        quota,
	}, nil
}

func newGiftSender(cfg config.Gift, ledgerPath string) (giftSender, error) {
	options, err := giftOptions(cfg, ledgerPath)
	if err != nil {
		return nil, err
	}
	return gift.New(options)
}

// giftLedgerPath 把账本放在状态文件同目录，随 state 目录一起保留。
func giftLedgerPath(cfg config.Config) string {
	return filepath.Join(filepath.Dir(cfg.Runtime.StateFile), "gift-ledger.json")
}

var _ giftSender = (*gift.NodeSender)(nil)
