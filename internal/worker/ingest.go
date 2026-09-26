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
			if _, err := g.db.AppendStatusHistory(ctx, tx, &store.StatusHistory{
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

		// 台账存档：仅当 doc_type→ledger_type 已配置时写入（口径 Q14 待定，未配置不写，避免虚构）。
		if strings.TrimSpace(det.BizNo) != "" && g.maps != nil {
			if lt, ok := g.maps.LedgerTypeFor(docType); ok {
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
					ExtJSON:         "{}",
					CreatedAt:       now,
					UpdatedAt:       now,
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
