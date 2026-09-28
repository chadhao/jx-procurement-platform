package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// handlers_org.go —— 组织查询接口（人员 / 部门；转交 / 加签目标选择器的数据源）。
//
// ★★ 数据源边界（2026-09-28 起切至**通讯录镜像**，docs/08 实施批次一）：
//
//	数据源为 **t_org_user / t_org_department**（飞书通讯录镜像，`internal/orgsync` 全量同步所得），
//	人员与部门**与飞书一致、覆盖全员**（含尚未配置系统角色者）——用户口径
//	「人员与部门的信息来源＝飞书，本系统不人工维护人员和部门信息」。
//	`role` 字段仍来自 `t_user_role`（LEFT JOIN，只读借用）：**镜像 ≠ 权限**（docs/08 §4.10），
//	未配角色者 role＝空串。是否过滤「未配角色者」＝**不过滤**（裁定与理由见交付报告/§3.15）。
//
// ★ 空表语义：镜像为空（尚未同步）⇒ 返回**空数组**（前端提示「暂无可选人员」），**不报错**。
//
// ★ 鉴权：挂 `api` 组（requireSession）＝**会话域业务接口**；未登录 ⇒ 401/40100。
//   故意**不**叠加 `identityFrom` 的角色映射校验：本接口是「选择器数据源」，
//   允许已登录但角色映射待配置的用户查看（与空表提示配合），鉴权收紧无业务收益。

// handleOrgUsers `GET /api/org/users` —— 可选人员清单（镜像中**在用**人员，全员覆盖）。
//
// 查询参数（均可选、可组合，**语义与切换前一致**）：
//   - `department`  精确匹配部门（主部门名称；配合 `GET /api/org/departments` 级联筛选）；
//   - `q`           按姓名**模糊**匹配（CONTAINS）。
//
// 响应 `items[]`：`open_id` / `name` / `role`（未配角色者＝空串）/ `department`（主部门名）；
// 稳定排序＝部门 → 姓名 → open_id（前端下拉展示顺序可预期）。
// 已离职/已删除人员（is_deleted=1）不出现——「离职/停用不可选」（docs/08 §4.12）。
func (d Deps) handleOrgUsers(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := d.DB.ListOrgUserDirectory(ctx)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	dept := strings.TrimSpace(c.QueryParam("department"))
	q := strings.TrimSpace(c.QueryParam("q"))

	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		if dept != "" && r.Department != dept {
			continue
		}
		if q != "" && !strings.Contains(r.Name, q) {
			continue
		}
		items = append(items, map[string]any{
			"open_id":    r.OpenID,
			"name":       r.Name,
			"role":       r.Role,
			"department": r.Department,
		})
	}
	// 稳定排序（不依赖 SQL 返回序，跨驱动行为可预期）。
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a["department"].(string) != b["department"].(string) {
			return a["department"].(string) < b["department"].(string)
		}
		if a["name"].(string) != b["name"].(string) {
			return a["name"].(string) < b["name"].(string)
		}
		return a["open_id"].(string) < b["open_id"].(string)
	})
	return ok(c, map[string]any{"items": items})
}

// handleOrgDepartments `GET /api/org/departments` —— 去重后的部门清单。
//
// 数据源＝镜像 `t_org_department`（在用、名称非空、去重、字典序稳定排序）；
// 镜像为空 ⇒ 空数组。
func (d Deps) handleOrgDepartments(c echo.Context) error {
	depts, err := d.DB.ListOrgDepartmentNames(c.Request().Context())
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	return ok(c, map[string]any{"items": depts})
}

// assigneeRoleViews 批量取任务办理人的「姓名 / 部门」展示信息。
//
// ★ 解绿框（用户实测反馈 2026-09-28）：任务列表此前只有裸 open_id，前端被迫回落显示
// `ou_xxx`。本 helper 一次 IN 查询取回全部所需映射（**避免 N+1**）。
// ★ 两级数据源（2026-09-28 起）：① `t_user_role`（角色映射，含已停用——展示不因停用消失）；
// ② 查不到者回落**通讯录镜像** `MapOrgUsersByOpenIDs`（**不过滤 is_deleted**，docs/08 C-E
// 历史解析口径）——覆盖「尚未配角色的全员」，办理人显示不再依赖人工先配角色。
// 两级都查不到的 open_id **不进 map** —— 调用方按「留空」处理，**绝不**把 open_id 塞进 name。
func (d Deps) assigneeRoleViews(ctx context.Context, tasks []store.FlowTask) map[string]store.UserRole {
	openIDs := make([]string, 0, len(tasks))
	for _, t := range tasks {
		openIDs = append(openIDs, t.AssigneeOpenID)
	}
	m, err := d.DB.MapUserRolesByOpenIDs(ctx, openIDs)
	if err != nil {
		// 展示信息取不到属降级而非致命：留空（前端回落 `-`），但要可见。
		d.Log.Warn("批量读取办理人角色映射失败（展示字段降级为空）", "error", err.Error())
		m = map[string]store.UserRole{}
	}
	// ★ 镜像兜底：t_user_role 查不到的 open_id 用镜像补姓名/部门（role 不伪造，留空）。
	var missing []string
	seen := make(map[string]bool, len(openIDs))
	for _, id := range openIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if _, ok := m[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		org, err := d.DB.MapOrgUsersByOpenIDs(ctx, missing)
		if err != nil {
			// 兜底也失败：降级留空，但要可见。
			d.Log.Warn("批量读取办理人镜像兜底失败（展示字段降级为空）", "error", err.Error())
		} else {
			for id, v := range org {
				if strings.TrimSpace(v.Name) == "" && strings.TrimSpace(v.Department) == "" {
					continue // 镜像里也无有效展示信息：保持留空（不塞 open_id）
				}
				m[id] = store.UserRole{OpenID: id, Name: v.Name, Department: v.Department}
			}
		}
	}
	return m
}

// handleOrgSync `POST /internal/org/sync` —— 手动触发通讯录全量同步（内部运维端点）。
//
// ★ 复用内部端点风格（X-Internal-Token，与 /internal/sync/* 同组）；
// 无视新鲜度阈值强制拉取；返回差异报告。失败 ⇒ 5xx + 明确错误（不静默）。
func (d Deps) handleOrgSync(c echo.Context) error {
	if d.OrgSync == nil {
		return fail(c, http.StatusServiceUnavailable, codeInternal, "组织同步未装配（Runner 为 nil）")
	}
	rep, err := d.OrgSync.RunNow(c.Request().Context())
	if err != nil {
		return fail(c, http.StatusBadGateway, codeInternal, "通讯录全量同步失败: "+err.Error())
	}
	d.audit(c.Request().Context(), &store.AuditLogRow{
		Action: "org_sync", Resource: "org_directory", Result: "allow",
	})
	return ok(c, map[string]any{
		"trigger":           rep.Trigger,
		"dept_total":        rep.DeptTotal,
		"user_total":        rep.UserTotal,
		"dept_added":        rep.DeptAdded,
		"dept_updated":      rep.DeptUpdated,
		"dept_soft_deleted": rep.DeptSoftDeleted,
		"user_added":        rep.UserAdded,
		"user_updated":      rep.UserUpdated,
		"user_soft_deleted": rep.UserSoftDeleted,
		"duration_ms":       rep.DurationMS,
	})
}
