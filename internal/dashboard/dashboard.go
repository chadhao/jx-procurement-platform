// Package dashboard 实现 M5 的 4 张看板聚合逻辑（FR-M5-01/05/06/07）。
//
// 设计纪律（docs/01-PRD.md §6.3、docs/04-Architecture.md §1.2 / §5）：
//   - 数据源指向「运营表」：t_ledger_archive（只读同步存档）LEFT JOIN t_ledger_ops（可写运营表）
//     的合并视图。只看只读存档表会使「经办人指定集中度」「平均采购周期」等指标恒空（FR-M5-05 / TC-24）。
//   - 行级过滤：本包不改写任何权限口径，行过滤 SQL 片段（含 ? 占位）由调用方（接入层）
//     通过 permission.RowFilter 生成后经 Query 注入，在 SQL 层生效（架构 §5.2 要点 1）。
//   - 列级投影：本包只产出「指标行」；金额类指标以 amount_cents / amount_display 命名，
//     由接入层复用 permission.Project 在序列化阶段裁剪（架构 §5.3，TC-07）。
//   - 看板只读，本包不提供任何写入口（FR-M5-06）。
package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 看板 id → 名称（docs/01-PRD.md §6.3 第 13~16 行）。
const (
	DashboardBudget     = 13            // 预算执行看板（本期不启用，空态）
	DashboardPurchase   = 14            // 采购执行看板
	DashboardExpense    = 15            // 费用结构看板
	DashboardAnomaly    = 16            // 异常预警面板
	defaultSplitCents   = int64(100000) // 同供应商 + 同品类月累计阈值：1,000 元 = 100,000 分
	defaultCycleMonths  = 6             // 趋势默认回看月数
	defaultEmergencyHrs = 24            // 紧急采购补录/闭合时限（小时）
	defaultReviewHours  = 72            // 超时未审口径（3 个工作日近似）
)

// 运营表（ops_json）与存档表（ext_json）中使用的业务键。
// ★ Q14 已定案（2026-09-26）：L11 订单执行台账为**派生视图**，其键名见 order_execution.go；
// 本块只保留**运营表确实可写**的中文键（L01/L03/L08/L09/L12 等）。键名以数据为准，不硬编码控件 id。
const (
	keyAssigned   = "assigned_open_id" // 被指定经办人（优先 ops，回退 archive ext）
	keyAssignedCN = "指定经办人"            // 中文等价键（兼容人工登记口径）
	keyStatus     = "经办状态"
	keyDoneDate   = "完成日期"
	keyClosed     = "是否已核销闭合"
	keyMethod     = "采购方式"
	keyAcctChange = "账户变更"
	keyOverBudget = "预警状态"
)

// Result 看板响应（docs/05-API.md §4.4：指标卡 + 图表序列 + 监督指标 + 异常预警）。
type Result struct {
	ID          int              `json:"id"`
	Name        string           `json:"name"`
	Period      string           `json:"period"`
	Cards       []map[string]any `json:"cards"`
	Charts      []Chart          `json:"charts"`
	Supervision map[string]any   `json:"supervision"`
	Alerts      []map[string]any `json:"alerts"`
}

// Chart 图表序列（key/type/series）。
type Chart struct {
	Key    string           `json:"key"`
	Type   string           `json:"type"`
	Series []map[string]any `json:"series"`
}

// Query 行级过滤条件（SQL 片段 + 参数），由接入层用 permission.RowFilter 生成后注入。
// 台账表别名固定为 a；实例表（t_instance）查询亦以别名 a 承载。
type Query struct {
	LedgerRowSQL    string
	LedgerRowArgs   []any
	InstanceRowSQL  string
	InstanceRowArgs []any
	// SubmissionRowSQL/Args 针对 t_submission（报送登记）的行过滤。
	// ★ 报送表既不在台账族内、也不在实例表内，必须单列一路：否则以报送为数据源的聚合指标
	// （如「集团驳回后未处置」）只能全表 COUNT → 向受限角色暴露全局值。
	SubmissionRowSQL  string
	SubmissionRowArgs []any
}

// Row 合并视图行（存档 + 运营）。金额以分为单位，HasAmount 标记存档是否带金额。
type Row struct {
	LedgerType      string
	BizNo           string
	InstanceCode    string
	Department      string
	ApplicantOpenID string
	AmountCents     int64
	HasAmount       bool
	Supplier        string
	PurposeL1       string
	PurposeL2       string
	BizDate         string
	ArchiveExt      map[string]any
	Ops             map[string]any
}

// Builder 看板聚合器。
type Builder struct {
	db         *store.DB
	now        func() time.Time
	splitCents int64
}

// New 构造聚合器（默认时钟 time.Now，默认拆分阈值 1,000 元）。
func New(db *store.DB) *Builder {
	return &Builder{db: db, now: func() time.Time { return time.Now().UTC() }, splitCents: defaultSplitCents}
}

// WithNow 注入时钟（供测试与可控时间窗）。
func (b *Builder) WithNow(fn func() time.Time) *Builder {
	if fn != nil {
		b.now = fn
	}
	return b
}

// WithSplitThresholdCents 覆盖拆分阈值（来自 t_config_mapping: threshold.split_supplier_month）。
func (b *Builder) WithSplitThresholdCents(cents int64) *Builder {
	if cents > 0 {
		b.splitCents = cents
	}
	return b
}

// DashboardName 返回看板名称。
func DashboardName(id int) string {
	switch id {
	case DashboardBudget:
		return "预算执行看板"
	case DashboardPurchase:
		return "采购执行看板"
	case DashboardExpense:
		return "费用结构看板"
	case DashboardAnomaly:
		return "异常预警面板"
	default:
		return ""
	}
}

// Build 计算某看板数据。id=13（预算执行）本期不启用 → 返回空序列且不报错（HTTP 200）。
func (b *Builder) Build(ctx context.Context, id int, period string, q Query) (Result, error) {
	if strings.TrimSpace(period) == "" {
		period = b.now().Format("2006-01")
	}
	res := Result{
		ID: id, Name: DashboardName(id), Period: period,
		Cards: []map[string]any{}, Charts: []Chart{}, Alerts: []map[string]any{},
		Supervision: emptySupervision(),
	}
	switch id {
	case DashboardBudget:
		// 本期预算不启用：空态，不报错（docs/05-API.md §3.3）。
		return res, nil
	case DashboardPurchase:
		return b.buildPurchase(ctx, res, period, q)
	case DashboardExpense:
		return b.buildExpense(ctx, res, period, q)
	case DashboardAnomaly:
		return b.buildAnomaly(ctx, res, period, q)
	default:
		return res, fmt.Errorf("dashboard: 未知看板 id %d", id)
	}
}

// ---------- 14 采购执行看板 ----------

func (b *Builder) buildPurchase(ctx context.Context, res Result, period string, q Query) (Result, error) {
	win := windowStart(period, defaultCycleMonths-1)
	// ★ L11 是**派生视图**（Q14 定案），不落 `t_ledger_archive` —— 此处改走与台账派生视图
	//   同一口径（L04 骨架 + 关联 L07）。原实现直接读 ledger_type='L11'，而该值永不落行
	//   → 本看板五项指标恒空/恒 0 且不报错（架构审查 A-1）。
	r11, err := b.orderExecutionRows(ctx, win, q)
	if err != nil {
		return res, err
	}
	// 经办登记台账（L03）仍读存档行（它是实例级台账）。
	r03, err := b.fetchLedgerRows(ctx, []string{"L03"}, win, q)
	if err != nil {
		return res, err
	}

	// 在途订单数：订单执行台账中尚无「实际到货」的订单。
	inFlight := 0
	for _, r := range r11 {
		if strings.TrimSpace(r.OpsStr(keyActualArrival)) == "" {
			inFlight++
		}
	}
	res.Cards = append(res.Cards, card("in_flight_orders", "在途订单数", inFlight))

	// 平均采购周期：订单日期 → 运营表「完成日期（回退实际到货）」，单位天。
	var sumDays float64
	var n int
	for _, r := range r11 {
		if d, ok := cycleDays(r); ok {
			sumDays += float64(d)
			n++
		}
	}
	if n > 0 {
		res.Cards = append(res.Cards, card("avg_cycle_days", "平均采购周期", round1(sumDays/float64(n))))
	} else {
		res.Cards = append(res.Cards, map[string]any{"key": "avg_cycle_days", "label": "平均采购周期", "value": nil})
	}

	// 延期订单 TOP5（运营表「延期天数」> 0）。
	type delay struct {
		biz  string
		days int64
	}
	var delays []delay
	for _, r := range r11 {
		if d, ok := r.OpsInt(keyDelayDays); ok && d > 0 {
			delays = append(delays, delay{r.BizNo, d})
		}
	}
	sort.Slice(delays, func(i, j int) bool {
		if delays[i].days != delays[j].days {
			return delays[i].days > delays[j].days
		}
		return delays[i].biz < delays[j].biz
	})
	if len(delays) > 5 {
		delays = delays[:5]
	}
	delaySeries := make([]map[string]any, 0, len(delays))
	for _, d := range delays {
		delaySeries = append(delaySeries, countPoint(d.biz, d.days))
	}
	res.Charts = append(res.Charts, Chart{Key: "delay_top5", Type: "bar", Series: delaySeries})

	// 月度采购金额趋势（近 N 月）。
	sumByMonth := map[string]int64{}
	for _, r := range r11 {
		if m := monthOf(r.BizDate); m != "" {
			sumByMonth[m] += r.AmountCents
		}
	}
	months := lastMonths(period, defaultCycleMonths)
	trend := make([]map[string]any, 0, len(months))
	for _, m := range months {
		trend = append(trend, moneyPoint(m, sumByMonth[m]))
	}
	res.Charts = append(res.Charts, Chart{Key: "monthly_amount_trend", Type: "line", Series: trend})

	// 同供应商当月累计 TOP（本期，按供应商聚合）。
	supSum := map[string]int64{}
	for _, r := range r11 {
		if monthOf(r.BizDate) == period && strings.TrimSpace(r.Supplier) != "" {
			supSum[r.Supplier] += r.AmountCents
		}
	}
	res.Charts = append(res.Charts, Chart{Key: "top_supplier_month", Type: "bar", Series: topMoneyBars(supSum, 5)})

	// ★ 监督指标（FR-M5-07）：需求提出人任经办人的笔数（应恒为 0） + 经办人指定集中度。
	res.Supervision = buildSupervision(r03, period, q, b)
	return res, nil
}

// ---------- 15 费用结构看板 ----------

func (b *Builder) buildExpense(ctx context.Context, res Result, period string, q Query) (Result, error) {
	rows, err := b.fetchLedgerRows(ctx, []string{"L05"}, windowStart(period, defaultCycleMonths-1), q)
	if err != nil {
		return res, err
	}
	r05 := splitByType(rows, "L05")["L05"]

	periodRows := filterByMonth(r05, period)

	// 指标卡：本期费用总额（金额）+ 笔数。
	var total int64
	for _, r := range periodRows {
		total += r.AmountCents
	}
	res.Cards = append(res.Cards, moneyCard("expense_total", "本期费用总额", total))
	res.Cards = append(res.Cards, card("expense_count", "本期笔数", len(periodRows)))

	// 按部门分布。
	res.Charts = append(res.Charts, Chart{Key: "expense_by_department", Type: "bar", Series: groupMoney(periodRows, func(r Row) string { return r.Department }, 0)})
	// 按类别分布（二级明细，回退一级）。
	cat := func(r Row) string {
		if strings.TrimSpace(r.PurposeL2) != "" {
			return r.PurposeL2
		}
		return r.PurposeL1
	}
	res.Charts = append(res.Charts, Chart{Key: "expense_by_category", Type: "pie", Series: groupMoney(periodRows, cat, 0)})
	// 按供应商分布（TOP5）。
	res.Charts = append(res.Charts, Chart{Key: "expense_by_supplier", Type: "bar", Series: groupMoney(periodRows, func(r Row) string { return r.Supplier }, 5)})

	// 月度趋势。
	sumByMonth := map[string]int64{}
	for _, r := range r05 {
		if m := monthOf(r.BizDate); m != "" {
			sumByMonth[m] += r.AmountCents
		}
	}
	months := lastMonths(period, defaultCycleMonths)
	trend := make([]map[string]any, 0, len(months))
	for _, m := range months {
		trend = append(trend, moneyPoint(m, sumByMonth[m]))
	}
	res.Charts = append(res.Charts, Chart{Key: "expense_monthly_trend", Type: "line", Series: trend})

	// 异常科目提示：某二级明细当月金额 > 全部有值科目均值的 3 倍（离群）。
	res.Alerts = append(res.Alerts, alert("abnormal_subject", "异常科目提示", abnormalSubjects(periodRows, cat)))

	return res, nil
}

// ---------- 16 异常预警面板 ----------

func (b *Builder) buildAnomaly(ctx context.Context, res Result, period string, q Query) (Result, error) {
	// L11 已为派生视图、不落行，不再列入（原列入但从未消费 byType["L11"]）。
	types := []string{"L01", "L03", "L06", "L08", "L09", "L12"}
	rows, err := b.fetchLedgerRows(ctx, types, windowStart(period, defaultCycleMonths-1), q)
	if err != nil {
		return res, err
	}
	byType := splitByType(rows, types...)
	r01, r03, r08, r09, r12 := byType["L01"], byType["L03"], byType["L08"], byType["L09"], byType["L12"]

	// ① 超时未审：t_instance 处于 PENDING 且创建时间早于 3 个工作日（近似 72 小时）。
	overdueReview, err := b.countOverduePending(ctx, q)
	if err != nil {
		return res, err
	}
	res.Alerts = append(res.Alerts,
		alert("overdue_review", "超时未审", overdueReview),

		// ② 超预算：预算执行台账（L12）「预警状态」含「超支」；本期预算不启用 → 恒为 0。
		alert("over_budget", "超预算", countOpsContains(r12, keyOverBudget, "超支")),

		// ③ 紧急采购：例外事项台账（L09）采购方式含「紧急」。
		alert("emergency_purchase", "紧急采购", countOpsContains(r09, keyMethod, "紧急")),

		// ④ 单一来源：例外事项台账（L09）采购方式含「单一来源」或「独家」。
		alert("single_source", "单一来源", countOpsContainsAny(r09, keyMethod, "单一来源", "独家")),

		// ⑤ 账户变更：供应商档案与绩效表（L08）标记账户变更。
		alert("account_change", "账户变更", countTruthy(r08, keyAcctChange)),

		// ⑥ 拆分嫌疑：同供应商 + 同品类月累计 ≥ 1,000 元（且 ≥2 笔）。
		alert("split_suspect", "拆分嫌疑", b.countSplitSuspect(r01)),
	)

	// ⑦ 经办超期未完成：采购经办登记台账（L03）未完成且无完成日期。
	res.Alerts = append(res.Alerts, alert("handler_overdue", "经办超期未完成", countHandlerIncomplete(r03)))

	// ⑧ 紧急采购超 24 小时未补录或未核销闭合。
	res.Alerts = append(res.Alerts, alert("emergency_unclosed_over_24h", "紧急采购超 24 小时未闭合", b.countEmergencyUnclosed(r09)))

	// ⑨ 集团驳回后未处置：报送登记 grp_state 含「驳回」且无驳回原因/处置。
	rej, err := b.countGroupRejectedUndisposed(ctx, q)
	if err != nil {
		return res, err
	}
	res.Alerts = append(res.Alerts, alert("group_rejected_undisposed", "集团驳回后未处置", rej))

	// ★ 监督指标（FR-M5-07）：单独成项。
	sup := buildSupervision(r03, period, q, b)
	requesterCount := intFromAny(sup["requester_as_handler_count"])

	// ⑩ 需求提出人任经办人的笔数（应恒为 0，异常信号 → 红标 high）。
	level := "warn"
	if requesterCount > 0 {
		level = "high"
	}
	res.Alerts = append(res.Alerts, map[string]any{
		"key": "requester_as_handler", "level": level, "count": requesterCount,
	})

	// ⑪ 经办人指定集中度（异常信号判读：零违规 + 长期固定指定同一人）。
	res.Alerts = append(res.Alerts, map[string]any{
		"key": "handler_concentration", "level": "warn",
		"count": intFromAny(sup["concentration_max_count"]),
	})

	res.Supervision = sup
	return res, nil
}

// ---------- 监督指标（FR-M5-07） ----------

// buildSupervision 产出监督指标块：
//   - requester_as_handler_count：需求提出人任经办人的笔数（应恒为 0）；
//   - handler_concentration：经办人指定集中度（某人被指定笔数 ÷ 总笔数）。
//
// 数据源：采购经办登记台账（L03）的运营表口径（被指定经办人优先取 ops_json）。
func buildSupervision(r03 []Row, period string, _ Query, b *Builder) map[string]any {
	out := emptySupervision()
	rows := r03
	if strings.TrimSpace(period) != "" {
		rows = filterByMonth(r03, period)
	}
	total := len(rows)
	counts := map[string]int{}
	requester := 0
	maxCount := 0
	for _, r := range rows {
		assigned := r.Assigned()
		if assigned == "" {
			continue
		}
		counts[assigned]++
		if strings.TrimSpace(r.ApplicantOpenID) != "" && assigned == r.ApplicantOpenID {
			requester++
		}
	}
	conc := make([]map[string]any, 0, len(counts))
	for h, c := range counts {
		ratio := 0.0
		if total > 0 {
			ratio = float64(c) / float64(total)
		}
		if c > maxCount {
			maxCount = c
		}
		conc = append(conc, map[string]any{"handler": h, "ratio": round4(ratio), "count": c})
	}
	sort.Slice(conc, func(i, j int) bool {
		ci, _ := conc[i]["count"].(int)
		cj, _ := conc[j]["count"].(int)
		if ci != cj {
			return ci > cj
		}
		return conc[i]["handler"].(string) < conc[j]["handler"].(string)
	})
	out["requester_as_handler_count"] = requester
	out["handler_concentration"] = conc
	out["concentration_max_count"] = maxCount
	out["handler_total"] = total
	if b != nil {
		out["split_threshold_cents"] = b.splitCents
	}
	return out
}

func emptySupervision() map[string]any {
	return map[string]any{
		"requester_as_handler_count": 0,
		"handler_concentration":      []map[string]any{},
	}
}

// ---------- 数据读取（运营表合并视图） ----------

// fetchLedgerRows 读取指定台账类型在时间窗内的合并视图行（存档 LEFT JOIN 运营）。
// 行过滤在 SQL 层生效：where 内联 q.LedgerRowSQL（含 ? 占位）及其参数。
func (b *Builder) fetchLedgerRows(ctx context.Context, types []string, windowStart string, q Query) ([]Row, error) {
	if len(types) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(types)+2)
	holders := make([]string, len(types))
	for i, t := range types {
		holders[i] = "?"
		args = append(args, t)
	}
	where := "a.ledger_type IN (" + strings.Join(holders, ",") + ")"
	if strings.TrimSpace(windowStart) != "" {
		// 含无业务日期的行（其指标不依赖账期，如「在途订单数」）。
		where += " AND (COALESCE(a.biz_date,'') = '' OR substr(a.biz_date,1,7) >= ?)"
		args = append(args, windowStart)
	}
	if strings.TrimSpace(q.LedgerRowSQL) != "" {
		where += " AND (" + q.LedgerRowSQL + ")"
		args = append(args, q.LedgerRowArgs...)
	}

	query := `
SELECT a.ledger_type, COALESCE(a.biz_no,''), COALESCE(a.instance_code,''), COALESCE(a.department,''),
       COALESCE(a.applicant_open_id,''), a.amount_cents, COALESCE(a.supplier,''),
       COALESCE(a.purpose_class_l1,''), COALESCE(a.purpose_class_l2,''), COALESCE(a.biz_date,''),
       COALESCE(a.ext_json,'{}'), COALESCE(o.ops_json,'{}')
FROM t_ledger_archive a
LEFT JOIN t_ledger_ops o ON o.ledger_type = a.ledger_type AND o.biz_no = a.biz_no
WHERE ` + where

	sqlRows, err := b.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("dashboard: 读取运营表合并视图失败: %w", err)
	}
	defer func() { _ = sqlRows.Close() }()

	var out []Row
	for sqlRows.Next() {
		var (
			r      Row
			amount sql.NullInt64
			extRaw string
			opsRaw string
		)
		if err := sqlRows.Scan(&r.LedgerType, &r.BizNo, &r.InstanceCode, &r.Department, &r.ApplicantOpenID,
			&amount, &r.Supplier, &r.PurposeL1, &r.PurposeL2, &r.BizDate, &extRaw, &opsRaw); err != nil {
			return nil, err
		}
		if amount.Valid {
			r.AmountCents = amount.Int64
			r.HasAmount = true
		}
		r.ArchiveExt = decodeJSONMap(extRaw)
		r.Ops = decodeJSONMap(opsRaw)
		out = append(out, r)
	}
	return out, sqlRows.Err()
}

// countOverduePending 统计超时未审实例（PENDING 且早于 72 小时）。
func (b *Builder) countOverduePending(ctx context.Context, q Query) (int, error) {
	cutoff := b.now().Add(-time.Duration(defaultReviewHours) * time.Hour).UTC().Format(time.RFC3339)
	where := "a.status = 'PENDING' AND a.created_at < ?"
	args := []any{cutoff}
	if strings.TrimSpace(q.InstanceRowSQL) != "" {
		where += " AND (" + q.InstanceRowSQL + ")"
		args = append(args, q.InstanceRowArgs...)
	}
	var n int
	if err := b.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_instance a WHERE `+where, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// countGroupRejectedUndisposed 统计集团驳回后未处置的报送登记。
//
// ★ 必须在 SQL 层施加报送行过滤（`q.SubmissionRowSQL`）：本指标是异常面板的对外指标，
// 只有 `ALL` 范围的角色才应看到全量；受限角色只统计「本人登记」的报送
// （`t_submission` 的身份锚点为 department / applicant_open_id / assigned_open_id / acceptors，
//
//	见 permission.RowFilterForSubmission；migrations/0003 补齐了这四列）。
func (b *Builder) countGroupRejectedUndisposed(ctx context.Context, q Query) (int, error) {
	where := []string{
		`COALESCE(grp_state,'') LIKE '%驳回%'`,
		`COALESCE(reject_reason,'') = ''`,
	}
	var args []any
	if strings.TrimSpace(q.SubmissionRowSQL) != "" {
		where = append(where, "("+q.SubmissionRowSQL+")")
		args = append(args, q.SubmissionRowArgs...)
	}
	var n int
	// ★ 表别名 `s` 必须与 permission.RowFilterForSubmission(..., "s") 的别名一致
	//（Q14-B 第 5 项改为按真实列过滤后，谓词里带别名，FROM 不加别名会直接 SQL 报错）。
	err := b.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_submission s WHERE `+strings.Join(where, " AND "), args...).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// countSplitSuspect 统计拆分嫌疑组数：同供应商 + 同品类（二级明细）月累计 ≥ 阈值且 ≥2 笔。
func (b *Builder) countSplitSuspect(rows []Row) int {
	type gk struct{ supplier, cat, month string }
	groups := map[gk]struct {
		sum int64
		cnt int
	}{}
	for _, r := range rows {
		if strings.TrimSpace(r.Supplier) == "" {
			continue
		}
		m := monthOf(r.BizDate)
		if m == "" {
			continue
		}
		k := gk{r.Supplier, r.PurposeL2, m}
		g := groups[k]
		g.sum += r.AmountCents
		g.cnt++
		groups[k] = g
	}
	n := 0
	for _, g := range groups {
		if g.sum >= b.splitCents && g.cnt >= 2 {
			n++
		}
	}
	return n
}

// countEmergencyUnclosed 统计紧急采购超 24 小时未补录或未核销闭合。
func (b *Builder) countEmergencyUnclosed(rows []Row) int {
	cutoff := b.now().Add(-time.Duration(defaultEmergencyHrs) * time.Hour)
	n := 0
	for _, r := range rows {
		if !strings.Contains(r.OpsStr(keyMethod), "紧急") {
			continue
		}
		if strings.TrimSpace(r.OpsStr(keyClosed)) == "是" {
			continue
		}
		if t, ok := parseDayStrict(r.BizDate); ok && !t.After(cutoff) {
			n++
		}
	}
	return n
}

// ---------- 行访问器 ----------

// Assigned 解析被指定经办人：优先运营表，回退存档扩展字段（Q14 口径：运营表可写）。
func (r Row) Assigned() string {
	if v := jsonStr(r.Ops[keyAssigned]); v != "" {
		return v
	}
	if v := jsonStr(r.ArchiveExt[keyAssigned]); v != "" {
		return v
	}
	if v := jsonStr(r.Ops[keyAssignedCN]); v != "" {
		return v
	}
	return jsonStr(r.ArchiveExt[keyAssignedCN])
}

// OpsStr 读取运营字段字符串值。
func (r Row) OpsStr(key string) string { return jsonStr(r.Ops[key]) }

// OpsInt 读取运营字段整数值。
func (r Row) OpsInt(key string) (int64, bool) { return jsonInt(r.Ops[key]) }

// ---------- 通用小工具 ----------

func splitByType(rows []Row, types ...string) map[string][]Row {
	out := make(map[string][]Row, len(types))
	for _, t := range types {
		out[t] = nil
	}
	for _, r := range rows {
		if _, ok := out[r.LedgerType]; ok {
			out[r.LedgerType] = append(out[r.LedgerType], r)
		}
	}
	return out
}

func filterByMonth(rows []Row, month string) []Row {
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		if monthOf(r.BizDate) == month {
			out = append(out, r)
		}
	}
	return out
}

func groupMoney(rows []Row, keyFn func(Row) string, topN int) []map[string]any {
	sums := map[string]int64{}
	for _, r := range rows {
		k := strings.TrimSpace(keyFn(r))
		if k == "" {
			k = "（未填）"
		}
		sums[k] += r.AmountCents
	}
	return topMoneyBars(sums, topN)
}

func topMoneyBars(sums map[string]int64, topN int) []map[string]any {
	type kv struct {
		k string
		v int64
	}
	list := make([]kv, 0, len(sums))
	for k, v := range sums {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		return list[i].k < list[j].k
	})
	if topN > 0 && len(list) > topN {
		list = list[:topN]
	}
	out := make([]map[string]any, 0, len(list))
	for _, it := range list {
		out = append(out, moneyPoint(it.k, it.v))
	}
	return out
}

func countOpsContains(rows []Row, key, sub string) int {
	n := 0
	for _, r := range rows {
		if strings.Contains(r.OpsStr(key), sub) {
			n++
		}
	}
	return n
}

func countOpsContainsAny(rows []Row, key string, subs ...string) int {
	n := 0
	for _, r := range rows {
		v := r.OpsStr(key)
		for _, s := range subs {
			if strings.Contains(v, s) {
				n++
				break
			}
		}
	}
	return n
}

func countTruthy(rows []Row, key string) int {
	n := 0
	for _, r := range rows {
		if isTruthy(r.Ops[key]) {
			n++
		}
	}
	return n
}

func countHandlerIncomplete(rows []Row) int {
	n := 0
	for _, r := range rows {
		if strings.TrimSpace(r.OpsStr(keyDoneDate)) != "" {
			continue
		}
		if strings.TrimSpace(r.OpsStr(keyStatus)) == "已完成" {
			continue
		}
		n++
	}
	return n
}

// abnormalSubjects 统计离群科目数：某科目金额 > 有值科目均值的 3 倍。
func abnormalSubjects(rows []Row, cat func(Row) string) int {
	sums := map[string]int64{}
	var total int64
	for _, r := range rows {
		k := strings.TrimSpace(cat(r))
		if k == "" || r.AmountCents <= 0 {
			continue
		}
		sums[k] += r.AmountCents
		total += r.AmountCents
	}
	if len(sums) < 2 {
		return 0
	}
	mean := float64(total) / float64(len(sums))
	th := mean * 3
	n := 0
	for _, v := range sums {
		if float64(v) > th {
			n++
		}
	}
	return n
}

func card(key, label string, v any) map[string]any {
	return map[string]any{"key": key, "label": label, "value": v}
}

func moneyCard(key, label string, cents int64) map[string]any {
	return map[string]any{"key": key, "label": label, "amount_cents": cents, "amount_display": FormatCents(cents)}
}

func countPoint(x string, y int64) map[string]any {
	return map[string]any{"x": x, "y": y}
}

func moneyPoint(x string, cents int64) map[string]any {
	return map[string]any{"x": x, "amount_cents": cents, "amount_display": FormatCents(cents)}
}

func alert(key, desc string, count int) map[string]any {
	return map[string]any{"key": key, "label": desc, "level": "warn", "count": count}
}

// ---------- 解析与格式化小工具 ----------

func decodeJSONMap(raw string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func jsonStr(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func jsonInt(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		return int64(t), true
	case int64:
		return t, true
	case int:
		return int64(t), true
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i, true
		}
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
			return i, true
		}
	}
	return 0, false
}

func isTruthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return false
		}
		switch strings.ToLower(s) {
		case "0", "false", "否", "no", "无", "none":
			return false
		}
		return true
	}
	return false
}

func intFromAny(v any) int {
	if i, ok := jsonInt(v); ok {
		return int(i)
	}
	return 0
}

func monthOf(bizDate string) string {
	s := strings.TrimSpace(bizDate)
	if len(s) >= 7 {
		return s[:7]
	}
	return ""
}

func cycleDays(r Row) (int, bool) {
	start, ok := parseDayStrict(r.BizDate)
	if !ok {
		return 0, false
	}
	doneStr := r.OpsStr(keyDoneDate)
	if doneStr == "" {
		doneStr = r.OpsStr(keyActualArrival) // L11 派生行的到货日期
	}
	end, ok := parseDayStrict(doneStr)
	if !ok {
		return 0, false
	}
	days := int(math.Round(end.Sub(start).Hours() / 24))
	if days < 0 {
		days = 0
	}
	return days, true
}

func parseDayStrict(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	layouts := []string{"2006-01-02", time.RFC3339, "2006-01-02T15:04:05", "2006/01/02"}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// lastMonths 返回截至 period（含）的 n 个月（YYYY-MM，升序）。
func lastMonths(period string, n int) []string {
	y, m, ok := parsePeriod(period)
	if !ok || n <= 0 {
		return nil
	}
	out := make([]string, 0, n)
	for i := n - 1; i >= 0; i-- {
		yy, mm := shiftMonth(y, m, -i)
		out = append(out, fmt.Sprintf("%04d-%02d", yy, mm))
	}
	return out
}

// windowStart 返回 period 往前 back 个月的账期（用于时间窗下界）。
func windowStart(period string, back int) string {
	y, m, ok := parsePeriod(period)
	if !ok {
		return ""
	}
	yy, mm := shiftMonth(y, m, -back)
	return fmt.Sprintf("%04d-%02d", yy, mm)
}

func parsePeriod(period string) (int, int, bool) {
	s := strings.TrimSpace(period)
	if len(s) < 7 {
		return 0, 0, false
	}
	var y, m int
	if _, err := fmt.Sscanf(s[:7], "%d-%d", &y, &m); err != nil || y == 0 || m < 1 || m > 12 {
		return 0, 0, false
	}
	return y, m, true
}

func shiftMonth(year, month, delta int) (int, int) {
	idx := year*12 + (month - 1) + delta
	return idx / 12, idx%12 + 1
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
func round4(f float64) float64 { return math.Round(f*10000) / 10000 }

// FormatCents 将分格式化为带千分位的字符串（金额出参 * 元）。
func FormatCents(cents int64) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	yuan := cents / 100
	frac := cents % 100
	s := strconv.FormatInt(yuan, 10)
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteString(",")
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteString(",")
		}
	}
	out := b.String() + "." + fmt.Sprintf("%02d", frac)
	if neg {
		out = "-" + out
	}
	return out
}
