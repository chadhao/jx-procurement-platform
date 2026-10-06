package httpapi

// N-067① 跨节点钩子 —— 无待办节点上的 approval(<node_id>) 判据
// （spec/chain.json#conventions.checks_when 第三处承载口径 · 批 47 规格）。
//
// ★ 缺口形态：第 ① 条通用求值器只在「任务被 approve」时按 task.NodeID 触发 ⇒
// 无待办节点（generates_task 解析为否）上的 hard 判据**既不执行、也不 fail-closed**
// —— 本钩子补齐该格；★ 挂载点唯一＝approveReject、拦在 Flow.Approve 之前（事务外）。
// ★ 求值复用第 ① 条的内层引擎 evaluateApprovalChecksFor（同一注册表、同一分派三条 ——
// 不写第二套判据逻辑）。
//
// ★ 「跨过」语义（照契约）：该节点之前的、**已物化为 t_flow_task** 的节点全部通过之
// 时刻 —— 由「前序物化节点状态」推算（无任务节点在任务表里不可见、不新增持久化）。
// ★ 去重：① 两路径按有无待办互斥（IsApproval 节点一律走第 ① 条，本处只看 !IsApproval）；
// ② 只在「本次 approve 使前序集合由未全过→全过」的那一次触发（会签末票语义）；
// ③ 前序集合为空（跨过时刻落在提交期，契约 ☆ 本期不定义）⇒ **恒不触发**。
//
// ★ Facts 组装（取证结论 · 逐字段表见 COLLAB#N-067 回执）：
//   - 列直取：doc_type / amount_cents / purpose_class_l1 / department /
//     applicant_open_id / applicant_name（migrations/0001）；
//   - ext_json 直取：payment_method_input / is_fixed_asset / change_amount_cents /
//     original_contract_amount_cents（applyBizFields 是唯一写入方 —— 提交期随 fields 落）；
//   - 角色：Auth.ResolveRole(applicant) ⇒ ApplicantIsOpsSupervisor（**只影响指派角色、
//     不改变节点集合与顺序** ⇒ 查询失败退 false 亦不使跨过判定失真）；
//   - HasContract（★ 唯一无列可取的 route/序列相关字段）：CT 恒 true；否则由
//     **物化合同节点存在性**确定性推导（contract_supervisor/contract_pgm ∈ t_flow_task
//     ⇔ 提交期 R-26 插入发生；tier2/3/CT 上该推导值不改变 BuildNodes 拓扑 —— 等价性
//     论证见回执；**非 seq 空档反推**：读的是具名 node_id，不受四类形变影响）；
//   - RelatedPRAmountCents：无生产者、emergency 通路未接（A8）⇒ 恒 nil（与提交期一致，
//     不影响任何可达 route）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/chain"
	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// errCrossedNodeCheck 跨节点判据未通过（业务面 ⇒ 调用方映射 400 并点名；
// 与基建错误 —— Compute/DB 失败 —— 分流，后者走 500）。
var errCrossedNodeCheck = errors.New("approval 跨节点判据未通过")

// evaluateCrossedNodeChecks 对「本次 approve 跨过的无待办节点」执行其
// approval(<node_id>) hard 判据。actor ＝ 本次操作人（open_id）。
func (d Deps) evaluateCrossedNodeChecks(ctx context.Context, bizNo, taskID, actor string,
	fields map[string]any) error {
	task, err := d.DB.GetFlowTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("读任务 %s 失败: %w", taskID, err)
	}
	inst, err := d.DB.GetInstanceByBizNo(ctx, task.BizNo)
	if err != nil {
		return fmt.Errorf("读实例 %s 失败: %w", task.BizNo, err)
	}
	facts, err := d.factsFromPersistedInstance(ctx, inst)
	if err != nil {
		return err // ext 损坏等：可见失败（500 面）
	}
	rc, err := d.Chain.Compute(ctx, facts)
	if err != nil {
		return err // 输入类 ⇒ 调用方按 isChainInputError 归 400，其余 500
	}
	allTasks, err := d.DB.ListFlowTasks(ctx, task.BizNo)
	if err != nil {
		return fmt.Errorf("读任务列表失败: %w", err)
	}
	// 按 NodeID 归组（当前状态：本次 approve 尚未应用 —— task 仍是 PENDING）。
	type nState struct{ pending, rejected, approved bool }
	states := map[string]*nState{}
	for _, tk := range allTasks {
		s := states[tk.NodeID]
		if s == nil {
			s = &nState{}
			states[tk.NodeID] = s
		}
		switch tk.Status {
		case flow.TaskPending:
			s.pending = true
		case flow.TaskRejected:
			s.rejected = true
		case flow.TaskApproved:
			s.approved = true
		}
	}
	// 当前判定（镜像 flow#nodeDecision：无 PENDING ∧ 无 REJECTED ∧ ≥1 APPROVED）。
	approved := func(nodeID string) bool {
		s := states[nodeID]
		return s != nil && !s.pending && !s.rejected && s.approved
	}
	// 会视判定：本次 approve（T 置 APPROVED）后其节点是否全过 ——
	// T 可能是该节点多票之一（顺序会签）⇒ 逐票重算，末票语义。
	wouldApprove := func(nodeID string) bool {
		if nodeID != task.NodeID {
			return approved(nodeID)
		}
		pend, rej, appr := 0, 0, 0
		for _, tk := range allTasks {
			if tk.NodeID != nodeID {
				continue
			}
			if tk.TaskID == task.TaskID {
				appr++ // 本次 approve 后
				continue
			}
			switch tk.Status {
			case flow.TaskPending:
				pend++
			case flow.TaskRejected:
				rej++
			case flow.TaskApproved:
				appr++
			}
		}
		return pend == 0 && rej == 0 && appr > 0
	}

	form, ok := d.Spec.Forms[inst.DocType]
	if !ok {
		return nil // 无表单单据 ⇒ 无判据
	}
	for i, n := range rc.Nodes {
		if n.IsApproval {
			continue // 有任务的节点一律走第 ① 条（去重判据 ①）
		}
		// 前序 = 遍历序更靠前、且**已物化**（存在任务行）的节点。
		preds := []string{}
		for j := 0; j < i && j < len(rc.Nodes); j++ {
			p := &rc.Nodes[j]
			if !p.IsApproval {
				continue
			}
			if _, mat := states[p.SourceNodeID]; !mat {
				continue // 未物化 ⇒ 不算前序（契约限定「已物化为 t_flow_task」）
			}
			preds = append(preds, p.SourceNodeID)
		}
		if len(preds) == 0 {
			continue // 契约 ☆：跨过时刻落在提交期 ⇒ 本期不定义、恒不触发
		}
		curAll := true
		for _, pid := range preds {
			if !approved(pid) {
				curAll = false
				break
			}
		}
		wouldAll := true
		for _, pid := range preds {
			if !wouldApprove(pid) {
				wouldAll = false
				break
			}
		}
		if !(wouldAll && !curAll) {
			continue // 非本次 approve 的跨过时刻（此前已跨过 ⇒ 不重复；未到 ⇒ 不触发）
		}
		// 跨过 ⇒ 跑该节点的 approval(node) hard 判据（复用第 ① 条内层引擎）。
		if err := evaluateApprovalChecksFor(ctx, form, inst, fields, n.SourceNodeID, actor, d); err != nil {
			return fmt.Errorf("%w: %s", errCrossedNodeCheck, err.Error())
		}
	}
	return nil
}

// factsFromPersistedInstance 以持久化实例组装链算事实（N-067① 取证结论）。
func (d Deps) factsFromPersistedInstance(ctx context.Context, inst *store.Instance) (chain.Facts, error) {
	ext := map[string]any{}
	if strings.TrimSpace(inst.ExtJSON) != "" && inst.ExtJSON != "{}" {
		if err := json.Unmarshal([]byte(inst.ExtJSON), &ext); err != nil {
			return chain.Facts{}, fmt.Errorf("实例 %s 的 ext_json 损坏（无法组装链算事实）: %w", inst.BizNo, err)
		}
	}
	f := chain.Facts{
		DocType:         inst.DocType,
		AmountCents:     inst.AmountCents,
		UsageCategoryL1: inst.PurposeClassL1,
		Department:      inst.Department,
		ApplicantOpenID: inst.ApplicantOpenID,
		ApplicantName:   inst.ApplicantName,
	}
	if s, _ := ext["payment_method_input"].(string); s != "" {
		f.PaymentMethodInput = s
	}
	f.IsFixedAsset = extTruthy(ext["is_fixed_asset"])
	if v, ok := toFloat64(ext["change_amount_cents"]); ok {
		c := int64(v)
		f.ChangeAmountCents = &c
	}
	if v, ok := toFloat64(ext["original_contract_amount_cents"]); ok {
		o := int64(v)
		f.OriginalContractAmountCents = &o
	}
	// RelatedPRAmountCents：无生产者（emergency 通路未接）⇒ 恒 nil —— 与提交期一致。
	// HasContract：CT 恒 true（提交侧同款强制）；其余由物化合同节点推导（见头注）。
	f.HasContract = inst.DocType == "CT"
	if !f.HasContract {
		has, err := d.hasMaterializedContractNodes(ctx, inst.BizNo)
		if err != nil {
			return chain.Facts{}, err
		}
		f.HasContract = has
	}
	// 角色（只影响指派、不影响节点集合/顺序 ⇒ 失败退 false 不失真）。
	if d.Auth != nil && inst.ApplicantOpenID != "" {
		if ur, err := d.Auth.ResolveRole(ctx, inst.ApplicantOpenID); err == nil && ur != nil &&
			ur.Role == "综合运营主管" {
			f.ApplicantIsOpsSupervisor = true
		}
	}
	return f, nil
}

// hasMaterializedContractNodes 合同两级节点是否已物化（＝提交期 R-26 是否插入）。
// ★ 读**具名 node_id**（contract_supervisor/contract_pgm），不是 seq 空档反推 ——
// 不受 tier3_plus/tier_expand/合同展开/R-26 四类形变影响。
func (d Deps) hasMaterializedContractNodes(ctx context.Context, bizNo string) (bool, error) {
	var n int
	if err := d.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM t_flow_task WHERE biz_no = ? AND node_id IN ('contract_supervisor','contract_pgm')`,
		bizNo).Scan(&n); err != nil {
		return false, fmt.Errorf("读物化合同节点失败: %w", err)
	}
	return n > 0, nil
}

// extTruthy ext 布尔宽容解析（提交期写入的是 JSON 原生 bool；兼容历史字符串形态）。
func extTruthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(strings.TrimSpace(x), "true")
	case float64:
		return x != 0
	default:
		return false
	}
}
