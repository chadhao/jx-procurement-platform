package inbox

import (
	"errors"
	"testing"
)

// TestExtractIdempotencyKey 覆盖幂等键提取三种情形：2.0 版 / 1.0 版 / 缺失（TC-01 前置纪律）。
func TestExtractIdempotencyKey(t *testing.T) {
	cases := []struct {
		name       string
		payload    string
		wantKey    string
		wantVer    string
		wantInst   string
		wantType   string
		wantStatus string
		wantErr    bool
	}{
		{
			name: "2.0 版取 header.event_id",
			payload: `{"schema":"2.0","header":{"event_id":"ev_2_0001",
				"event_type":"approval.instance.status_changed_v4"},
				"event":{"instance_code":"INST-0001","status":"APPROVED","approval_code":"TODO(Q1)"}}`,
			wantKey:    "ev_2_0001",
			wantVer:    "2.0",
			wantInst:   "INST-0001",
			wantType:   "approval.instance.status_changed_v4",
			wantStatus: "APPROVED",
		},
		{
			name: "1.0 版取顶层 uuid",
			payload: `{"ts":"2026-09-26T08:30:00Z","uuid":"uuid-1-0001","token":"t",
				"type":"approval_instance","event":{"instance_code":"INST-0002","status":"PENDING"}}`,
			wantKey:    "uuid-1-0001",
			wantVer:    "1.0",
			wantInst:   "INST-0002",
			wantType:   "approval_instance",
			wantStatus: "PENDING",
		},
		{
			name:    "缺失事件 ID → 拒绝（不退化为时间戳兜底）",
			payload: `{"event":{"instance_code":"INST-0003","status":"APPROVED"}}`,
			wantErr: true,
		},
		{
			name:    "2.0 header 存在但 event_id 为空 → 拒绝",
			payload: `{"schema":"2.0","header":{"event_id":"","event_type":"x"},"event":{"instance_code":"I"}}`,
			wantErr: true,
		},
		{
			name:    "空报文 → 拒绝",
			payload: ``,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := Extract([]byte(tc.payload))
			if tc.wantErr {
				if !errors.Is(err, ErrMissingEventID) {
					t.Fatalf("期望 ErrMissingEventID，实际 err=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不期望错误: %v", err)
			}
			if ev.IdemKey != tc.wantKey {
				t.Errorf("IdemKey = %q, 期望 %q", ev.IdemKey, tc.wantKey)
			}
			if ev.SchemaVersion != tc.wantVer {
				t.Errorf("SchemaVersion = %q, 期望 %q", ev.SchemaVersion, tc.wantVer)
			}
			if ev.InstanceCode != tc.wantInst {
				t.Errorf("InstanceCode = %q, 期望 %q", ev.InstanceCode, tc.wantInst)
			}
			if ev.EventType != tc.wantType {
				t.Errorf("EventType = %q, 期望 %q", ev.EventType, tc.wantType)
			}
			if ev.Status != tc.wantStatus {
				t.Errorf("Status = %q, 期望 %q", ev.Status, tc.wantStatus)
			}
		})
	}
}
