package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// 附件（**只做下载**，不做上传）
//
// ★ 模式 A 下「上传」路径不需要：本系统**不创建审批实例**，附件由申请人在**飞书侧**上传，
// 本系统只是**接收方** —— 需要的是「按 file_id 取回文件字节」并落主存（S3 主 + RustFS 备）。
// 上传接口留作**模式 B 的遗留**，不接业务（无任何调用点）。
//
// ★ 为什么不放在入站同步路径上：事件处理有 **3 秒窗口**，而下载附件是网络 IO。
// 故设计为**按需拉取**：用户/凭证包需要时再取，取到后缓存到对象存储（`internal/objectstore`）。

// DownloadAttachment 附件下载。
// GET /open-apis/approval/v4/approvals/:approval_code/... 之外，附件走 openapi v2：
// GET /open-apis/approval/openapi/v2/file/download?file_id=xxx
//
// 响应体是**文件流**（不是统一 JSON 包裹），故不能走 doJSON；成功时直接返回字节。
// 失败时飞书会返回 JSON 错误体，这里识别并转成带 code/msg 的错误。
func (c *HTTPClient) DownloadAttachment(ctx context.Context, fileID string) ([]byte, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return nil, fmt.Errorf("feishu: 附件下载缺少 file_id")
	}
	token, err := c.tokens.get(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/open-apis/approval/openapi/v2/file/download", nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("file_id", fileID)
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Authorization", "Bearer "+token)

	c.metrics.IncFeishuAPICall()
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("feishu: 下载附件 %s 失败: %w", fileID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// 失败时返回 JSON 错误体（含 code/msg）；成功时是二进制文件流。
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK || strings.Contains(ct, "application/json") {
		var env apiEnvelope
		if json.Unmarshal(raw, &env) == nil && env.Code != 0 {
			return nil, fmt.Errorf("feishu: 下载附件返回错误 code=%d msg=%s", env.Code, env.Msg)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("feishu: 下载附件 HTTP %d", resp.StatusCode)
		}
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("feishu: 下载附件 %s 得到空内容", fileID)
	}
	return raw, nil
}
