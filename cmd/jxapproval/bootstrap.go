package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/httpapi"
	"github.com/chadhao/jx-procurement-platform/internal/inbox"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/seed"
	"github.com/chadhao/jx-procurement-platform/internal/singlelock"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/sync"
	"github.com/chadhao/jx-procurement-platform/internal/webui"
	"github.com/chadhao/jx-procurement-platform/internal/worker"
)

const (
	workerPoolSize = 4
	sessionTTL     = 8 * time.Hour
)

// run 完成依赖装配与生命周期管理。
func run(version string) error {
	env, err := config.LoadEnv()
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}

	logger := observ.WithComponent(
		observ.NewLogger(os.Getenv("JX_LOG_LEVEL"), os.Stdout), "app")
	logger.Info("启动中", "version", version, "env", env.RunEnv, "dev_mode", env.DevMode)

	metrics := observ.NewMetrics()
	health := observ.NewHealth(version)

	// ---- ① 单实例锁（禁止多副本，ADR-01 / TC-04）----
	lock := singlelock.New(env.LockPath)
	if err := lock.Acquire(); err != nil {
		logger.Error("获取单实例锁失败，拒绝启动（长连接集群模式不广播，禁止多副本）",
			"error", err.Error(), "lock_path", env.LockPath)
		return fmt.Errorf("单实例自检失败: %w", err)
	}
	defer func() { _ = lock.Release() }()
	health.SetSingleInstance(true)
	logger.Info("单实例锁已获取", "lock_path", env.LockPath)

	// ---- ② 打开数据库 + 迁移 ----
	db, err := store.Open(env.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := store.Migrate(ctx, db); err != nil {
		return err
	}
	logger.Info("数据库迁移完成", "db_path", env.DBPath)

	// ---- ②′ 播种 Q3 默认权限口径（幂等：INSERT OR IGNORE，不覆盖管理员已改规则）----
	if n, err := seed.SeedQ3Defaults(ctx, db); err != nil {
		return fmt.Errorf("播种 Q3 默认权限口径失败: %w", err)
	} else if n > 0 {
		logger.Info("Q3 默认权限口径已播种", "inserted", n)
	}

	// ---- ③ 启动自检：数据库可写（失败拒绝启动，避免半可用态，TC-28）----
	if err := db.WritableProbe(ctx); err != nil {
		health.SetDBWritable(false)
		logger.Error("启动自检失败：数据库不可写，拒绝启动", "error", err.Error())
		return fmt.Errorf("数据库可写自检失败: %w", err)
	}
	health.SetDBWritable(true)

	// ---- ④ 配置映射装载（approval_code / field_id / ledger_type / threshold）----
	maps, err := config.LoadMaps(ctx, db)
	if err != nil {
		return fmt.Errorf("装载配置映射失败: %w", err)
	}
	logger.Info("配置映射已装载", "approval_code_count", maps.Approval.Len(),
		"ledger_type_count", len(maps.Ledger), "threshold_count", len(maps.Thresholds))
	if maps.Approval.Len() == 0 {
		logger.Warn("★ 配置表中无 approval_code 映射（PRD Q1 待确认）——订阅与事件落库将无数据，属预期（占位 TODO(Q1)）")
	}
	if missing := env.MissingSecrets(); len(missing) > 0 {
		logger.Warn("以下敏感环境变量尚未配置（开发模式可忽略）", "missing", missing)
	}

	// ---- ⑤ 飞书通道适配层 + inbox（长连接 sink）----
	// 开发模式且未配置凭据时，改用内存 dev 客户端：使 /internal/dev/inject-event 可端到端落库（不依赖飞书）。
	var client feishu.Client = feishu.NewHTTPClient(env.AppID, env.AppSecret, logger, metrics)
	if env.IsDev() && (env.AppID == "" || env.AppSecret == "") {
		client = feishu.NewDevClient()
		logger.Warn("开发模式且未配置飞书凭据：使用内存 dev 客户端（注入事件可端到端落库）")
	}
	inboxSvc := inbox.NewService(db, metrics, logger)
	longconn := feishu.NewLongConn(env.AppID, env.AppSecret, inboxSvc, logger)

	// ---- ⑥ worker + ingestor ----
	ingestor := worker.NewIngestor(db, maps, logger)
	wk := worker.NewWorker(db, client, ingestor, metrics, logger)

	// ---- ⑦ 订阅 + 对账 ----
	subscriber := sync.NewSubscriber(db, client, maps, metrics, logger)
	reconciler := sync.NewReconciler(db, client, ingestor, maps, metrics, logger)
	scheduler := sync.NewScheduler(reconciler, env.ReconcileInterval, logger)

	// ---- ⑧ 启动自检第 1 项：必须先订阅（否则静默无数据，FR-M0-03/04）----
	_, failed := subscriber.SubscribeAll(ctx)
	if env.AppID == "" || env.AppSecret == "" {
		logger.Warn("未配置飞书凭据，订阅结果为预期失败（开发模式）；配置凭据后须重订阅")
	}
	health.SetSubscribe(len(failed) == 0, failed)
	if len(failed) > 0 {
		logger.Error("★ 启动自检：部分 approval_code 订阅失败（穷尽告警）", "failed", failed)
	}

	// ---- ⑨ 权限 / 会话 / 免登 ----
	permLoader := permission.NewLoader(db)
	sessions := access.NewStore(env.SessionKey, sessionTTL)
	var oauth feishu.OAuthExchange
	if env.AppID != "" && env.AppSecret != "" {
		oauth = feishu.NewOAuthClient(logger)
	}
	auth := access.NewAuthenticator(db, sessions, oauth, env.IsDev(), logger)

	// ---- ⑩ HTTP 路由 ----
	router := httpapi.NewRouter(httpapi.Deps{
		Env:        env,
		DB:         db,
		Log:        logger,
		Metrics:    metrics,
		Health:     health,
		Inbox:      inboxSvc,
		Worker:     wk,
		Subscriber: subscriber,
		Reconciler: reconciler,
		Perm:       permLoader,
		Auth:       auth,
		Maps:       maps,
		WebUI:      webui.Handler(),
		Version:    version,
	})
	if env.IsDev() {
		logger.Warn("开发模式已开启：已注册 POST /internal/dev/inject-event（仅本地验证用）")
	}

	// ---- ⑪ 运行：长连接 + worker + 定时对账 + HTTP ----
	go func() {
		if err := longconn.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("长连接异常退出", "error", err.Error())
		}
	}()
	wk.Start(ctx, workerPoolSize)
	go scheduler.Run(ctx)

	// 长连接状态回填健康检查。
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				health.SetLongConn(longconn.Connected())
			}
		}
	}()

	srv := &http.Server{Addr: env.ListenAddr, Handler: router}
	go func() {
		logger.Info("HTTP 监听中", "addr", env.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP 服务异常", "error", err.Error())
			cancel()
		}
	}()

	// ---- ⑫ 优雅退出：先停长连接，再排空 worker，最后关 HTTP ----
	<-ctx.Done()
	logger.Info("收到退出信号，开始优雅退出")
	longconn.Stop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("HTTP 关闭超时", "error", err.Error())
	}
	wk.Stop()
	logger.Info("已优雅退出")
	return nil
}
