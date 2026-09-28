package inbox

import (
	"context"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// dispatch_test.go —— 事件分流测试（docs/08 §4.6-b 批次二）：
//   - contact.* 事件 ⇒ org_sync 作业（无 instance_code 也不误判）；
//   - 审批事件 ⇒ fetch_detail 作业（既有路径回归断言）；
//   - 重复投递 ⇒ 幂等命中，不新增作业（事件级唯一 ID 幂等键）。

// contactPayload 构造通讯录事件 2.0 报文。
func contactPayload(eventID, eventType string) []byte {
	return []byte(`{"schema":"2.0","header":{"event_id":"` + eventID + `","event_type":"` + eventType + `"},` +
		`"event":{"object":{"open_id":"ou-x"}}}`)
}

// approvalPayload 构造审批事件 2.0 报文（既有形态）。
func approvalPayload(eventID, instanceCode string) []byte {
	return []byte(`{"schema":"2.0","header":{"event_id":"` + eventID + `","event_type":"approval_instance"},` +
		`"event":{"instance_code":"` + instanceCode + `","status":"PENDING"}}`)
}

// TestDispatchContactEventToOrgSync contact.* ⇒ org_sync 作业。
func TestDispatchContactEventToOrgSync(t *testing.T) {
	for _, et := range []string{
		"contact.department.created_v3", "contact.department.updated_v3", "contact.department.deleted_v3",
		"contact.user.created_v3", "contact.user.updated_v3", "contact.user.deleted_v3",
	} {
		db := storetest.NewDB(t)
		svc := NewService(db, observ.NewMetrics(), observ.NewLogger("error", nil))
		res, err := svc.Handle(context.Background(), contactPayload("ev-"+et, et))
		if err != nil {
			t.Fatalf("%s 处理失败: %v", et, err)
		}
		if res.Duplicate || !res.JobCreated {
			t.Fatalf("%s 应新入库并建作业", et)
		}
		n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_worker_job WHERE job_type = 'org_sync'`)
		if n != 1 {
			t.Fatalf("%s 应产生 1 条 org_sync 作业: %d", et, n)
		}
		nDetail := storetest.Count(t, db, `SELECT COUNT(*) FROM t_worker_job WHERE job_type = 'fetch_detail'`)
		if nDetail != 0 {
			t.Fatalf("%s 不得进 fetch_detail 链: %d", et, nDetail)
		}
	}
}

// TestDispatchApprovalEventUnchanged 审批事件 ⇒ fetch_detail（既有语义回归断言）。
func TestDispatchApprovalEventUnchanged(t *testing.T) {
	db := storetest.NewDB(t)
	svc := NewService(db, observ.NewMetrics(), observ.NewLogger("error", nil))
	res, err := svc.Handle(context.Background(), approvalPayload("ev-appr", "IC-1"))
	if err != nil {
		t.Fatalf("审批事件处理失败: %v", err)
	}
	if !res.JobCreated {
		t.Fatalf("审批事件应建作业")
	}
	n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_worker_job WHERE job_type = 'fetch_detail'`)
	if n != 1 {
		t.Fatalf("审批事件应产生 1 条 fetch_detail 作业: %d", n)
	}
	if nOrg := storetest.Count(t, db, `SELECT COUNT(*) FROM t_worker_job WHERE job_type = 'org_sync'`); nOrg != 0 {
		t.Fatalf("审批事件不得进 org_sync 链: %d", nOrg)
	}
}

// TestDispatchDuplicateEventIdempotent 同一事件重复投递 ⇒ 幂等命中，作业不重复。
// ★ 幂等键 = header.event_id（事件级唯一 ID，不用"实体 id + 状态"——见 idempotent.go 头注教训）。
func TestDispatchDuplicateEventIdempotent(t *testing.T) {
	db := storetest.NewDB(t)
	svc := NewService(db, observ.NewMetrics(), observ.NewLogger("error", nil))
	p := contactPayload("ev-dup", "contact.user.updated_v3")

	first, err := svc.Handle(context.Background(), p)
	if err != nil {
		t.Fatalf("首次投递失败: %v", err)
	}
	second, err := svc.Handle(context.Background(), p)
	if err != nil {
		t.Fatalf("重复投递失败: %v", err)
	}
	if first.Duplicate || !second.Duplicate {
		t.Fatalf("重复投递应命中幂等: first=%v second=%v", first.Duplicate, second.Duplicate)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_worker_job WHERE job_type = 'org_sync'`); n != 1 {
		t.Fatalf("重复投递不得重复建作业: %d", n)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_event_inbox`); n != 1 {
		t.Fatalf("重复投递不得重复落收件箱: %d", n)
	}
}
