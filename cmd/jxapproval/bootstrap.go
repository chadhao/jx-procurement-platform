// Package main 装配 jxapproval 服务（单实例）。
//
// ★ 架构转向 ③（审批核心迁至我方 · 飞书三方审批）—— 本文件的装配纪律：
//
//	· 审批事件**不再订阅**：我方是唯一状态源（N10 / F1），再订阅飞书审批事件
//	  会与旧事件链一起把已推进的终态覆盖回 PENDING（R18/R23）；
//	· 旧「定时对账器 + 调度器」装配**已退役**（R23：旧对账"补拉"经 ingest 覆盖我方状态）；
//	· worker 事件链（inbox → worker → ingest）暂留作过渡，其写入者退役见 T02b。
//
// 守卫测试见 bootstrap_wiring_test.go —— 若有人把旧审批订阅 / 旧对账器接回来，测试必须转红。
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
	"github.com/chadhao/jx-procurement-platform/internal/objectstore"
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

// subscribeTargetCodes 启动时要订阅的事件源（显式枚举）。
//
// ★ ③ 口径（N10 / F1）：**审批事件不再订阅** —— 审批核心已迁至我方，我方是唯一状态源；
//
//	再订阅飞书审批事件会与旧链一起把已推进的终态覆盖回 PENDING（R18/R23）。
//	通讯录（部门/人员）事件的订阅在 docs/08 批次接入，故当前**显式为空集**：
//	这是刻意的空，不是遗漏。守卫测试会断言此集合不含任何审批事件键。
var subscribeTargetCodes = []string{}

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
		"ledger_type_count", maps.LedgerMappingCount(), "threshold_count", len(maps.Thresholds))
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

	// ---- ⑦ 订阅（③ 口径：不订阅审批事件，见 subscribeTargetCodes）----
	// ★ R23 退役：旧「定时对账器 + 调度器」的装配已在此**摘除** —— 只要它还挂着，
	//   每跑一次就经 reconcile.go 的补拉路径覆盖我方已推进状态（"对账"名义下的隐蔽覆盖）。
	//   本批次**不**装配 flow/approval/number（另行排期）。
	subscriber := sync.NewSubscriber(db, client, maps, metrics, logger)

	// ---- ⑧ 启动自检第 1 项：订阅（显式空集；通讯录事件待 docs/08 接入）----
	_, failed := subscriber.Subscribe(ctx, subscribeTargetCodes)
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
	// 附件对象存储（B39 / ADR-08）：主存 S3（手写 SigV4，零新增依赖）+ 异地备份 RustFS；
	// 未配 S3 时退回本地落盘；两者皆无则降级为"不缓存、每次回源"。
	attachStore, err := objectstore.Build(
		s3ConfigOrNil(env.S3Endpoint, env.S3Bucket, env.S3Region, env.S3AK, env.S3SK, env.S3PathStyle),
		s3ConfigOrNil(env.RustFSEndpoint, env.RustFSBucket, env.RustFSRegion, env.RustFSAK, env.RustFSSK, env.RustFSPathStyle),
		env.AttachDir, logger,
	)
	if err != nil {
		return err
	}
	switch {
	case attachStore == nil:
		logger.Warn("未配置对象存储（JX_S3_* / JX_ATTACH_DIR 均为空）：附件不做缓存，每次回源拉取")
	default:
		logger.Info("附件对象存储就绪", "kind", attachStore.Kind())
	}

	router := httpapi.NewRouter(httpapi.Deps{
		Env:        env,
		DB:         db,
		Log:        logger,
		Metrics:    metrics,
		Health:     health,
		Inbox:      inboxSvc,
		Worker:     wk,
		Subscriber: subscriber,
		// ★ Reconciler 不再装配（R23 退役）：Deps.Reconciler 保持零值 nil。
		//   /internal/sync/reconcile 路由属旧路径，其退役/改造随 httpapi 一并排期。
		Perm:    permLoader,
		Auth:    auth,
		Maps:    maps,
		WebUI:   webui.Handler(),
		Version: version,
		Feishu:  client,
		Objects: attachStore,
	})
	if env.IsDev() {
		logger.Warn("开发模式已开启：已注册 POST /internal/dev/inject-event（仅本地验证用）")
	}

	// ---- ⑪ 运行：长连接 + worker + HTTP（★ 定时对账已退役，见 ⑦ / R23）----
	go func() {
		if err := longconn.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("长连接异常退出", "error", err.Error())
		}
	}()
	wk.Start(ctx, workerPoolSize)

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

// s3ConfigOrNil 端点/AK/SK 三者齐备才返回配置，否则返回 nil（＝该层不启用）。
//
// ★ 为什么用"齐备才启用"而不是逐项降级：只配一半的 S3 会在**运行时报错**，
// 而那时用户看到的是"附件下载失败"，排查成本高。宁可启动日志就说明"未启用"。
func s3ConfigOrNil(endpoint, bucket, region, ak, sk string, pathStyle bool) *objectstore.S3Config {
	if endpoint == "" || bucket == "" || ak == "" || sk == "" {
		return nil
	}
	return &objectstore.S3Config{
		Endpoint: endpoint, Bucket: bucket, Region: region,
		AccessKey: ak, SecretKey: sk, PathStyle: pathStyle,
	}
}
