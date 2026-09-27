package worker

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 入库来源标记（区分事件入库 / 对账补录，架构 §3.2 t_instance.source）。
const (
	SourceEvent     = "event"
	SourceReconcile = "reconcile"
)

// Ingestor 把「实例详情」幂等写入业务表：实例主表 / 表单字段 / 状态史（追加式）/ 台账存档。
// 幂等由各表 UNIQUE 约束与 UPSERT 保证：重放、对账补录均不产生重复行。
type Ingestor struct {
	db   *store.DB
	maps *config.Maps
	log  *slog.Logger
	now  func() time.Time
}

// NewIngestor 构造入库器。
func NewIngestor(db *store.DB, maps *config.Maps, log *slog.Logger) *Ingestor {
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &Ingestor{
		db:   db,
		maps: maps,
		log:  observ.WithComponent(log, "ingest"),
		now:  func() time.Time { return time.Now().UTC() },
	}
}

// WithClock 注入时钟（供测试）。
func (g *Ingestor) WithClock(fn func() time.Time) *Ingestor {
	if fn != nil {
		g.now = fn
	}
	return g
}

// Ingest 幂等入库一个实例详情。source 取 SourceEvent / SourceReconcile。
func (g *Ingestor) Ingest(ctx context.Context, det *feishu.InstanceDetail, source string) error {
	if det == nil || strings.TrimSpace(det.InstanceCode) == "" {
		return errors.New("ingest: 实例详情缺少 instance_code")
	}
	if source == "" {
		source = SourceEvent
	}
	now := g.now()

	docType := ""
	if g.maps != nil && g.maps.Approval != nil {
		if t, ok := g.maps.Approval.DocType(det.ApprovalCode); ok {
			docType = t
		}
	}

	// 规范字段抽取（FR-M2-08）：把表单控件值搬到 t_instance 的规范列。
	// ★ 必须早于 ParseBizNo —— 若模板未走流水号控件，单号来自表单字段，需先抽取再解析。
	ExtractDetail(g.maps, docType, det)

	// ★ Q21 定案（2026-09-27）：**单笔金额必须 > 0**。0 或负金额不作为有效金额落库
	//   （保持 NULL）并记警告 —— 否则「0 元采购单」既被接受、又不被任何规则拦下，
	//   还会以 0 参与看板金额统计与档位判定。
	if det.AmountCents != nil && *det.AmountCents <= 0 {
		g.log.Warn("实例金额非正，已忽略该金额（Q21：单笔金额必须 > 0）",
			"instance_code", det.InstanceCode, "amount_cents", *det.AmountCents)
		det.AmountCents = nil
	}

	// 台账 ext_json：承载非规范列的可检索字段（如关联合同号，供变更链回溯）。
	extJSON := BuildExtJSON(g.maps, docType, det.Fields)

	parts := ParseBizNo(det.BizNo)

	return g.db.WithTx(ctx, func(tx *sql.Tx) error {
		current := ""
		cur, err := g.db.GetInstanceTx(ctx, tx, det.InstanceCode)
		switch {
		case err == nil:
			current = cur.Status
		case errors.Is(err, store.ErrNotFound):
			// 首次入库
		default:
			return err
		}
		converged := ConvergeStatus(current, det.StatusRaw)

		occurred := det.OccurredAt
		if occurred.IsZero() {
			occurred = now
		}

		// 状态史：追加式写入，保留全部变迁（含驳回重提链），永不覆盖（TC-16 / FR-M3-05）。
		if strings.TrimSpace(det.StatusRaw) != "" {
			if _, _, err := g.db.AppendStatusHistory(ctx, tx, &store.StatusHistory{
				InstanceCode:   det.InstanceCode,
				Status:         det.StatusRaw,
				OperatorOpenID: det.ApplicantOpenID,
				OccurredAt:     occurred,
			}); err != nil {
				return err
			}
		}

		inst := &store.Instance{
			InstanceCode:    det.InstanceCode,
			ApprovalCode:    det.ApprovalCode,
			DocType:         docType,
			BizNo:           det.BizNo,
			BizNoPrefix:     parts.Prefix,
			BizNoYYMM:       parts.YYMM,
			BizNoSeq:        parts.Seq,
			Status:          converged,
			StatusRaw:       det.StatusRaw,
			ApplicantOpenID: det.ApplicantOpenID,
			ApplicantName:   det.ApplicantName,
			Department:      det.Department,
			AmountCents:     det.AmountCents,
			PurposeClassL1:  det.PurposeClassL1,
			PurposeClassL2:  det.PurposeClassL2,
			Supplier:        det.Supplier,
			Source:          source,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := g.db.UpsertInstanceTx(ctx, tx, inst); err != nil {
			return err
		}

		fields := make([]store.InstanceField, 0, len(det.Fields))
		for _, f := range det.Fields {
			biz := f.BizField
			if g.maps != nil && g.maps.Field != nil {
				if mapped, ok := g.maps.Field.BizField(docType, f.FieldID); ok {
					biz = mapped
				}
			}
			fields = append(fields, store.InstanceField{
				InstanceCode: det.InstanceCode,
				FieldID:      f.FieldID,
				FieldName:    f.FieldName,
				BizField:     biz,
				ValueText:    f.ValueText,
				ValueType:    f.ValueType,
				RawJSON:      f.RawJSON,
			})
		}
		if len(fields) > 0 {
			if err := g.db.UpsertFieldsTx(ctx, tx, det.InstanceCode, fields); err != nil {
				return err
			}
		}

		// 附件元数据登记（B39）：**只登记、不下载** —— 事件处理有 3 秒窗口，
		// 网络 IO 绝不能放在同步路径上；文件本体按需拉取（下载端点 / 凭证包）。
		for _, ref := range CollectAttachments(det) {
			if err := g.db.UpsertAttachmentTx(ctx, tx, &store.Attachment{
				FileID:       ref.FileID,
				InstanceCode: det.InstanceCode,
				BizNo:        det.BizNo,
				FileName:     ref.Name,
				SizeBytes:    ref.Size,
			}); err != nil {
				return err
			}
		}

		// 台账存档：仅当 doc_type→ledger_type 已配置时写入（未配置不写，避免虚构口径）。
		//
		// ★ 一对多（B47 修复）：一个 doc_type 可对应**多个**台账 —— `PR` 同时落
		//   `L02`（采购需求与审批台账）与 `L03`（采购经办登记台账）。原先只取第一个
		//   会让 `L03` **永远没有行**，而看板「需求提出人任经办人的笔数」读 `L03`
		//   → 恒为 0，且 **0 恰好是该指标的期望值**（错得看不出来）。
		if strings.TrimSpace(det.BizNo) != "" && g.maps != nil {
			for _, lt := range g.maps.LedgerTypesFor(docType) {
				if err := g.db.UpsertArchiveTx(ctx, tx, &store.LedgerArchive{
					LedgerType:      lt,
					BizNo:           det.BizNo,
					InstanceCode:    det.InstanceCode,
					SourceDocType:   docType,
					Department:      det.Department,
					ApplicantOpenID: det.ApplicantOpenID,
					AmountCents:     det.AmountCents,
					Supplier:        det.Supplier,
					PurposeClassL1:  det.PurposeClassL1,
					PurposeClassL2:  det.PurposeClassL2,
					// ★ 业务日期（B32）：此前**生产端从不写入**该列，导致 L11 派生的到货字段、
					//   看板全部按月指标、以及「同供应商当月累计」口径**恒为空**。
					//   口径统一为 `YYYY-MM-DD`（全系统唯一格式）：取实例提交/发生日期。
					BizDate:   occurred.Format("2006-01-02"),
					ExtJSON:   extJSON,
					CreatedAt: now,
					UpdatedAt: now,
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
