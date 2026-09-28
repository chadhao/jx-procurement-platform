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
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/approval"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/httpapi"
	"github.com/chadhao/jx-procurement-platform/internal/inbox"
	"github.com/chadhao/jx-procurement-platform/internal/number"
	"github.com/chadhao/jx-procurement-platform/internal/objectstore"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/orgsync"
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

	// approvalRepairInterval 派生式修复循环（#69 ②）的扫查间隔。
	// ★ 依据：#69 ①「落盘即 200」后，推进失败**不再由 HTTP 重试驱动**（飞书收到 200 不再重试）
	//   → 必须由本地循环兜底。审批推进非高频、且重驱动走幂等的 `act`，30s 的延迟对用户口径
	//   「先落盘…然后**慢慢跑业务**」完全可接受；启动先跑一次做 catch-up（捞回重启前卡住的行）。
	approvalRepairInterval = 30 * time.Second
)

// subscribeTargetCodes 启动时要订阅的事件源（显式枚举）。
//
// ★ ③ 口径（N10 / F1）：**审批事件不再订阅** —— 审批核心已迁至我方，我方是唯一状态源；
//
//	再订阅飞书审批事件会与旧链一起把已推进的终态覆盖回 PENDING（R18/R23）。
//	通讯录（部门/人员）事件的订阅**不走本清单**（本清单是 approval_code 订阅接口的
//	目标），而是长连接 `internal/platform/feishu/longconn.go` 的 `sinkEventTypes`
//	（批次二已接入 6 个 `*_v3` 事件）。守卫测试会断言此集合不含任何审批事件键。
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

	// ---- ④′ 启动自检：三方审批定义装载检查（docs/16 §2-C 通道②；不自动建定义）----
	// ★ 定义未装载的后果是「静默无数据」：提交侧 S7 → 40901，回调侧实例存在性关被拒。
	//   此处只 warn + 置 health 状态位（不自动建，避免启动期网络依赖飞书）；
	//   生产装载入口＝POST /api/admin/approval/defs/sync（requireSysAdmin）。
	if n, err := db.CountApprovalDefs(ctx); err != nil {
		logger.Warn("启动自检：读取 t_approval_def 行数失败", "error", err.Error())
	} else if n == 0 {
		logger.Warn("★ 启动自检：t_approval_def 为空（三方定义未装载，回调/提交将 40901）" +
			"——请以系统管理员调用 POST /api/admin/approval/defs/sync 装载")
		health.SetApprovalDefs(false)
	} else {
		logger.Info("启动自检：三方审批定义已装载", "def_count", n)
		health.SetApprovalDefs(true)
	}

	// ---- ⑤ 飞书通道适配层 + inbox（长连接 sink）----
	// 开发模式且未配置凭据时，改用内存 dev 客户端：使 /internal/dev/inject-event 可端到端落库（不依赖飞书）。
	feishuHTTP := feishu.NewHTTPClient(env.AppID, env.AppSecret, logger, metrics)
	var client feishu.Client = feishuHTTP
	if env.IsDev() && (env.AppID == "" || env.AppSecret == "") {
		client = feishu.NewDevClient()
		logger.Warn("开发模式且未配置飞书凭据：使用内存 dev 客户端（注入事件可端到端落库）")
	}
	// ★ 身份转换端口（docs/16 §2-A-3）：回调官方发 user_id（租户内域），须换 open_id 后
	//   再进准入鉴权。生产/dev 一律挂真实 HTTP 实现（转换失败 ⇒ 回调侧可见拒绝 40000）；
	//   dev 内存客户端不支持该端点，属可接受降级（dev 无真实回调流量）。
	contact := feishu.ContactClient(feishuHTTP)
	inboxSvc := inbox.NewService(db, metrics, logger)
	longconn := feishu.NewLongConn(env.AppID, env.AppSecret, inboxSvc, logger)

	// ---- ⑤′ 通讯录镜像全量同步（docs/08 实施批次一）----
	// ★ 与业务侧共享同一 tenant_access_token 缓存（feishu.TenantAccessToken）；
	//   token 未配置凭据时拉取会**可见失败**（不静默、不伪造数据），手动端点可重试。
	//   触发：启动异步 RunIfStale（超 JX_ORG_SYNC_STALE_HOURS 阈值才拉，不阻塞启动、
	//   不进就绪门禁）+ POST /internal/org/sync 手动强制。
	orgFetcher := orgsync.NewFeishuFetcher(feishuHTTP, feishu.DefaultBaseURL, logger)
	orgRunner := orgsync.NewRunner(orgFetcher, db, metrics, health, logger,
		time.Duration(env.OrgSyncStaleHours)*time.Hour)
	if env.AppID == "" || env.AppSecret == "" {
		logger.Warn("★ 未配置飞书凭据：通讯录镜像不会同步（/api/org/* 将为空清单）——" +
			"配置 JX_APP_ID/JX_APP_SECRET 后以 POST /internal/org/sync 手动触发")
	}
	// ★ 批次二（docs/08 §4.6/§4.7）：通讯录事件增量 + 定期对账兜底。
	//   事件增量：长连接把 contact.* 事件交 inbox（幂等落盘 + 分流 org_sync 作业），
	//   worker 消费时经 EventHandler 增量落镜像（与审批事件链完全隔离）。
	//   定期对账：距上次成功全量超 JX_ORG_RECONCILE_HOURS（默认 168h=每周）⇒
	//   RunFull(trigger="reconcile")，以飞书为准自愈漂移；手动入口复用 POST /internal/org/sync。
	orgEvents := orgsync.NewEventHandler(db, orgFetcher, metrics, health, logger)
	orgReconcileAfter := time.Duration(env.OrgReconcileHours) * time.Hour

	// ---- ⑥ worker + ingestor ----
	ingestor := worker.NewIngestor(db, maps, logger)
	wk := worker.NewWorker(db, client, ingestor, metrics, logger).
		WithOrgEventHandler(orgEvents) // ★ 批次二：org_sync 作业 → 通讯录事件增量落镜像

	// ---- ⑥′ 审批核心上电（架构转向 ③ · T03b/T04b）----
	//
	// ★ R09 上电：`flow` / `approval` / `number` 三包**首次进入生产装配**（此前零生产装配、
	//   仅测试可达 → 所有正确性都跑在死代码路径上）。
	//
	// 三方审批定义客户端（ExternalApprovalClient）、出方向推送客户端（PushClient）、
	// 对账 check 客户端（ExtCheckClient）：生产＝同一个 `*HTTPClient`；开发（无凭据）＝内存替身，
	// 使 `DEV_MODE` 端到端可用（不依赖飞书凭据）。
	// ★ 第 3 批（docs/16 §2-F）：同惯例追加两个端口 ——
	//   ① messageClient（审批 Bot 消息更新，message/update；请求体字段待 V-2 实测定稿）；
	//   ② notifySender（待办通知发送，message/send；flow.Sender 实现）。
	var (
		extClient     feishu.ExternalApprovalClient
		pushClient    feishu.PushClient
		checkClient   feishu.ExtCheckClient
		messageClient feishu.MessageUpdateClient
		notifySender  flow.Sender
	)
	if hc, ok := client.(*feishu.HTTPClient); ok {
		extClient, pushClient, checkClient = hc, hc, hc
		messageClient = hc
		notifySender = feishu.NewNotifySender(db, hc, env.CallbackDomain, logger)
		if strings.TrimSpace(env.CallbackDomain) == "" {
			// ★ 通知的「查看详情」四 URL 缺一即 60001（实测）；无域名配置宁可可见失败
			//   （t_notify_log.FAILED 留痕、漏发可检出），绝不编造 URL。
			logger.Warn("JX_CALLBACK_DOMAIN 未配置：待办通知的「查看详情」链接无法构造，" +
				"通知发送将可见失败（t_notify_log 记 FAILED；漏发可检出）")
		}
	} else {
		extClient, pushClient, checkClient = feishu.NewFakeExternalApprovalClient(),
			feishu.NewFakePushClient(), feishu.NewFakeExtCheckClient()
		messageClient = feishu.NewFakeMessageClient()
		notifySender = feishu.NewFakeNotifySender()
	}

	// 定义注册表：装配即用；生产装载入口＝POST /api/admin/approval/defs/sync（docs/16 §2-C），
	// 清单源＝t_config_mapping(map_kind='approval_code')，启动自检见 ④′。
	approvalDefs := approval.NewRegistry(db, extClient, logger)
	// 出方向推送服务（external_instances）。★ detailBase（JX_CALLBACK_DOMAIN）＝
	// links 的唯一来源（实例级与 task_list[*].links 均必填，2026-09-28 实测 99992402；
	// 未配置 ⇒ 推送可见失败，不编造 URL）。
	pusher := feishu.NewPusher(db, pushClient, env.CallbackDomain, logger)
	// 审批领域服务（唯一状态源）：注入台账映射（finalize 落账）与日志。
	flowSvc := flow.NewWithConfig(db, env.AppID, maps, logger)

	// 事件订阅者：① 通知（第 3 批接通：生产＝feishu.NotifySender（message/send，实测契约），
	// 开发＝Fake；两阶段 EXPECTED→SENT/FAILED 落盘机制不变）② 出方向推送。
	// ★ 二者均在**事务提交后**由 flow.emit 分发（04a §2.5），失败不影响主流程。
	// ★ notifySender 以 flow.Sender 类型承接（上方 var 块）—— NotifySender/FakeNotifySender
	//   的方法集与 flow.Sender 的编译期一致性由该赋值保证（feishu 包不反向依赖 flow）。
	flowSvc.Subscribe(flow.NewNotifier(db, notifySender, logger))
	flowSvc.Subscribe(&flowPushSubscriber{pusher: pusher, log: logger})
	// ★ 本批核心（2026-09-28 实测定稿）：审批 Bot 卡片「推进成功后主动刷新」。
	//   实测：回调处理成功（accepted=true、状态机推进）后平台**并未自动刷新卡片**
	//   （卡片仍带「同意/拒绝」两键）⇒ 不依赖平台自动更新，订阅 TASK_APPROVED /
	//   TASK_REJECTED 事件主动调 message/update（body={"message_id","status"}，实测 code=0）。
	//   ★ 失败只记日志（emit 已隔离订阅者错误/panic），**绝不影响回调的 200**（#69 落盘即 200）；
	//   ★ message_id 由 NotifySender.Send 发送时落 t_notify_log（0014 列），查不到 ⇒ 跳过不报错。
	flowSvc.Subscribe(&flowCardRefreshSubscriber{
		refresher: feishu.NewCardRefresher(db, messageClient, logger),
	})

	// 回调异步推进端口：`flow.HandleCallback` 同步路径只落 op_log + 调此端口。
	// ★ 本批以**同步适配器**落地（真正"worker 消费 op_log"的异步队列另排期；见本批报告）。
	flowSvc.SetCallbackAdvancer(func(ctx context.Context, req flow.CallbackRequest) error {
		switch strings.ToUpper(strings.TrimSpace(req.OpType)) {
		case flow.OpApprove:
			return flowSvc.Approve(ctx, req.BizNo, req.TaskID, req.OperatorOpenID, req.Reason)
		case flow.OpReject:
			return flowSvc.Reject(ctx, req.BizNo, req.TaskID, req.OperatorOpenID, req.Reason)
		default:
			return nil
		}
	})

	// 新审批对账器（T03）：独立于 ingest、不复用旧 Reconciler；对 check 的 diff 判方向后重推。
	// ★ 本批修复（2026-09-28 实测 99992402 "instances is required"）：check 入参必须带
	//   instances[]（每项 update_time + tasks[]），数据源＝本地 t_instance / t_flow_task，
	//   字段表示与推送侧一致（feishu.BuildCheckInstance 统一组装）。
	approvalRec := sync.NewApprovalReconciler(db, sync.ExtSyncCheckerFunc(
		func(ctx context.Context, code string) ([]sync.RemoteInstanceState, error) {
			insts, err := db.ListInstancesByApprovalCode(ctx, code)
			if err != nil {
				return nil, err
			}
			checks := make([]feishu.ExternalCheckInstance, 0, len(insts))
			for i := range insts {
				tasks, err := db.ListFlowTasks(ctx, insts[i].BizNo)
				if err != nil {
					return nil, err
				}
				checks = append(checks, feishu.BuildCheckInstance(&insts[i], tasks))
			}
			sts, err := checkClient.CheckExternalInstances(ctx, code, checks)
			if err != nil {
				return nil, err
			}
			out := make([]sync.RemoteInstanceState, 0, len(sts))
			for _, s := range sts {
				out = append(out, sync.RemoteInstanceState{
					InstanceID: s.InstanceID, UpdateTime: s.UpdateTime, Status: s.Status,
				})
			}
			return out, nil
		}), pusher, maps, metrics, logger)

	// number 装配自检（R09）：单号周期错会**静默撞号**（04a §3.3）→ 启动即把当期 YYMM 打进日志。
	logger.Info("单号格式自检（number 装配）", "yymm", number.YYMM(time.Now()), "sample_key", number.NumberKey("PR"))

	// ---- ⑦ 订阅（③ 口径：不订阅审批事件，见 subscribeTargetCodes）----
	// ★ R23 退役：旧「定时对账器 + 调度器」的装配已在此**摘除** —— 只要它还挂着，
	//   每跑一次就经 reconcile.go 的补拉路径覆盖我方已推进状态（"对账"名义下的隐蔽覆盖）。
	//   本批次**不**装配 flow/approval/number（另行排期）。
	subscriber := sync.NewSubscriber(db, client, maps, metrics, logger)

	// ---- ⑧ 启动自检第 1 项：订阅（显式空集；通讯录事件走长连接 sinkEventTypes，批次二已接入）----
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
		// ★ 免登凭据/回调地址从配置注入（官方 token 请求体必填 client_id / client_secret，
		//   redirect_uri 与授权时一致）；redirect_uri 缺省＝JX_CALLBACK_DOMAIN + 回调路径。
		oauth = feishu.NewOAuthClient(env.AppID, env.AppSecret, env.FeishuRedirectURI(), logger)
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
		// ★ 审批核心（转向 ③）：页面两键 / 四操作 / 待办 / 入站回调 / 对账。
		Flow:               flowSvc,
		ApprovalDefs:       approvalDefs,
		ApprovalReconciler: approvalRec,
		Perm:               permLoader,
		Auth:               auth,
		Maps:               maps,
		WebUI:              webui.Handler(),
		Version:            version,
		Feishu:             client,
		Contact:            contact,
		OrgSync:            orgRunner,
		Objects:            attachStore,
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

	// ★ 审批对账循环（T03）：独立 goroutine；未配置外部 check 端口时该方法自行告警并空跑退出。
	go approvalRec.Run(ctx)

	// ★ 通讯录镜像启动全量（docs/08 §4.5）：**异步**——不阻塞启动、失败不进就绪门禁；
	//   距上次成功同步超阈值（JX_ORG_SYNC_STALE_HOURS，默认 24h）才拉。
	go func() {
		if err := orgRunner.RunIfStale(ctx); err != nil {
			// 失败已在 Runner 内落 failed 流水/状态/计数/健康段；此处只补一条进程级告警。
			logger.Error("启动通讯录全量同步失败（不影响就绪；可用 POST /internal/org/sync 重试）",
				"error", err.Error())
		}
	}()

	// ★ 通讯录定期对账循环（docs/08 §4.7 批次二）：独立 goroutine；每小时检查一次，
	//   距上次成功全量超 JX_ORG_RECONCILE_HOURS（默认 168h）才跑——兜底事件丢投漂移。
	go orgRunner.RunReconcileLoop(ctx, orgReconcileAfter, time.Hour)

	// ★ 派生式修复循环（#69 ②）：独立 goroutine；兜底「已落盘、未推进」的回调
	//   （#69 ① 落盘即 200 后，推进失败不再由 HTTP 重试驱动，见 internal/flow/repair.go）。
	//   ★ 第 3 批（docs/16 §2-F-③）：对「最终失败」行用落盘 message_id 调 message/update
	//   标注飞书卡片（message_id 为空不发请求；失败只记日志不回滚业务）。
	repairFeedback := feishu.NewRepairCardFeedback(messageClient, logger)
	go approvalRepairLoop(ctx, flowSvc, repairFeedback, logger)

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

// approvalRepairLoop 周期性地把「已落盘、未推进」的回调**重驱动**到位（#69 ②）。
//
// ★ 启动即跑一次（catch-up）：把进程重启前卡住的行捞回来；之后每 `approvalRepairInterval` 扫一次。
// ★ 与 worker / 长连接 / 对账等并列的独立 goroutine；`ctx` 取消即退出。
// ★ 逻辑委托 `flow.Service.RepairPendingApprovals`（查 = store 只读扫描；写 = 幂等 `act`）。
// ★ 第 3 批（docs/16 §2-F-③）：每轮对「最终失败」行经 repairFeedback 调 message/update
//
//	标注卡片 —— message_id 为空时反馈器拦截（无卡可更新，不发同步请求）；
//	卡片更新失败只记日志、不回滚业务（与「落盘即 200」纪律一致）。
func approvalRepairLoop(ctx context.Context, svc *flow.Service, feedback *feishu.RepairCardFeedback, log *slog.Logger) {
	runOnce := func() {
		rep, err := svc.RepairPendingApprovals(ctx)
		if err != nil {
			log.Error("审批修复循环：扫描待修复行失败（下一轮重试）", "error", err.Error())
			return
		}
		if rep.Scanned == 0 {
			return
		}
		log.Info("审批修复循环：重驱动已落盘未推进的回调",
			"scanned", rep.Scanned, "repaired", rep.Repaired,
			"skipped", rep.Skipped, "failed", rep.Failed)
		for _, e := range rep.Errors {
			log.Warn("审批修复循环：单行重驱动失败（不影响其余行）", "detail", e)
		}
		// ★ F-③：最终失败行的卡片反馈（反馈器内部做空 message_id 拦截与防重）。
		for _, f := range rep.Failures {
			feedback.OnRepairFailure(ctx, f.Op.BizNo, f.Op.TaskID, f.Op.Round, f.Op.MessageID, f.Err)
		}
	}
	runOnce() // catch-up：先捞一次重启前卡住的行
	ticker := time.NewTicker(approvalRepairInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

// flowCardRefreshSubscriber 流程事件订阅者：审批推进成功后**主动刷新**飞书待办卡片
// （internal/platform/feishu.CardRefresher；2026-09-28 实测平台不自动刷新卡片）。
//
// ★ 触发点＝flow 状态机推进成功（act 事务提交后 emit）——覆盖回调推进 / 修复循环重驱动 /
// 页面操作三条路径，恰为「回调处理成功且推进成功」（docs/16 本批定案）。
// ★ status 与审批终态一致（实测 "APPROVED" 成功）：TASK_APPROVED → APPROVED、
// TASK_REJECTED → REJECTED；其余事件（提交/转交/回退/撤回等）不刷卡片。
// ★ 失败只记日志（CardRefresher 内部吞错 + emit 隔离），绝不影响回调的 200（#69）。
type flowCardRefreshSubscriber struct {
	refresher *feishu.CardRefresher
}

// OnFlowEvent 实现 flow.Subscriber（仅两任务终态事件触发刷新，其余忽略）。
func (s *flowCardRefreshSubscriber) OnFlowEvent(ctx context.Context, ev flow.FlowEvent) {
	if s == nil || s.refresher == nil {
		return
	}
	switch ev.Type {
	case flow.EventTaskApproved:
		s.refresher.Refresh(ctx, ev.BizNo, ev.TaskID, "APPROVED")
	case flow.EventTaskRejected:
		s.refresher.Refresh(ctx, ev.BizNo, ev.TaskID, "REJECTED")
	}
}

// flowPushSubscriber 流程事件订阅者：每次状态变更后主动**重推**飞书 `external_instances`。
//
// ★ 为什么订阅推送（而非在各写路径各自内联调用）：状态迁移的唯一出口是 `flow`（04a §2.5），
//
//	由事件统一分发可保证"每次操作后都重推"，避免某条路径漏推导致飞书待办**静默不更新**（S2）。
//
// ★ 失败只记日志（审批已落库；推送可重试）——订阅者失败不得影响主流程（flow.emit 约束）。
type flowPushSubscriber struct {
	pusher *feishu.Pusher
	log    *slog.Logger
}

// OnFlowEvent 实现 flow.Subscriber（推送当前实例快照；Pusher 内部按 update_time 幂等）。
func (p *flowPushSubscriber) OnFlowEvent(ctx context.Context, ev flow.FlowEvent) {
	if strings.TrimSpace(ev.BizNo) == "" || p.pusher == nil {
		return
	}
	if _, err := p.pusher.Push(ctx, ev.BizNo); err != nil {
		p.log.Error("流程事件推送失败（审批已落库，推送可重试）",
			"biz_no", ev.BizNo, "event", string(ev.Type), "error", err.Error())
	}
}
