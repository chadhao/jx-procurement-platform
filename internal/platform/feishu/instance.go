package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// errEmptyApprovalCode 缺少 approval_code（Q1 未定，配置未装载）。
var errEmptyApprovalCode = errors.New("feishu: approval_code 为空（待确认 Q1）")

// GetInstanceDetail 取实例详情与表单字段。
// GET /open-apis/approval/v4/instances/:instance_id
//
// ★ 联调状态：字段解析按官方审批实例详情结构实现；因尚无真机报文（Q1/Q17），
// 具体控件 id 不做硬编码，form 项原样转换后由配置映射。
func (c *HTTPClient) GetInstanceDetail(ctx context.Context, instanceCode string) (*InstanceDetail, error) {
	path := "/open-apis/approval/v4/instances/" + url.PathEscape(instanceCode)
	data, logID, err := c.doJSON(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}

	var raw struct {
		ApprovalCode string `json:"approval_code"`
		InstanceCode string `json:"instance_code"`
		Status       string `json:"status"`
		SerialNumber string `json:"serial_number"`
		OpenID       string `json:"open_id"`
		UserID       string `json:"user_id"`
		DepartmentID string `json:"department_id"`
		StartTime    string `json:"start_time"`
		Form         string `json:"form"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	det := &InstanceDetail{
		InstanceCode:    firstNonEmpty(raw.InstanceCode, instanceCode),
		ApprovalCode:    raw.ApprovalCode,
		StatusRaw:       raw.Status,
		BizNo:           raw.SerialNumber,
		ApplicantOpenID: firstNonEmpty(raw.OpenID, raw.UserID),
		Fields:          parseForm(raw.Form),
		FeishuLogID:     logID,
	}
	if raw.StartTime != "" {
		if ts, err := strconv.ParseInt(raw.StartTime, 10, 64); err == nil {
			det.OccurredAt = time.Unix(ts, 0).UTC()
		}
	}
	return det, nil
}

// ListInstanceIDs 批量取实例 ID（对账补拉用）。
// GET /open-apis/approval/v4/instances
func (c *HTTPClient) ListInstanceIDs(ctx context.Context, req ListInstanceIDsRequest) (*ListInstanceIDsResult, error) {
	pageSize := req.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 100
	}
	query := map[string]string{
		"approval_code": req.ApprovalCode,
		"start_time":    strconv.FormatInt(req.From.Unix(), 10),
		"end_time":      strconv.FormatInt(req.To.Unix(), 10),
		"page_size":     strconv.Itoa(pageSize),
	}
	if req.PageToken != "" {
		query["page_token"] = req.PageToken
	}
	data, _, err := c.doJSON(ctx, http.MethodGet, "/open-apis/approval/v4/instances", query, nil)
	if err != nil {
		return nil, err
	}
	var raw struct {
		InstanceCodeList []string `json:"instance_code_list"`
		PageToken        string   `json:"page_token"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return &ListInstanceIDsResult{InstanceIDs: raw.InstanceCodeList, NextPageToken: raw.PageToken}, nil
}

// formItem 飞书表单控件项（原始形态）。
type formItem struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

// parseForm 解析 form 字符串（JSON 数组）为键值对字段；解析失败返回空切片而非报错。
func parseForm(form string) []FieldValue {
	if strings.TrimSpace(form) == "" {
		return nil
	}
	var items []formItem
	if err := json.Unmarshal([]byte(form), &items); err != nil {
		return nil
	}
	out := make([]FieldValue, 0, len(items))
	for _, it := range items {
		out = append(out, FieldValue{
			FieldID:   it.ID,
			FieldName: it.Name,
			ValueText: valueToText(it.Value),
			ValueType: it.Type,
			RawJSON:   string(it.Value),
		})
	}
	return out
}

// valueToText 将控件值统一文本化（字符串直取，其余保留 JSON 原文）。
func valueToText(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(v))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
