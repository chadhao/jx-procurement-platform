package httpapi

// 附件上传（M6 / FR-M0-09 前半 / 决策 D4）：
//   POST /api/approval/attachments      multipart 上传 → 暂存（objectstore + t_attachment_staging）
//   GET  /api/approval/attachments/:fid owner-only 读取未绑定暂存件（已绑定件走 /api/attachment/:fid）
//
// ★ 边界纪律：
//   - 提交前的文件**没有实例归属** ⇒ 只进暂存表，提交事务内绑定（0016 头注）；
//   - 未绑定件仅 owner 可见（404 不泄漏他人 file_id 存在性）；
//   - 大小上限 20 MiB（内网单据附件；已知代价：无病毒扫描，登记于批 1 风险）；
//   - 上传顺手惰性清理过期未绑定件（单实例低频，不另起清理协程）。

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

const (
	uploadMaxBytes   = 20 << 20 // 20 MiB
	stagingTTL       = 7 * 24 * time.Hour
	uploadFieldLimit = 32 << 20 // ParseMultipartForm 内存/临时文件阈值（含表单其余部分）
)

type uploadResponse struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	Size     int64  `json:"size_bytes"`
}

func (d Deps) handleApprovalAttachmentUpload(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	if d.Objects == nil {
		return fail(c, http.StatusServiceUnavailable, codeNotReady, "对象存储未装配")
	}
	ctx := c.Request().Context()

	if err := c.Request().ParseMultipartForm(uploadFieldLimit); err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "multipart 解析失败: "+err.Error())
	}
	file, header, err := c.Request().FormFile("file")
	if err != nil {
		return fail(c, http.StatusBadRequest, codeBadRequest, "缺少 multipart 字段 file: "+err.Error())
	}
	defer func() { _ = file.Close() }()

	if header.Size > uploadMaxBytes {
		return fail(c, http.StatusRequestEntityTooLarge, codeBadRequest,
			"附件超过 20 MiB 上限")
	}
	data, err := io.ReadAll(io.LimitReader(file, uploadMaxBytes+1))
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, "读取上传内容失败: "+err.Error())
	}
	if int64(len(data)) > uploadMaxBytes {
		return fail(c, http.StatusRequestEntityTooLarge, codeBadRequest, "附件超过 20 MiB 上限")
	}

	fileID := newStagingFileID()
	fileName := sanitizeUploadName(header.Filename)
	storageKey := "staging/" + idn.OpenID + "/" + fileID
	if err := d.Objects.Put(ctx, storageKey, data); err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, "对象存储写入失败: "+err.Error())
	}
	now := time.Now()
	row := &store.AttachmentStaging{
		FileID: fileID, OwnerOpenID: idn.OpenID, FileName: fileName,
		SizeBytes: int64(len(data)), StorageKey: storageKey, StorageKind: d.Objects.Kind(),
		CreatedAt: now, ExpiresAt: now.Add(stagingTTL),
	}
	if err := d.DB.PutStaging(ctx, row); err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	// 惰性清理过期未绑定件（尽力而为；失败只告警不影响本次上传）。
	if n, pErr := d.DB.PurgeExpiredStaging(ctx, now); pErr != nil {
		d.Log.Warn("清理过期附件暂存失败", "error", pErr.Error())
	} else if n > 0 {
		d.Log.Info("已清理过期附件暂存", "purged", n)
	}
	d.audit(ctx, &store.AuditLogRow{ActorOpenID: idn.OpenID, ActorRole: idn.Role,
		Action: "upload", Resource: "api:approval", TargetID: fileID, Result: "allow"})
	return ok(c, uploadResponse{FileID: fileID, FileName: fileName, Size: int64(len(data))})
}

func (d Deps) handleApprovalAttachmentStagingRead(c echo.Context) error {
	idn, _, err := d.identityFrom(c)
	if err != nil {
		return fail(c, http.StatusUnauthorized, codeRoleMapped, "未映射角色或会话失效")
	}
	ctx := c.Request().Context()
	fileID := strings.TrimSpace(c.Param("file_id"))
	if fileID == "" {
		return fail(c, http.StatusBadRequest, codeBadRequest, "file_id 不能为空")
	}
	row, err := d.DB.GetStaging(ctx, fileID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fail(c, http.StatusNotFound, codeNotFound, "暂存附件不存在")
		}
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	// owner-only：非本人一律 404（不泄漏他人 file_id 的存在性）。
	if row.OwnerOpenID != idn.OpenID {
		return fail(c, http.StatusNotFound, codeNotFound, "暂存附件不存在")
	}
	if row.BoundBizNo != "" {
		return fail(c, http.StatusNotFound, codeNotFound,
			"暂存件已绑定（请走 /api/attachment/"+fileID+"）")
	}
	if !row.ExpiresAt.After(time.Now()) {
		return fail(c, http.StatusNotFound, codeNotFound, "暂存件已过期")
	}
	if d.Objects == nil {
		return fail(c, http.StatusServiceUnavailable, codeNotReady, "对象存储未装配")
	}
	data, err := d.Objects.Get(ctx, row.StorageKey)
	if err != nil {
		return fail(c, http.StatusBadGateway, codeInternal, "暂存件读取失败: "+err.Error())
	}
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="`+row.FileName+`"`)
	return c.Blob(http.StatusOK, "application/octet-stream", data)
}

// newStagingFileID 生成暂存文件 id（stg_ + 128bit 随机 hex）。
func newStagingFileID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败属环境级异常；回退时间戳随机 —— 不静默复用同一 id。
		return "stg_fallback_" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return "stg_" + hex.EncodeToString(b[:])
}

// sanitizeUploadName 剥离路径与控制字符（防路径穿越与响应头注入）。
func sanitizeUploadName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" || name == "" {
		return "attachment"
	}
	var sb strings.Builder
	for _, r := range name {
		if unicode.IsControl(r) {
			continue
		}
		sb.WriteRune(r)
	}
	out := sb.String()
	if out == "" {
		return "attachment"
	}
	return out
}
