package httpapi

// 运营性常量表后台管理（T2 / R-24 / 用户 A4 定案）。
//
// ★ 守护栏（spec/constants.json#policy + critical_note）：
//   1. **只停用不删**：DELETE 显式 409 拒绝（物理删除路径不存在）；
//   2. **role_display_name 只能改显示名**：禁止 POST 新增角色行；PUT 仅允许改 value/sort/status
//      —— 角色的**有无**由 chain.json#roles 制度性定义，后台能加角色＝绕过 10 角色口径；
//   3. 每次成功变更写审计（操作人/时间/表/条目前后值）。
// 权限：requireSysAdmin（与「系统管理」页同口径）。

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// knownConstantTables 合法 table_key 白名单（来自 spec/constants.json；装载侧 [C] 已校验，
// 这里防「拿任意表名写库」）。
func (d Deps) knownConstantTables() map[string]bool {
	m := map[string]bool{}
	if d.Spec != nil && d.Spec.Constants != nil {
		for _, t := range d.Spec.Constants.Tables {
			m[t.Key] = true
		}
	}
	return m
}

func (d Deps) requireSpecForAdmin(c echo.Context) bool {
	if d.Spec == nil {
		_ = fail(c, http.StatusServiceUnavailable, codeNotReady, "机读规格/常量登记册未装配")
		return false
	}
	return true
}

// handleAdminConstantsList GET /api/admin/constants?table=unit[&status=active]
func (d Deps) handleAdminConstantsList(c echo.Context) error {
	if _, granted := d.requireSysAdmin(c); !granted {
		return nil
	}
	if !d.requireSpecForAdmin(c) {
		return nil
	}
	tableKey := strings.TrimSpace(c.QueryParam("table"))
	if tableKey == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "table 不能为空")
	}
	if !d.knownConstantTables()[tableKey] {
		return fail(c, http.StatusBadRequest, codeBadRequest, "未知常量表: "+tableKey)
	}
	rows, err := d.DB.ListConstants(c.Request().Context(), tableKey, c.QueryParam("status"))
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	def, _ := d.Spec.Constants.TableByKey(tableKey)
	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		items = append(items, map[string]any{
			"id": r.ID, "table": r.TableKey, "value": r.Value,
			"sort_order": r.SortOrder, "status": r.Status,
		})
	}
	return ok(c, map[string]any{
		"table": map[string]any{
			"key": def.Key, "label": def.Label, "delete_policy": def.DeletePolicy,
			"used_by": def.UsedBy,
		},
		"items": items,
	})
}

// handleAdminConstantsCreate POST /api/admin/constants {table, value, sort_order?}
func (d Deps) handleAdminConstantsCreate(c echo.Context) error {
	idn, granted := d.requireSysAdmin(c)
	if !granted {
		return nil
	}
	if !d.requireSpecForAdmin(c) {
		return nil
	}
	var req struct {
		Table     string `json:"table"`
		Value     string `json:"value"`
		SortOrder *int   `json:"sort_order"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	req.Table = strings.TrimSpace(req.Table)
	req.Value = strings.TrimSpace(req.Value)
	if !d.knownConstantTables()[req.Table] {
		return fail(c, http.StatusBadRequest, codeBadRequest, "未知常量表: "+req.Table)
	}
	if req.Value == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "value 不能为空")
	}
	// ★ 护栏：role_display_name 禁止新增角色（角色有无归 chain.json，R-24 critical_note）。
	if req.Table == "role_display_name" {
		return fail(c, http.StatusBadRequest, codeBadRequest,
			"role_display_name 表禁止新增角色 —— 角色的有无由 spec/chain.json#roles 定义（R-24），后台只能改显示名")
	}
	sort := 0
	if req.SortOrder != nil {
		sort = *req.SortOrder
	}
	row, err := d.DB.InsertConstant(c.Request().Context(), req.Table, req.Value, sort)
	if err != nil {
		if err == store.ErrConstantConflict {
			return fail(c, http.StatusConflict, codeConflict, "同表同值已存在: "+req.Value)
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	d.audit(c.Request().Context(), &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: actorRoleOf(idn), Action: "constant_create",
		Resource: "table:" + req.Table, TargetID: row.Value, Result: "allow",
		DetailJSON: `{"value":` + strconv.Quote(row.Value) + `,"status":"active"}`,
	})
	return ok(c, map[string]any{"id": row.ID, "table": row.TableKey, "value": row.Value, "status": row.Status})
}

// handleAdminConstantsUpdate PUT /api/admin/constants/:id {value?, sort_order?, status?}
func (d Deps) handleAdminConstantsUpdate(c echo.Context) error {
	idn, granted := d.requireSysAdmin(c)
	if !granted {
		return nil
	}
	if !d.requireSpecForAdmin(c) {
		return nil
	}
	id, err := parseInt64(c.Param("id"))
	if err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "非法的常量 id")
	}
	before, err := d.DB.GetConstant(c.Request().Context(), id)
	if err != nil {
		return fail(c, http.StatusNotFound, codeNotFound, "常量不存在")
	}
	var req struct {
		Value     *string `json:"value"`
		SortOrder *int    `json:"sort_order"`
		Status    *string `json:"status"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "请求体非法: "+err.Error())
	}
	status := ""
	if req.Status != nil {
		status = strings.TrimSpace(*req.Status)
		if status != "active" && status != "retired" {
			return fail(c, http.StatusBadRequest, codeBadRequest, "status 仅允许 active / retired（只停用不删，R-24）")
		}
	}
	// ★ role_display_name：只允许改**显示名**（value）与排序/状态 —— 不提供任何
	//   「删除角色」或「改角色语义」的入口（语义字段本就不在本表）。
	value := ""
	if req.Value != nil {
		value = strings.TrimSpace(*req.Value)
		if value == "" {
			return fail(c, http.StatusBadRequest, codeBadRequest, "value 不得为空字符串（改名请给新名；删除请用 status=retired）")
		}
	}
	row, err := d.DB.UpdateConstant(c.Request().Context(), id, value, req.SortOrder, status)
	if err != nil {
		if err == store.ErrConstantConflict {
			return fail(c, http.StatusConflict, codeConflict, "同表同值已存在")
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	d.audit(c.Request().Context(), &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: actorRoleOf(idn), Action: "constant_update",
		Resource: "table:" + row.TableKey, TargetID: row.Value, Result: "allow",
		DetailJSON: `{"before":{"value":` + strconv.Quote(before.Value) +
			`,"status":` + strconv.Quote(before.Status) +
			`},"after":{"value":` + strconv.Quote(row.Value) +
			`,"status":` + strconv.Quote(row.Status) + `}}`,
	})
	return ok(c, map[string]any{"id": row.ID, "table": row.TableKey, "value": row.Value, "status": row.Status})
}

// handleAdminConstantsDeleteRefused DELETE /api/admin/constants/:id —— **永远 409**。
// ★ R-24：物理删除会破坏「历史单据值快照可追溯」（constants.json#policy.delete_reason）。
func (d Deps) handleAdminConstantsDeleteRefused(c echo.Context) error {
	idn, granted := d.requireSysAdmin(c)
	if !granted {
		return nil
	}
	d.audit(c.Request().Context(), &store.AuditLogRow{
		ActorOpenID: idn.OpenID, ActorRole: actorRoleOf(idn), Action: "constant_delete_refused",
		Resource: "table:constant", TargetID: c.Param("id"), Result: "deny",
		DetailJSON: `{"reason":"retire_only（R-24 只停用不删）"}`,
	})
	return fail(c, http.StatusConflict, codeConflict,
		"常量表只停用不删除（R-24）：请改用 PUT 将 status 置为 retired —— 历史单据存的是值快照，物理删除会让「值从哪来」无从追溯")
}
