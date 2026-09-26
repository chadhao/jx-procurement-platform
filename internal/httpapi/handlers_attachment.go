package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 附件（B39）—— **只做下载**。
//
// ★ 模式 A 下本系统不创建实例，附件由申请人在飞书侧上传；本系统按 `file_id`
// **按需拉取**并缓存到对象存储。元数据在入库时登记（零网络 IO），文件本体此处才取。
//
// ★ 权限：**不冗余身份列**，一律以附件的 `instance_code` 回查 `t_instance` 的可见性
// （`permission.RowFilterForInstances`），与实例/台账看到的是同一套行范围。

// instanceVisible 判断当前身份能否看见该实例（行级过滤在 SQL 层执行）。
//
// ★ 表别名必须是 `a`：`permission.RowFilterForInstances` 内部经 `RowFilter` 生成谓词，
// 而 `RowFilter` 会把空别名兜底成 `a`（如 `a.department IN (?)`）。若此处不给
// `t_instance` 起别名，就会报 "no such column: a.department" → 500。
func (d Deps) instanceVisible(ctx context.Context, code string, cond permission.Condition) (bool, error) {
	where := "a.instance_code = ?"
	args := []any{code}
	if strings.TrimSpace(cond.SQL) != "" {
		where += " AND (" + cond.SQL + ")"
		args = append(args, cond.Args...)
	}
	var n int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_instance a WHERE `+where, args...).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// handleInstanceAttachments GET /api/instance/:code/attachments —— 列出附件**元数据**（不拉文件）。
func (d Deps) handleInstanceAttachments(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	ctx := c.Request().Context()
	rule, err := d.Perm.Resolve(ctx, "api:instances", idn)
	if err != nil || permission.IsDenyAll(rule) {
		return fail(c, http.StatusForbidden, codeForbidden, "无权限访问实例")
	}
	code := strings.TrimSpace(c.Param("code"))
	if code == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "实例编号不能为空")
	}
	if ok, err := d.instanceVisible(ctx, code, permission.RowFilterForInstances(rule.RowScope, idn)); err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	} else if !ok {
		d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "view",
			Resource: "api:instances", TargetID: code, Result: "deny"})
		return fail(c, http.StatusForbidden, codeRowForbidden, "越权访问：该实例不在你的可见范围内")
	}

	rows, err := d.DB.ListAttachmentsByInstance(ctx, code)
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	items := make([]map[string]any, 0, len(rows))
	for _, a := range rows {
		items = append(items, attachmentMetaMap(a))
	}
	d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "view",
		Resource: "api:instances", TargetID: code, Result: "allow"})
	return ok(c, map[string]any{"instance_code": code, "items": items, "total": len(items)})
}

// handleAttachmentDownload GET /api/attachment/:file_id —— 下载附件（按需拉取 + 缓存）。
func (d Deps) handleAttachmentDownload(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	ctx := c.Request().Context()
	rule, err := d.Perm.Resolve(ctx, "api:instances", idn)
	if err != nil || permission.IsDenyAll(rule) {
		return fail(c, http.StatusForbidden, codeForbidden, "无权限访问实例")
	}
	fileID := strings.TrimSpace(c.Param("file_id"))
	if fileID == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "file_id 不能为空")
	}

	// ① 元数据必须已登记 —— 没有元数据就无法判定它属于哪个实例，也就**无法做行级判定**，
	//    此时一律拒绝（宁可拒绝也不放行：附件是"内容"而非"计数"，越权后果更重）。
	meta, err := d.DB.GetAttachmentByFileID(ctx, fileID)
	if err != nil {
		return fail(c, http.StatusNotFound, codeNotFound, "附件未登记（或 file_id 不存在）")
	}
	// ② 行级：以所属实例的可见性为准。
	if ok, err := d.instanceVisible(ctx, meta.InstanceCode, permission.RowFilterForInstances(rule.RowScope, idn)); err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	} else if !ok {
		d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "download",
			Resource: "api:instances", TargetID: meta.InstanceCode, Result: "deny"})
		return fail(c, http.StatusForbidden, codeRowForbidden, "越权访问：该附件不在你的可见范围内")
	}

	// ③ 取字节：先看主存，未命中再向飞书拉取并缓存。
	data, err := d.attachmentBytes(ctx, meta)
	if err != nil {
		return fail(c, http.StatusBadGateway, codeInternal, "附件获取失败: "+err.Error())
	}
	d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role, Action: "download",
		Resource: "api:instances", TargetID: meta.FileID, Result: "allow"})

	name := strings.TrimSpace(meta.FileName)
	if name == "" {
		name = meta.FileID
	}
	// 文件名进 Content-Disposition 前必须转义，避免头注入与中文乱码。
	c.Response().Header().Set("Content-Disposition",
		"attachment; filename*=UTF-8''"+url.PathEscape(name))
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	return c.Blob(http.StatusOK, "application/octet-stream", data)
}

// attachmentBytes 取附件字节：主存命中直接返回；否则拉取 → 落主存 → 回填 → 返回。
//
// ★ 降级：未配置对象存储时（`Objects == nil`）**不缓存、直接转发**，链路仍可用，
// 只是每次都打飞书接口（会在日志里体现）。这是刻意保留的降级路径，不是静默丢功能。
func (d Deps) attachmentBytes(ctx context.Context, meta *store.Attachment) ([]byte, error) {
	if d.Objects != nil && meta.StorageKey != "" {
		if b, err := d.Objects.Get(ctx, meta.StorageKey); err == nil {
			return b, nil
		} else if d.Log != nil {
			d.Log.Warn("附件主存读取失败，将回源拉取", "file_id", meta.FileID, "error", err.Error())
		}
	}
	if d.Feishu == nil {
		return nil, errFeishuUnavailable
	}
	b, err := d.Feishu.DownloadAttachment(ctx, meta.FileID)
	if err != nil {
		return nil, err
	}
	if d.Objects != nil {
		if err := d.Objects.Put(ctx, meta.FileID, b); err != nil {
			// 落盘失败不影响本次下载（数据已拿到），但必须留痕。
			if d.Log != nil {
				d.Log.Warn("附件落主存失败（本次仍返回）", "file_id", meta.FileID, "error", err.Error())
			}
		} else if err := d.DB.MarkAttachmentFetched(ctx, meta.ID, meta.FileID, d.Objects.Kind()); err != nil {
			if d.Log != nil {
				d.Log.Warn("附件存储信息回填失败", "file_id", meta.FileID, "error", err.Error())
			}
		}
	}
	return b, nil
}

// attachmentMetaMap 附件元数据的对外形态（**不含存储内部键**，避免暴露实现细节）。
func attachmentMetaMap(a store.Attachment) map[string]any {
	m := map[string]any{
		"file_id":       a.FileID,
		"instance_code": a.InstanceCode,
		"biz_no":        a.BizNo,
		"field_id":      a.FieldID,
		"file_name":     a.FileName,
		"fetched":       a.StorageKey != "",
		"storage_kind":  a.StorageKind,
	}
	if a.SizeBytes != nil {
		m["size_bytes"] = *a.SizeBytes
	}
	if a.FetchedAt != nil {
		m["fetched_at"] = a.FetchedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return m
}

var errFeishuUnavailable = &attachmentError{"飞书客户端不可用（未配置凭据）"}

type attachmentError struct{ msg string }

func (e *attachmentError) Error() string { return e.msg }
