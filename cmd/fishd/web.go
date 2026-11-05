package main

// 本文件装配 Web 面板：只在 [web].enabled 时启动，失败只记日志不阻塞主流程。

import (
	"context"
	"sync"
	"time"

	"RaiseMyFish/internal/account"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/web"
	"go.uber.org/zap"
)

// startWebPanel 启动面板；返回的 stop 会等待服务退出（未启用时是空函数）。
func startWebPanel(ctx context.Context, cfg config.Config, resources *infra.Resources, runtime knowledgeAssembly, accounts *account.Service, control web.RuntimeController, logs web.LogSource, log *zap.Logger) func() {
	if !cfg.Web.Enabled {
		return func() {}
	}
	repo, err := database.NewWebRepo(resources.DB)
	if err != nil {
		log.Warn("面板未启动：数据源装配失败", zap.String("action", "web_setup"), zap.Error(err))
		return func() {}
	}
	var knowledgeInfo web.KnowledgeInfo
	if runtime.Base != nil {
		knowledgeInfo = func() (int, string) {
			info, err := runtime.Base.SnapshotInfo()
			if err != nil {
				log.Warn("读取知识快照状态失败", zap.String("action", "web_knowledge"), zap.Error(err))
				return 0, ""
			}
			return info.Entries, info.Revision
		}
	}
	srv, err := web.New(web.Options{Addr: cfg.Web.Addr, Repo: repo, Log: log, KnowledgeInfo: knowledgeInfo, Accounts: accounts, Control: control, Logs: logs})
	if err != nil {
		log.Warn("面板未启动：参数不合法", zap.String("action", "web_setup"), zap.Error(err))
		return func() {}
	}
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := srv.Run(runCtx); err != nil {
			log.Warn("面板服务退出", zap.String("action", "web_exit"), zap.Error(err))
		}
	}()
	return func() {
		cancel()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
		}
	}
}
