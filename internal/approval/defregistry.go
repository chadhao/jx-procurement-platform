// Package approval 承载审批核心（架构转向 ③）的应用服务。
//
// 本文件实现**三方审批定义注册表**（04a §13 T01）：把我方 11 类单据类型的三方审批定义
// 幂等地注册到飞书（external_approvals），并在本地 `t_approval_def` 留有映射。
//
// ★ 纪律：本包**不直接**发起飞书调用，一律经 feishu.ExternalApprovalClient 接口
// （边界纪律：只有 internal/platform/feishu 触及飞书 HTTP/SDK）。
package approval

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// DefInput 单个单据类型的三方审批定义入参（来自本地配置）。
type DefInput struct {
	DocType          string // 我方单据类型（BA/PR/SA/RFQ/BJ/SS/CT/PC/GR/QC/SUB）
	ApprovalCode     string // 三方审批定义码（稳定标识；本地配置给出）
	Name             string // 审批名称（飞书侧展示）
	GroupName        string // 分组
	VisibleScopeJSON string // 可见范围（原样 JSON）
	CreateLinkPC     string // 发起页 PC（指向我方页面）
	CreateLinkMobile string // 发起页 Mobile
	SupportPC        bool
	SupportMobile    bool
	CallbackURL      string // action_callback_url
	CallbackToken    string // action_callback_token
	CallbackKey      string // action_callback_key
	FormSummaryJSON  string // 飞书列表摘要（3 条）配置
}

// validate 校验注册入参（缺失即**显式失败**，不静默跳过）。
func (d DefInput) validate() error {
	if strings.TrimSpace(d.DocType) == "" {
		return errors.New("approval: doc_type 为空")
	}
	if strings.TrimSpace(d.ApprovalCode) == "" {
		return fmt.Errorf("approval: doc_type=%s 的 approval_code 为空", d.DocType)
	}
	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf("approval: doc_type=%s 的 name 为空", d.DocType)
	}
	return nil
}

// SyncItem 单个定义同步的结果（供汇总与告警）。
type SyncItem struct {
	DocType      string `json:"doc_type"`
	ApprovalCode string `json:"approval_code"`
	Created      bool   `json:"created"` // true=本地首建 / false=更新
	Err          string `json:"error,omitempty"`
}

// SyncResult 一次全量同步的结果。
type SyncResult struct {
	Items  []SyncItem `json:"items"`
	Failed int        `json:"failed"`
}

// ErrSyncFailed 存在定义同步失败（错误必须可见，不得静默）。
var ErrSyncFailed = errors.New("approval: 存在定义同步失败")

// Registry 定义注册/更新服务。
type Registry struct {
	db     *store.DB
	client feishu.ExternalApprovalClient
	log    *slog.Logger
}

// NewRegistry 构造注册表服务。
func NewRegistry(db *store.DB, client feishu.ExternalApprovalClient, log *slog.Logger) *Registry {
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &Registry{db: db, client: client, log: observ.WithComponent(log, "approval.def")}
}

// Register 幂等注册/更新**单个**定义。
//
// 流程：校验 → 调飞书 upsert（approval_code 命中即更新、未命中即新建）→ 落本地注册表。
// ★ 幂等：本地以 approval_code 为唯一键，**重复注册＝更新，不产生第二条定义**。
// ★ 失败可见：飞书调用失败 → 返回错误 + 记日志，**不落本地行**（不制造「本地有、飞书无」的两处真相）。
func (r *Registry) Register(ctx context.Context, in DefInput) (SyncItem, error) {
	item := SyncItem{DocType: in.DocType, ApprovalCode: in.ApprovalCode}
	if err := in.validate(); err != nil {
		r.log.Error("定义注册参数非法", "doc_type", in.DocType, "error", err.Error())
		item.Err = err.Error()
		return item, err
	}

	// ① 本地是否已存在（决定 def_version 递增）——按 approval_code 判定，避免"两处真相"。
	var (
		created = true
		nextVer = 1
	)
	if prev, err := r.db.GetApprovalDef(ctx, in.ApprovalCode); err == nil {
		created = false
		nextVer = prev.DefVersion + 1
	} else if !errors.Is(err, store.ErrNotFound) {
		r.log.Error("读取本地定义失败", "approval_code", in.ApprovalCode, "error", err.Error())
		item.Err = err.Error()
		return item, err
	}

	// ② 调飞书（唯一入站点在适配层）。失败即返回，绝不静默。
	res, err := r.client.UpsertExternalApproval(ctx, toExternalDef(in))
	if err != nil {
		r.log.Error("飞书三方审批定义注册失败",
			"doc_type", in.DocType, "approval_code", in.ApprovalCode, "error", err.Error())
		item.Err = err.Error()
		return item, fmt.Errorf("approval: 注册定义 %s 失败: %w", in.DocType, err)
	}
	code := res.ApprovalCode
	if strings.TrimSpace(code) == "" {
		code = in.ApprovalCode
	}

	// ③ 落本地注册表（upsert，重复注册=更新）。
	def := &store.ApprovalDef{
		ApprovalCode:     code,
		DocType:          in.DocType,
		Name:             in.Name,
		GroupName:        in.GroupName,
		VisibleScopeJSON: in.VisibleScopeJSON,
		CreateLinkPC:     in.CreateLinkPC,
		CreateLinkMobile: in.CreateLinkMobile,
		CallbackURL:      in.CallbackURL,
		CallbackToken:    in.CallbackToken,
		CallbackKey:      in.CallbackKey,
		FormSummaryJSON:  in.FormSummaryJSON,
		DefVersion:       nextVer,
	}
	if err := r.db.UpsertApprovalDef(ctx, def); err != nil {
		// 仅当**本地写失败**时才可能出现「飞书已建、本地无」——必须告警，不得静默。
		r.log.Error("三方定义已建但本地注册表写入失败（对账不平风险）",
			"doc_type", in.DocType, "approval_code", code, "error", err.Error())
		item.Err = err.Error()
		return item, fmt.Errorf("approval: 定义 %s 本地登记失败: %w", in.DocType, err)
	}

	item.ApprovalCode = code
	item.Created = created
	r.log.Info("三方审批定义已注册",
		"doc_type", in.DocType, "approval_code", code, "created", created,
		"def_version", nextVer, "feishu_log_id", res.FeishuLogID)
	return item, nil
}

// Sync 同步**一批**定义（按本地配置把各类定义注册到飞书）。
//
// ★ 逐条执行、逐条记录：单条失败**不阻断**其余（避免一处错误让其余定义建不起来），
//
//	但只要有失败即返回 ErrSyncFailed 并把失败明细带出——**失败必须可见**（04a §10 S7）。
func (r *Registry) Sync(ctx context.Context, defs []DefInput) (SyncResult, error) {
	res := SyncResult{Items: make([]SyncItem, 0, len(defs))}
	for _, in := range defs {
		item, err := r.Register(ctx, in)
		res.Items = append(res.Items, item)
		if err != nil {
			res.Failed++
		}
	}
	if res.Failed > 0 {
		return res, fmt.Errorf("%w：%d/%d 条失败", ErrSyncFailed, res.Failed, len(defs))
	}
	return res, nil
}

// toExternalDef 把本地入参转为适配层 DTO。
func toExternalDef(in DefInput) feishu.ExternalApprovalDef {
	return feishu.ExternalApprovalDef{
		ApprovalCode:     in.ApprovalCode,
		Name:             in.Name,
		GroupName:        in.GroupName,
		VisibleScopeJSON: in.VisibleScopeJSON,
		CreateLinkPC:     in.CreateLinkPC,
		CreateLinkMobile: in.CreateLinkMobile,
		SupportPC:        in.SupportPC,
		SupportMobile:    in.SupportMobile,
		CallbackURL:      in.CallbackURL,
		CallbackToken:    in.CallbackToken,
		CallbackKey:      in.CallbackKey,
	}
}
