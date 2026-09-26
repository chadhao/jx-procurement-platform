package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

// UploadAttachment 附件上传。
// POST /open-apis/approval/openapi/v2/file/upload
//
// ★ 联调状态：官方该端点要求 multipart 表单；此处给出骨架（沿用同一鉴权与调用计数收口），
// 真机联调时按官方文档补齐 multipart（附件主存 S3，见架构 §7.5 / ADR-08）。
func (c *HTTPClient) UploadAttachment(ctx context.Context, req UploadAttachmentRequest) (string, error) {
	body, err := json.Marshal(map[string]any{
		"name": req.Name,
		"type": req.Type,
	})
	if err != nil {
		return "", err
	}
	data, _, err := c.doJSON(ctx, http.MethodPost,
		"/open-apis/approval/openapi/v2/file/upload", nil, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	var raw struct {
		Code   string `json:"code"`
		FileID string `json:"file_id"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", err
	}
	return firstNonEmpty(raw.FileID, raw.Code), nil
}

// DownloadAttachment 附件下载。
// GET /open-apis/approval/openapi/v2/file/download
func (c *HTTPClient) DownloadAttachment(ctx context.Context, fileID string) ([]byte, error) {
	if fileID == "" {
		return nil, errEmptyApprovalCode
	}
	query := map[string]string{"file_id": fileID}
	// 下载端点返回文件流；此处骨架以示签名为准（TODO(Q1)：联调时改走二进制读取）。
	_, _, err := c.doJSON(ctx, http.MethodGet,
		"/open-apis/approval/openapi/v2/file/download", query, nil)
	if err != nil {
		return nil, err
	}
	return nil, nil
}
