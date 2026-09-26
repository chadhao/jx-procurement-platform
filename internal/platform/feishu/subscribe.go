package feishu

import (
	"context"
	"net/http"
	"net/url"
)

// SubscribeApprovalEvent 订阅审批事件（★ 必须先订阅，否则一条事件都不会推送）。
// POST /open-apis/approval/v4/approvals/:approval_code/subscribe
func (c *HTTPClient) SubscribeApprovalEvent(ctx context.Context, approvalCode string) (SubscribeResult, error) {
	result := SubscribeResult{ApprovalCode: approvalCode}
	if approvalCode == "" {
		result.Result = "失败"
		result.Err = "approval_code 为空（★ 具体值待确认（Q1），应由配置表提供）"
		return result, errEmptyApprovalCode
	}
	path := "/open-apis/approval/v4/approvals/" + url.PathEscape(approvalCode) + "/subscribe"
	_, logID, err := c.doJSON(ctx, http.MethodPost, path, nil, nil)
	if err != nil {
		result.Result = "失败"
		result.Err = err.Error()
		c.log.Warn("订阅审批事件失败", "approval_code", approvalCode, "feishu_log_id", logID, "error", err)
		return result, err
	}
	result.Subscribed = true
	result.Result = "成功"
	c.log.Info("订阅审批事件成功", "approval_code", approvalCode, "feishu_log_id", logID)
	return result, nil
}
