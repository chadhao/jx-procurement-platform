// Package feishu 是飞书通道适配层（M0）；唯一允许 import 飞书 SDK 的包（架构边界纪律 2）。
//
// 设计纪律（写在代码里）：
//  1. 全案只用 4 个飞书接口 + 1 条长连接：订阅审批事件、取实例详情、批量取实例 ID、附件上传/下载。
//  2. 不存在「创建审批实例」调用路径——本包不暴露任何 create 方法（FR-M0-11 / TC-25）。
//  3. 飞书 DTO 在此转换为内部领域模型后再上抛，领域层不感知飞书 SDK 类型。
package feishu

import (
	"context"
	"time"
)

// FieldValue 实例表单字段值（键值对，不依赖顺序；biz_field 由配置映射得出）。
type FieldValue struct {
	FieldID   string // 飞书表单控件 id（★ 具体值待确认（Q1），原样存储）
	FieldName string // 展示名
	BizField  string // 映射后的业务字段名；空表示未映射（Q1）
	ValueText string // 统一文本化
	ValueType string // text/number/date/attachment/option/user...
	RawJSON   string // 原始片段（保真）
}

// InstanceDetail 实例详情（适配层输出，领域层可直接消费）。
type InstanceDetail struct {
	InstanceCode    string
	ApprovalCode    string
	StatusRaw       string // 飞书原始状态
	BizNo           string // 流水号控件生成的业务单号（自建侧只读）
	ApplicantOpenID string
	ApplicantName   string
	Department      string
	AmountCents     *int64
	PurposeClassL1  string
	PurposeClassL2  string
	Supplier        string
	OccurredAt      time.Time
	Fields          []FieldValue
	FeishuLogID     string
}

// SubscribeResult 单个 approval_code 的订阅结果。
type SubscribeResult struct {
	ApprovalCode string `json:"approval_code"`
	Subscribed   bool   `json:"subscribed"`
	Result       string `json:"result"` // 成功 / 失败码
	Err          string `json:"error,omitempty"`
}

// ListInstanceIDsRequest 批量取实例 ID（对账）请求。
type ListInstanceIDsRequest struct {
	ApprovalCode string
	From         time.Time
	To           time.Time
	PageSize     int
	PageToken    string
}

// ListInstanceIDsResult 批量取实例 ID 结果。
type ListInstanceIDsResult struct {
	InstanceIDs   []string
	NextPageToken string
}

// UploadAttachmentRequest 附件上传请求。
type UploadAttachmentRequest struct {
	Name string
	Type string // 附件类型（如 contract）
	Data []byte
}

// Client 飞书接口抽象（便于 mock 与后续联调）。仅暴露 4 类接口，无创建实例方法。
type Client interface {
	// SubscribeApprovalEvent 订阅审批事件：POST /open-apis/approval/v4/approvals/:approval_code/subscribe
	SubscribeApprovalEvent(ctx context.Context, approvalCode string) (SubscribeResult, error)
	// GetInstanceDetail 取实例详情：GET /open-apis/approval/v4/instances/:instance_id
	GetInstanceDetail(ctx context.Context, instanceCode string) (*InstanceDetail, error)
	// ListInstanceIDs 批量取实例 ID（对账用）：GET /open-apis/approval/v4/instances
	ListInstanceIDs(ctx context.Context, req ListInstanceIDsRequest) (*ListInstanceIDsResult, error)
	// UploadAttachment 附件上传：POST /open-apis/approval/openapi/v2/file/upload
	UploadAttachment(ctx context.Context, req UploadAttachmentRequest) (string, error)
	// DownloadAttachment 附件下载：GET /open-apis/approval/openapi/v2/file/download
	DownloadAttachment(ctx context.Context, fileID string) ([]byte, error)
}
