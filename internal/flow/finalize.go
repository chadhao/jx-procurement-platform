// finalize.go —— 终态落账（架构转向 ③，04a §T02b / docs/11 R01–R04）。
//
// 职责：实例到达终态时，**由我方显式写**每类 L 台账（取代旧链 `worker/ingest.go:189 UpsertArchiveTx`）。
//
// ★ 三约束（team-lead 裁决 A5 / docs/11 §8.1）：
//  1. **不依赖旧链**：`flow.finalize` 的写入者是我方，不调用 `worker`/`ingest`；
//  2. **独立于 ingest**：本文件只读写 `t_ledger_archive` / `t_instance`（已构造好的 ext_json），
//     与 `worker` 包无任何耦合；
//  3. **可落账范围硬拦**：仅实例级 `L01–L07` + `L09`；`L08`（手工主数据）/`L10`（只读汇总）/
//     `L11`（派生）/`L12`（未启用）**硬拦**——与配置导入层（`config.PerInstanceLedgerTypes`）**同口径**。
//
// ★ 三层可见性（team-lead 裁决 A5.4，缺一不可）：
//
//	① 源头：配置导入层 `config.(*ImportPayload).Validate()` 已在导入时硬拒「doc_type → 非实例级台账」；
//	② 运行时：本文件 `ResolveLedgerTypes` 兜底，跳过 + `Warn`（非阻断终态）；
//	③ 落账后：`finalizeLedgersTx` 末尾自检「每类应落台账都应有行」，缺失 → `Error` 告警。
//
//	★ ③ 的必要性＝B47 教训：「恒 0 恰好等于期望值」是最隐蔽的静默——「没有违规」与「没有数据」
//	  在界面上完全一样。
package flow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// ResolveLedgerTypes 把「配置的 doc_type → 台账」判定为**可落账** / **应硬拦**两组。
//
// 可落账＝实例级台账（`config.PerInstanceLedgerTypes`：`L01–L07` + `L09`）；
// 其余（`L08`/`L10`/`L11`/`L12` 及任何未知键）→ rejected，**绝不落行**。
//
// 纯函数：便于测试与复用，不触碰任何状态。
func ResolveLedgerTypes(configured []string) (write, rejected []string) {
	for _, lt := range configured {
		t := strings.TrimSpace(lt)
		if t == "" {
			continue
		}
		if isInstanceLedger(t) {
			write = append(write, t)
			continue
		}
		rejected = append(rejected, t)
	}
	return write, rejected
}

// isInstanceLedger 判断某台账键是否可承载「实例级落一行」（与配置导入层同口径）。
func isInstanceLedger(lt string) bool {
	for _, t := range config.PerInstanceLedgerTypes {
		if t == lt {
			return true
		}
	}
	return false
}

// finalizeLedgersTx 在终态事务内显式落每类 L 台账。
//
// ★ 不可落账（L08/L10/L11/L12）**跳过 + Warn + 计数**，**不返回阻断性 error**：
// 若 error 阻断 → 一个配置错误会让同批合法台账一起回滚、终态落不定（"审批通过了却落不上账"
// 是硬故障，比静默更糟）。源头（导入层）已硬拒，此处是运行时兜底。
func (s *Service) finalizeLedgersTx(ctx context.Context, tx *sql.Tx, inst *store.Instance, at time.Time) error {
	if inst == nil {
		return nil
	}
	if s.maps == nil {
		return nil // 未注入配置映射 → 不写台账（不虚构口径）
	}
	configured := s.maps.LedgerTypesFor(inst.DocType)
	if len(configured) == 0 {
		return nil
	}
	write, rejected := ResolveLedgerTypes(configured)
	if len(rejected) > 0 {
		// ② 运行时兜底：不可落账键一定在导入层就该被拒；此处跳过 + Warn（可见，非静默）。
		s.log.Warn("finalize：跳过非实例级台账（应由配置导入层拦截，此处兜底）",
			"biz_no", inst.BizNo, "doc_type", inst.DocType, "rejected", rejected)
	}
	if len(write) == 0 {
		return nil
	}

	// ★ 金额：必须 > 0 才作有效金额落库（决策 #39）；否则保持 NULL + Warn。
	amount := inst.AmountCents
	if amount != nil && *amount <= 0 {
		s.log.Warn("finalize：金额非正，不落有效金额（#39）",
			"biz_no", inst.BizNo, "amount_cents", *amount)
		amount = nil
	}
	bizDate := bizDateOf(inst, at)
	ext := inst.ExtJSON
	if strings.TrimSpace(ext) == "" {
		ext = "{}"
	}
	// ★ N-042 权限位点：archive 的 designated_open_id / acceptors **同源自 ext**
	//   （designation 写 ext.designated_purchaser、GR 提交写 ext.acceptors ——
	//   规范列与 ext 双写，避免两处不一致；列过滤（ASSIGNED/PARTICIPATED）读规范列）。
	extMap := map[string]any{}
	if uerr := json.Unmarshal([]byte(ext), &extMap); uerr != nil {
		// 权限位点取值：ext 脏 ⇒ 两列留空（fail-closed —— 列空 ⇒ 行过滤不命中，宁少勿多；
		// 不中断终态，与 finalize 自检「告警不拒绝」同口径）。
		extMap = map[string]any{}
	}
	designated, _ := extMap["designated_purchaser"].(string)
	acceptors := ""
	switch av := extMap["acceptors"].(type) {
	case string:
		acceptors = av
	case nil:
	default:
		if b, err := json.Marshal(av); err == nil {
			acceptors = string(b)
		}
	}

	for _, lt := range write {
		if err := s.db.UpsertArchiveTx(ctx, tx, &store.LedgerArchive{
			LedgerType:       lt,
			BizNo:            inst.BizNo,
			InstanceCode:     inst.InstanceCode,
			SourceDocType:    inst.DocType,
			Department:       inst.Department,
			ApplicantOpenID:  inst.ApplicantOpenID,
			AmountCents:      amount,
			Supplier:         inst.Supplier,
			PurposeClassL1:   inst.PurposeClassL1,
			PurposeClassL2:   inst.PurposeClassL2,
			BizDate:          bizDate,
			DesignatedOpenID: designated,
			Acceptors:        acceptors,
			ExtJSON:          ext,
			CreatedAt:        at,
			UpdatedAt:        at,
		}); err != nil {
			return err
		}
	}

	// ③ 落账后自检：每类应落台账都应有行（B47 教训：恒 0 恰等于期望值最隐蔽）。
	//   缺失 → 审计记录 + 告警（Error 日志）：使「没有数据」不再与「没有违规」长得一样。
	for _, lt := range write {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM t_ledger_archive WHERE ledger_type = ? AND biz_no = ?`,
			lt, inst.BizNo).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			s.log.Error("finalize 自检：应落台账缺失（账可能未落，请检查 upsert）",
				"biz_no", inst.BizNo, "doc_type", inst.DocType, "ledger_type", lt)
			detail, _ := json.Marshal(map[string]any{"ledger_type": lt, "doc_type": inst.DocType})
			if err := s.db.InsertAuditTx(ctx, tx, &store.AuditLogRow{
				Action:     "finalize_missing_ledger",
				Resource:   "t_ledger_archive",
				TargetID:   inst.BizNo,
				Result:     "warn",
				DetailJSON: string(detail),
				CreatedAt:  at,
			}); err != nil {
				return err
			}
			continue
		}
		// ★ L09 列自检（N-036 · PC/SS#ledger_l09_written · else=「告警」——
		//   与行存在性同款形态：审计 warn ＋ Error 日志，不中断终态）。
		//   断言的列由**提交期注入**生产（httpapi#injectPCSSSystemFields）——
		//   缺列＝生产者被绕过/判据被误删，必须可见。
		if lt == "L09" && (inst.DocType == "PC" || inst.DocType == "SS") {
			var extRaw string
			if err := tx.QueryRowContext(ctx,
				`SELECT COALESCE(ext_json,'{}') FROM t_ledger_archive
WHERE ledger_type='L09' AND biz_no = ?`, inst.BizNo).Scan(&extRaw); err != nil {
				return err
			}
			l9ext := map[string]any{}
			if err := json.Unmarshal([]byte(extRaw), &l9ext); err == nil {
				need := []string{"exception_type"}
				if inst.DocType == "PC" {
					need = append(need, "change_chain", "change_count_to_date",
						"is_anomaly_listed", "resubmitted_to_group_at", "applicable_tier")
				}
				var missing []string
				for _, k := range need {
					v, ok := l9ext[k]
					if !ok || v == nil {
						missing = append(missing, k)
					}
				}
				if len(missing) > 0 {
					s.log.Error("finalize L09 列自检：生产者列缺失（台账自检判据告警）",
						"biz_no", inst.BizNo, "doc_type", inst.DocType, "missing", missing)
					detail, _ := json.Marshal(map[string]any{"ledger_type": "L09", "missing": missing})
					if err := s.db.InsertAuditTx(ctx, tx, &store.AuditLogRow{
						Action:     "ledger_l09_column_missing",
						Resource:   "t_ledger_archive",
						TargetID:   inst.BizNo,
						Result:     "warn",
						DetailJSON: string(detail),
						CreatedAt:  at,
					}); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// bizDateOf 计算台账业务日期（决策 #33：`YYYY-MM-DD`）。
// 优先取 ext_json 里的 `biz_date`（须严格合法），否则退回实例提交日期。
func bizDateOf(inst *store.Instance, at time.Time) string {
	if bd := extBizDate(inst.ExtJSON); bd != "" {
		return bd
	}
	base := inst.CreatedAt
	if base.IsZero() {
		base = at
	}
	if base.IsZero() {
		base = time.Now()
	}
	return base.Format("2006-01-02")
}

// extBizDate 从 ext_json 取 `biz_date`；**仅当其严格为 `YYYY-MM-DD`** 时返回，否则空串（#33）。
func extBizDate(extJSON string) string {
	if strings.TrimSpace(extJSON) == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(extJSON), &m); err != nil {
		return ""
	}
	v, ok := m["biz_date"]
	if !ok {
		return ""
	}
	sv := strings.TrimSpace(fmt.Sprint(v))
	if _, err := time.Parse("2006-01-02", sv); err != nil {
		return ""
	}
	return sv
}
