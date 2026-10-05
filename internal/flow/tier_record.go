package flow

// PC/SS 档位审批记录生产者（N-062 J1 · tier_approval_record / tier_chain_record）。
//
// ★ 两字段 spec 声明 `source=system` 且 `carried_by_kind=pending_implementation`
//   （「声明了却没人执行」）—— 规则正本（forms/PC.json / SS.json）：
//   「系统自动关联本单按所取档位走的审批实例与结论（含审批人／时间／意见）」。
// ★ 结论只在**终态**齐备 ⇒ 生产时点 = Act 提交事务**之后**（post-commit）：
//   ① 状态史 / op_log 在事务内最后才写（AppendStatusHistory 晚于 advanceTx ⇒
//      事务内读会缺最后一条）；② store 单连接（MaxOpenConns=1）⇒ 事务内再起
//      非事务读会死锁。⇒ 与 emit 同排（提交后阶段），失败只记日志不回滚审批
//   （审批已落库，回滚不了；记录缺失可见于日志与字段空值）。
// ★ 落点双写：实例 ext `tier_approval_record` / `tier_chain_record`（表单字段正名）
//   ＋ L09 台账行 ext `approval_record`（spec/ledger-mapping.json#ledgers.L09.fields
//   的列名 —— 台账页「审批记录」列读它；finalize 已在事务内写过该行，此处补丁）。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// tierRecordFieldKey 单据 → 表单字段键。
var tierRecordFieldKey = map[string]string{
	"PC": "tier_approval_record",
	"SS": "tier_chain_record",
}

// WriteTierApprovalRecord 终态后回写档位审批记录（PC/SS；其它单据 no-op）。
// 内容 = 本实例全部 APPROVE/REJECT 操作（含驳回）按 op_id 序：
//
//	「节点名 审批人 时间 同意/拒绝 意见」以「；」连接。
//
// 失败由调用方记日志（审批本体已提交，不因记录失败而报错）。
func (s *Service) WriteTierApprovalRecord(ctx context.Context, inst *store.Instance) error {
	fieldKey := tierRecordFieldKey[inst.DocType]
	if fieldKey == "" {
		return nil
	}
	ops, err := s.db.ListFlowOpLogs(ctx, inst.BizNo)
	if err != nil {
		return fmt.Errorf("读操作留痕失败: %w", err)
	}
	tasks, err := s.db.ListFlowTasks(ctx, inst.BizNo)
	if err != nil {
		return fmt.Errorf("读任务失败: %w", err)
	}
	nodeName := make(map[string]string, len(tasks))
	assigneeName := make(map[string]string, len(tasks))
	for _, t := range tasks {
		nodeName[t.TaskID] = t.NodeName
		assigneeName[t.TaskID] = t.AssigneeName
	}

	// 审批人姓名：org 镜像批量解析，命不中回落任务上的 assignee 姓名，再回落 open_id
	// （绝不因解析失败丢记录 —— 记录是「档位实际经过审批」的证据）。
	var ids []string
	for _, op := range ops {
		if op.OpType == OpApprove || op.OpType == OpReject {
			if op.ActorOpenID != "" {
				ids = append(ids, op.ActorOpenID)
			}
		}
	}
	names := map[string]string{}
	if len(ids) > 0 {
		if views, err := s.db.MapOrgUsersByOpenIDs(ctx, ids); err == nil {
			for id, v := range views {
				if v.Name != "" {
					names[id] = v.Name
				}
			}
		}
	}

	var parts []string
	for _, op := range ops {
		if op.OpType != OpApprove && op.OpType != OpReject {
			continue
		}
		decision := "同意"
		if op.OpType == OpReject {
			decision = "拒绝"
		}
		node := nodeName[op.TaskID]
		if node == "" {
			node = op.NodeID
		}
		person := names[op.ActorOpenID]
		if person == "" {
			person = assigneeName[op.TaskID]
		}
		if person == "" {
			person = op.ActorOpenID
		}
		part := fmt.Sprintf("%s %s %s %s", node, person,
			op.CreatedAt.Format("2006-01-02 15:04"), decision)
		if r := strings.TrimSpace(op.Reason); r != "" {
			part += "：" + r
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return fmt.Errorf("实例 %s 无 APPROVE/REJECT 留痕，审批记录无源", inst.BizNo)
	}
	record := strings.Join(parts, "；")

	// ① 实例 ext（fresh 读-改-写，避免覆盖并发的推送列状态）。
	cur, err := s.db.GetInstanceByBizNo(ctx, inst.BizNo)
	if err != nil {
		return fmt.Errorf("读实例失败: %w", err)
	}
	ext := map[string]any{}
	if cur.ExtJSON != "" {
		_ = json.Unmarshal([]byte(cur.ExtJSON), &ext)
	}
	ext[fieldKey] = record
	b, err := json.Marshal(ext)
	if err != nil {
		return err
	}
	cur.ExtJSON = string(b)
	cur.UpdatedAt = time.Now()
	cur.UpdateTime++
	if err := s.db.UpsertInstance(ctx, cur); err != nil {
		return fmt.Errorf("回写实例字段失败: %w", err)
	}

	// ② L09 台账行 ext 的 approval_record 列（台账页读列名键；行缺失＝落账未发生，
	//    报错可见 —— PC/SS 终态必落 L09，缺行即异常）。
	arch, err := s.db.GetArchiveByKey(ctx, "L09", inst.BizNo)
	if err != nil {
		return fmt.Errorf("读 L09 台账行失败: %w", err)
	}
	aext := map[string]any{}
	if arch.ExtJSON != "" {
		_ = json.Unmarshal([]byte(arch.ExtJSON), &aext)
	}
	aext["approval_record"] = record
	ab, err := json.Marshal(aext)
	if err != nil {
		return err
	}
	arch.ExtJSON = string(ab)
	arch.UpdatedAt = time.Now()
	if err := s.db.UpsertArchive(ctx, s.db, arch); err != nil {
		return fmt.Errorf("回写 L09 台账行失败: %w", err)
	}
	return nil
}
