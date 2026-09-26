package worker

import "strings"

// terminalStatuses 飞书实例终态集合。
// 收敛规则：终态不被中间态覆盖；REJECTED 不是终态——驳回后重提会使同一实例再次进入 PENDING
// （FR-M3-05 / TC-16，这正是幂等键不能取 instance_code+status 的原因）。
var terminalStatuses = map[string]bool{
	"APPROVED":       true,
	"CANCELED":       true,
	"DELETED":        true,
	"OVERTIME_CLOSE": true,
}

// ConvergeStatus 按状态机收敛为「当前状态」：保留历史由追加式状态表承担。
func ConvergeStatus(current, incoming string) string {
	incoming = strings.TrimSpace(incoming)
	if incoming == "" {
		return current
	}
	if current == "" {
		return incoming
	}
	if terminalStatuses[current] && !strings.EqualFold(current, incoming) {
		// 终态不被中间态覆盖（FR-M3-04）。
		return current
	}
	return incoming
}

// BizNoParts 业务单号「前缀-YYMM-####」拆分结果（自建侧只读归档，不生成，ADR-06）。
type BizNoParts struct {
	Prefix string
	YYMM   string
	Seq    string
}

// ParseBizNo 解析业务单号；格式不符时容错返回可得片段（不报错，FR-M2-04）。
// ★ 「#### 是否按月重置」属待确认项（Q7），此处只做拆分不做语义判断。
func ParseBizNo(bizNo string) BizNoParts {
	s := strings.TrimSpace(bizNo)
	if s == "" {
		return BizNoParts{}
	}
	parts := strings.Split(s, "-")
	var out BizNoParts
	if len(parts) > 0 {
		out.Prefix = strings.TrimSpace(parts[0])
	}
	if len(parts) > 1 {
		out.YYMM = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		out.Seq = strings.TrimSpace(parts[len(parts)-1])
	}
	return out
}
