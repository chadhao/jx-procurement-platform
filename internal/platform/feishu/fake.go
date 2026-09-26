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
