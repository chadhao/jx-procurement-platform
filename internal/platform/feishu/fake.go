package feishu

import (
	"context"
	"errors"
	"time"
)

// FakeClient 是 Client 的内存测试替身（供单测与无凭据的端到端验证使用）。
// 通过设定各 Fn 钩子控制行为；未设定的钩子返回安全默认值。
type FakeClient struct {
	SubscribeFn func(ctx context.Context, approvalCode string) (SubscribeResult, error)
	DetailFn    func(ctx context.Context, instanceCode string) (*InstanceDetail, error)
	ListIDsFn   func(ctx context.Context, req ListInstanceIDsRequest) (*ListInstanceIDsResult, error)
	UploadFn    func(ctx context.Context, req UploadAttachmentRequest) (string, error)
	DownloadFn  func(ctx context.Context, fileID string) ([]byte, error)
}

var _ Client = (*FakeClient)(nil)

// SubscribeApprovalEvent 默认视为订阅成功。
func (f *FakeClient) SubscribeApprovalEvent(ctx context.Context, approvalCode string) (SubscribeResult, error) {
	if f.SubscribeFn != nil {
		return f.SubscribeFn(ctx, approvalCode)
	}
	return SubscribeResult{ApprovalCode: approvalCode, Subscribed: true, Result: "成功"}, nil
}

// GetInstanceDetail 默认返回空详情。
func (f *FakeClient) GetInstanceDetail(ctx context.Context, instanceCode string) (*InstanceDetail, error) {
	if f.DetailFn != nil {
		return f.DetailFn(ctx, instanceCode)
	}
	return &InstanceDetail{InstanceCode: instanceCode, OccurredAt: time.Now().UTC()}, nil
}

// ListInstanceIDs 默认返回空列表。
func (f *FakeClient) ListInstanceIDs(ctx context.Context, req ListInstanceIDsRequest) (*ListInstanceIDsResult, error) {
	if f.ListIDsFn != nil {
		return f.ListIDsFn(ctx, req)
	}
	return &ListInstanceIDsResult{}, nil
}

// UploadAttachment 默认返回占位 file_id。
func (f *FakeClient) UploadAttachment(ctx context.Context, req UploadAttachmentRequest) (string, error) {
	if f.UploadFn != nil {
		return f.UploadFn(ctx, req)
	}
	return "fake_file_id", nil
}

// DownloadAttachment 默认返回空字节。
func (f *FakeClient) DownloadAttachment(ctx context.Context, fileID string) ([]byte, error) {
	if f.DownloadFn != nil {
		return f.DownloadFn(ctx, fileID)
	}
	return nil, errors.New("feishu: FakeClient 未配置 DownloadFn")
}

// NewDevClient 返回开发模式用的内存客户端：按 instance_code 生成确定性样例详情，
// 使 DEV_MODE 下 POST /internal/dev/inject-event 注入的事件可端到端落库（不依赖飞书凭据）。
// ★ 仅开发/本地验证使用，绝不用于生产（生产走 NewHTTPClient）。
func NewDevClient() *FakeClient {
	return &FakeClient{
		DetailFn: func(_ context.Context, instanceCode string) (*InstanceDetail, error) {
			amount := int64(480000)
			return &InstanceDetail{
				InstanceCode:    instanceCode,
				ApprovalCode:    "ac-dev",
				StatusRaw:       "PENDING",
				BizNo:           "PR-2609-0001",
				ApplicantOpenID: "ou_applicant",
				ApplicantName:   "张三",
				Department:      "生产部",
				AmountCents:     &amount,
				PurposeClassL1:  "生产采购",
				PurposeClassL2:  "备品备件",
				Supplier:        "开发供应商",
				OccurredAt:      time.Now().UTC(),
				Fields: []FieldValue{
					{FieldID: "f_amount", FieldName: "预估总金额", ValueText: "4800.00", ValueType: "number"},
					{FieldID: "f_assigned", FieldName: "指定采购经办人", ValueText: "ou_dev_handler", ValueType: "user"},
				},
			}, nil
		},
	}
}
