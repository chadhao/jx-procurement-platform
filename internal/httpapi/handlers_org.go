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
// ★★ 数据源边界（与 docs/05-API §3.15 一致，务必如实传达）：
//
//	数据源为 **`t_user_role`**（＝**已在系统内配置角色的人员**），
//	**不是**飞书全量通讯录 —— 组织架构同步 / 镜像属另一工程，本期未做。
//	这与业务语义一致：**转交 / 加签的目标必须是有权限的审批人**，把单据转给
//	系统里没角色的人是无效的。若 `t_user_role` 只有 1 条，接口就返回 1 条
//	—— **不造假数据、不回落飞书通讯录实时拉取**。
//
// ★ 空表语义：列表为空 ⇒ 返回**空数组**（前端提示「暂无可选人员」），**不报错**。
//
// ★ 鉴权：挂 `api` 组（requireSession）＝**会话域业务接口**；未登录 ⇒ 401/40100。
//   故意**不**叠加 `identityFrom` 的角色映射校验：本接口是「选择器数据源」，
//   允许已登录但角色映射待配置的用户查看（与空表提示配合），鉴权收紧无业务收益。

// handleOrgUsers `GET /api/org/users` —— 可选人员清单（仅**启用中**的角色映射）。
//
// 查询参数（均可选、可组合）：
//   - `department`  精确匹配部门（配合 `GET /api/org/departments` 做级联筛选）；
//   - `q`           按姓名**模糊**匹配（CONTAINS）。
//
// 响应 `items[]`：`open_id` / `name` / `role` / `department`；
// 稳定排序＝部门 → 姓名 → open_id（前端下拉展示顺序可预期）。
func (d Deps) handleOrgUsers(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := d.DB.ListUserRoles(ctx)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	dept := strings.TrimSpace(c.QueryParam("department"))
	q := strings.TrimSpace(c.QueryParam("q"))

	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		// ★ 只返回「启用中」的映射：停用者已不是有效审批人，不应出现在转交/加签目标里。
		if !r.Active {
			continue
		}
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
// 数据源＝`t_user_role.department` 非空去重、稳定排序（字典序）；表为空 ⇒ 空数组。
func (d Deps) handleOrgDepartments(c echo.Context) error {
	depts, err := d.DB.ListDistinctDepartments(c.Request().Context())
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	return ok(c, map[string]any{"items": depts})
}

// assigneeRoleViews 批量取任务办理人的「姓名 / 部门」展示信息（数据源 t_user_role）。
//
// ★ 解绿框（用户实测反馈 2026-09-28）：任务列表此前只有裸 open_id，前端被迫回落显示
// `ou_xxx`。本 helper 一次 IN 查询取回全部所需映射（**避免 N+1**），查不到的 open_id
// **不进 map** —— 调用方按「留空」处理，**绝不**把 open_id 塞进 name。
func (d Deps) assigneeRoleViews(ctx context.Context, tasks []store.FlowTask) map[string]store.UserRole {
	openIDs := make([]string, 0, len(tasks))
	for _, t := range tasks {
		openIDs = append(openIDs, t.AssigneeOpenID)
	}
	m, err := d.DB.MapUserRolesByOpenIDs(ctx, openIDs)
	if err != nil {
		// 展示信息取不到属降级而非致命：留空（前端回落 `-`），但要可见。
		d.Log.Warn("批量读取办理人角色映射失败（展示字段降级为空）", "error", err.Error())
		return map[string]store.UserRole{}
	}
	return m
}
