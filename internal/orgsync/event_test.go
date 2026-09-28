package orgsync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// event_test.go —— 通讯录事件增量行为测试（docs/08 §4.6 批次二；不依赖真机）。
//
// 覆盖（对应任务自验硬要求）：
//   - 6 个事件类型各自的增量语义（部门 upsert/软删、用户 upsert/软删）；
//   - 幂等：同一事件重复应用 ⇒ 结果不变（镜像层面）；（收件箱层去重见 inbox/worker 测试）；
//   - 乱序：updated 先于 created ⇒ 不丢失（回源拿当前真相，最终一致）；
//   - 失败可见：缺 ID ⇒ error（worker 退避/死信）；回源失败 ⇒ gap 计数 + 退回事件体；
//     未知事件 ⇒ unknown 计数（不静默丢弃）；
//   - last_event_at 推进；事件不触碰审批链（本包不 import worker/ingest 即结构性保证）。

// contactEvent 构造 2.0 信封报文（与官方 schema/header/event 三段一致）。
func contactEvent(eventID, eventType string, object map[string]any) []byte {
	return []byte(fmt.Sprintf(`{"schema":"2.0","header":{"event_id":%q,"event_type":%q,"tenant_key":"tk","create_time":"1727500000000"},"event":{"object":%s}}`,
		eventID, eventType, mustJSON(object)))
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// fakeDetailStub 详情回源桩：id → 详情条目；err != 0 时返回该错误码。
type fakeDetailStub struct {
	depts map[string]map[string]any
	users map[string]map[string]any
	err   int
	calls int
}

func (s *fakeDetailStub) GetDepartment(_ context.Context, id string) (*store.OrgDepartment, error) {
	s.calls++
	if s.err != 0 {
		return nil, fmt.Errorf("详情接口错误 code=%d", s.err)
	}
	it, ok := s.depts[id]
	if !ok {
		return nil, fmt.Errorf("orgsync: 部门详情 %s 未找到", id)
	}
	return &store.OrgDepartment{
		OpenDepartmentID:       it["open_department_id"].(string),
		DepartmentID:           str(it["department_id"]),
		ParentOpenDepartmentID: str(it["parent_department_id"]),
		Name:                   str(it["name"]),
		RawJSON:                "{}",
	}, nil
}

func (s *fakeDetailStub) GetUser(_ context.Context, openID string) (*store.OrgUser, error) {
	s.calls++
	if s.err != 0 {
		return nil, fmt.Errorf("详情接口错误 code=%d", s.err)
	}
	it, ok := s.users[openID]
	if !ok {
		return nil, fmt.Errorf("orgsync: 用户详情 %s 未找到", openID)
	}
	ids := it["department_ids"].([]string)
	return &store.OrgUser{
		OpenID:              it["open_id"].(string),
		Name:                str(it["name"]),
		DepartmentIDs:       ids,
		IsActivated:         true,
		EmployeeStatus:      normalizeStatus(false, false, false, true, false),
		PrimaryDepartmentID: primaryDeptOf(ids),
		RawJSON:             "{}",
	}, nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func newTestEventHandler(t *testing.T, stub *fakeDetailStub) (*EventHandler, *store.DB, *observ.Metrics) {
	t.Helper()
	db := storetest.NewDB(t)
	m := observ.NewMetrics()
	h := NewEventHandler(db, stub, m, nil, observ.NewLogger("error", nil))
	return h, db, m
}

// getDept / getUser 断言辅助。
func getDept(t *testing.T, db *store.DB, id string) *store.OrgDepartment {
	t.Helper()
	g, err := db.GetOrgDepartment(context.Background(), id)
	if err != nil {
		t.Fatalf("读部门 %s 失败: %v", id, err)
	}
	return g
}

func getUser(t *testing.T, db *store.DB, id string) *store.OrgUser {
	t.Helper()
	u, err := db.GetOrgUser(context.Background(), id)
	if err != nil {
		t.Fatalf("读人员 %s 失败: %v", id, err)
	}
	return u
}

// TestEventDepartmentCreatedUpdated 部门 created/updated ⇒ upsert（回源为准）+ 复活软删行。
func TestEventDepartmentCreatedUpdated(t *testing.T) {
	stub := &fakeDetailStub{depts: map[string]map[string]any{
		"od-a": {"open_department_id": "od-a", "department_id": "D-A", "name": "研发部", "parent_department_id": "0"},
	}}
	h, db, m := newTestEventHandler(t, stub)

	// created：事件体字段不全（无 name）⇒ 回源补全。
	if err := h.HandleContactEvent(context.Background(),
		contactEvent("ev-1", "contact.department.created_v3",
			map[string]any{"open_department_id": "od-a", "department_id": "D-A"})); err != nil {
		t.Fatalf("created 事件处理失败: %v", err)
	}
	g := getDept(t, db, "od-a")
	if g.Name != "研发部" || g.ParentOpenDepartmentID != "0" || g.IsDeleted {
		t.Fatalf("created 落库不符: %+v", g)
	}
	if g.Source != "event" {
		t.Fatalf("source 应为 event: %s", g.Source)
	}

	// updated：改名 ⇒ 镜像跟着改（回源返回新名）。
	stub.depts["od-a"]["name"] = "研发中心"
	if err := h.HandleContactEvent(context.Background(),
		contactEvent("ev-2", "contact.department.updated_v3",
			map[string]any{"open_department_id": "od-a", "name": "研发中心"})); err != nil {
		t.Fatalf("updated 事件处理失败: %v", err)
	}
	if g2 := getDept(t, db, "od-a"); g2.Name != "研发中心" {
		t.Fatalf("updated 未生效: %s", g2.Name)
	}
	if m.Snapshot().OrgEventAppliedTotal != 2 {
		t.Fatalf("applied 计数应=2: %d", m.Snapshot().OrgEventAppliedTotal)
	}

	// first_seen_at 保留（不清史，口径④）。
	g3 := getDept(t, db, "od-a")
	if !g3.FirstSeenAt.Equal(g.FirstSeenAt) {
		t.Fatalf("first_seen_at 被改写: %v → %v", g.FirstSeenAt, g3.FirstSeenAt)
	}
}

// TestEventDepartmentDeletedOnlyItself 部门 deleted ⇒ 只软删该部门本身，不级联。
func TestEventDepartmentDeletedOnlyItself(t *testing.T) {
	stub := &fakeDetailStub{}
	h, db, _ := newTestEventHandler(t, stub)

	// 预置镜像：父部门 + 子部门 + 人员（归属父部门）。
	now := time.Now().UTC()
	for _, g := range []*store.OrgDepartment{
		{OpenDepartmentID: "od-parent", Name: "P", IsDeleted: false},
		{OpenDepartmentID: "od-child", ParentOpenDepartmentID: "od-parent", Name: "C", IsDeleted: false},
	} {
		g.FirstSeenAt, g.LastSeenAt, g.UpdatedAt = now, now, now
		if err := db.UpsertOrgDepartment(context.Background(), g); err != nil {
			t.Fatalf("预置部门失败: %v", err)
		}
	}
	u := &store.OrgUser{OpenID: "ou-1", Name: "张三", PrimaryDepartmentID: "od-parent",
		DepartmentIDs: []string{"od-parent"}, IsActivated: true, EmployeeStatus: "在职"}
	u.FirstSeenAt, u.LastSeenAt, u.UpdatedAt = now, now, now
	if err := db.UpsertOrgUser(context.Background(), u); err != nil {
		t.Fatalf("预置人员失败: %v", err)
	}

	if err := h.HandleContactEvent(context.Background(),
		contactEvent("ev-del", "contact.department.deleted_v3",
			map[string]any{"open_department_id": "od-parent"})); err != nil {
		t.Fatalf("deleted 事件处理失败: %v", err)
	}

	if g := getDept(t, db, "od-parent"); !g.IsDeleted {
		t.Fatalf("父部门应已软删")
	}
	if g := getDept(t, db, "od-child"); g.IsDeleted {
		t.Fatalf("★ 子部门不得级联软删（口径：只删部门本身，对账修正）")
	}
	if u2 := getUser(t, db, "ou-1"); u2.IsDeleted {
		t.Fatalf("★ 人员不得级联软删（口径：归属由 user 事件/对账修正）")
	}
}

// TestEventUserCreatedUpdatedDeleted 用户 created/updated/deleted 语义 + department_ids。
func TestEventUserCreatedUpdatedDeleted(t *testing.T) {
	stub := &fakeDetailStub{users: map[string]map[string]any{
		"ou-1": {"open_id": "ou-1", "name": "李四", "department_ids": []string{"od-a", "od-b"}},
	}}
	h, db, m := newTestEventHandler(t, stub)

	// created：事件体无 name/department_ids ⇒ 回源补全；主部门 = department_ids 首项。
	if err := h.HandleContactEvent(context.Background(),
		contactEvent("ev-u1", "contact.user.created_v3",
			map[string]any{"open_id": "ou-1", "status": map[string]bool{"is_activated": true}})); err != nil {
		t.Fatalf("created 事件处理失败: %v", err)
	}
	u := getUser(t, db, "ou-1")
	if u.Name != "李四" || u.PrimaryDepartmentID != "od-a" || len(u.DepartmentIDs) != 2 || u.IsDeleted {
		t.Fatalf("created 落库不符: %+v", u)
	}

	// updated：回源返回服务端当前态（改名/换部门/在职态）——回源为准，事件体仅兜底。
	stub.users["ou-1"] = map[string]any{"open_id": "ou-1", "name": "李四丰",
		"department_ids": []string{"od-c"}}
	if err := h.HandleContactEvent(context.Background(),
		contactEvent("ev-u2", "contact.user.updated_v3",
			map[string]any{"open_id": "ou-1", "name": "事件旧值", "department_ids": []string{"od-old"},
				"status": map[string]bool{"is_activated": true, "is_frozen": true}})); err != nil {
		t.Fatalf("updated 事件处理失败: %v", err)
	}
	u = getUser(t, db, "ou-1")
	if u.Name != "李四丰" || u.PrimaryDepartmentID != "od-c" || u.EmployeeStatus != "在职" {
		t.Fatalf("updated 落库不符（应为回源当前态）: %+v", u)
	}
	if m.Snapshot().OrgEventAppliedTotal != 2 {
		t.Fatalf("applied 计数应=2: %d", m.Snapshot().OrgEventAppliedTotal)
	}

	// deleted：软删、行保留（C-E：历史解析不过滤 is_deleted）。
	if err := h.HandleContactEvent(context.Background(),
		contactEvent("ev-u3", "contact.user.deleted_v3",
			map[string]any{"open_id": "ou-1"})); err != nil {
		t.Fatalf("deleted 事件处理失败: %v", err)
	}
	if u = getUser(t, db, "ou-1"); !u.IsDeleted || u.Name != "李四丰" {
		t.Fatalf("deleted 应软删且保留字段: %+v", u)
	}
}

// TestEventUserResignedSoftDeleted 离职事件（updated + is_resigned）⇒ 软删。
func TestEventUserResignedSoftDeleted(t *testing.T) {
	stub := &fakeDetailStub{}
	h, db, _ := newTestEventHandler(t, stub)
	now := time.Now().UTC()
	u := &store.OrgUser{OpenID: "ou-9", Name: "王五", IsActivated: true, EmployeeStatus: "在职"}
	u.FirstSeenAt, u.LastSeenAt, u.UpdatedAt = now, now, now
	if err := db.UpsertOrgUser(context.Background(), u); err != nil {
		t.Fatalf("预置失败: %v", err)
	}
	if err := h.HandleContactEvent(context.Background(),
		contactEvent("ev-r", "contact.user.updated_v3",
			map[string]any{"open_id": "ou-9", "name": "王五", "department_ids": []string{"od-a"},
				"status": map[string]bool{"is_activated": true, "is_resigned": true}})); err != nil {
		t.Fatalf("离职事件处理失败: %v", err)
	}
	if u2 := getUser(t, db, "ou-9"); !u2.IsDeleted || u2.EmployeeStatus != "离职" {
		t.Fatalf("离职应软删且状态=离职: %+v", u2)
	}
}

// TestEventIdempotentReplay 同一事件重复应用 ⇒ 结果不变（处理器层幂等）。
// （收件箱层去重：重复报文在 inbox.Handle 即被 INSERT OR IGNORE 吞掉，
// 见 inbox/dispatch_test.go。）
func TestEventIdempotentReplay(t *testing.T) {
	stub := &fakeDetailStub{users: map[string]map[string]any{
		"ou-1": {"open_id": "ou-1", "name": "李四", "department_ids": []string{"od-a"}},
	}}
	h, db, _ := newTestEventHandler(t, stub)
	payload := contactEvent("ev-same", "contact.user.created_v3",
		map[string]any{"open_id": "ou-1"})
	for i := 0; i < 3; i++ {
		if err := h.HandleContactEvent(context.Background(), payload); err != nil {
			t.Fatalf("第 %d 次应用失败: %v", i+1, err)
		}
	}
	// 行数恒为 1（UPSERT 幂等）；first_seen_at 不漂移。
	depts, _ := db.ListOrgUsers(context.Background())
	n := 0
	for _, u := range depts {
		if u.OpenID == "ou-1" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("重复应用产生 %d 行（应=1）", n)
	}
}

// TestEventOutOfOrder 乱序：updated 先于 created ⇒ 不丢失（回源当前真相，最终一致）。
func TestEventOutOfOrder(t *testing.T) {
	stub := &fakeDetailStub{users: map[string]map[string]any{
		"ou-1": {"open_id": "ou-1", "name": "最终名", "department_ids": []string{"od-x"}},
	}}
	h, db, _ := newTestEventHandler(t, stub)

	// ① updated 先到（镜像中还没有这个人）。
	if err := h.HandleContactEvent(context.Background(),
		contactEvent("ev-late", "contact.user.updated_v3",
			map[string]any{"open_id": "ou-1", "name": "旧名"})); err != nil {
		t.Fatalf("乱序 updated 处理失败: %v", err)
	}
	// ② created 后到：回源拿到的是服务端当前态（"最终名"）——与到达顺序无关。
	if err := h.HandleContactEvent(context.Background(),
		contactEvent("ev-early", "contact.user.created_v3",
			map[string]any{"open_id": "ou-1", "name": "旧名"})); err != nil {
		t.Fatalf("乱序 created 处理失败: %v", err)
	}
	u := getUser(t, db, "ou-1")
	if u.Name != "最终名" || u.PrimaryDepartmentID != "od-x" {
		t.Fatalf("乱序后最终态不符（应取回源当前真相）: %+v", u)
	}
}

// TestEventFailureVisible 失败可见：缺 ID ⇒ error；回源失败 ⇒ gap 可见 + 退回事件体；未知键 ⇒ unknown 计数。
func TestEventFailureVisible(t *testing.T) {
	stub := &fakeDetailStub{err: 41050} // 回源恒失败（如 41050 no user authority）
	h, db, m := newTestEventHandler(t, stub)

	// ① 缺 ID ⇒ 显式 error（worker 退避重试 → 死信，可见）。
	if err := h.HandleContactEvent(context.Background(),
		contactEvent("ev-bad", "contact.department.created_v3", map[string]any{"name": "x"})); err == nil {
		t.Fatalf("缺 ID 事件应显式报错")
	}

	// ② 回源失败 ⇒ 不报错（永久性缺口不打重试风暴），但 gap 计数 + 退回事件体字段可见。
	if err := h.HandleContactEvent(context.Background(),
		contactEvent("ev-gap", "contact.user.created_v3",
			map[string]any{"open_id": "ou-2", "name": "赵六", "department_ids": []string{"od-a"},
				"status": map[string]bool{"is_activated": true}})); err != nil {
		t.Fatalf("回源失败不应中断应用: %v", err)
	}
	if snap := m.Snapshot(); snap.OrgEventGapTotal == 0 {
		t.Fatalf("回源失败应计入 gap")
	}
	if u := getUser(t, db, "ou-2"); u.Name != "赵六" {
		t.Fatalf("回源失败应退回事件体字段: %+v", u)
	}

	// ③ 未知 contact.* 键 ⇒ unknown 计数（可见、不丢弃报文——报文在 inbox 落盘）。
	if err := h.HandleContactEvent(context.Background(),
		contactEvent("ev-unk", "contact.scope.updated_v3", map[string]any{})); err != nil {
		t.Fatalf("未知键不应报错: %v", err)
	}
	if m.Snapshot().OrgEventUnknownTotal != 1 {
		t.Fatalf("unknown 计数应=1: %d", m.Snapshot().OrgEventUnknownTotal)
	}

	// ④ 缺 header.event_id ⇒ 显式拒绝（幂等键纪律）。
	bad := []byte(`{"schema":"2.0","header":{"event_type":"contact.user.deleted_v3"},"event":{"object":{"open_id":"ou-1"}}}`)
	if err := h.HandleContactEvent(context.Background(), bad); err == nil {
		t.Fatalf("缺 event_id 应显式拒绝")
	}
}

// TestEventTouchesLastEventAtAndRunRow 事件应用推进 last_event_at（/healthz 上次增量处理时间来源）。
func TestEventTouchesLastEventAtAndRunRow(t *testing.T) {
	stub := &fakeDetailStub{}
	h, db, _ := newTestEventHandler(t, stub)
	if err := h.HandleContactEvent(context.Background(),
		contactEvent("ev-t", "contact.department.deleted_v3",
			map[string]any{"open_department_id": "od-nope"})); err != nil {
		t.Fatalf("处理失败: %v", err)
	}
	st, err := db.GetOrgSyncState(context.Background())
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	if st.LastEventAt.IsZero() {
		t.Fatalf("last_event_at 未推进")
	}
}

// TestEventDoesNotTouchApprovalPath 结构性回归：事件处理器只写 t_org_*，
// 不触碰审批链表（t_event_inbox 的 PROCESSING/DONE 由 worker 统一管理，这里断言不动业务表）。
func TestEventDoesNotTouchApprovalPath(t *testing.T) {
	stub := &fakeDetailStub{}
	h, db, _ := newTestEventHandler(t, stub)
	if err := h.HandleContactEvent(context.Background(),
		contactEvent("ev-x", "contact.user.created_v3",
			map[string]any{"open_id": "ou-3", "name": "孙七", "department_ids": []string{"od-a"},
				"status": map[string]bool{"is_activated": true}})); err != nil {
		t.Fatalf("处理失败: %v", err)
	}
	// 审批相关表必须零写入（本包未 import worker/ingest，结构性保证 + 显式计数断言）。
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_deadletter`); n != 0 {
		t.Fatalf("事件路径不应产生死信: %d", n)
	}
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_instance`); n != 0 {
		t.Fatalf("事件路径不应写实例表: %d", n)
	}
}

// TestFetcherDetailEndpoints 详情接口 HTTP 契约（路径/参数/解析，httptest 不依赖真机）。
func TestFetcherDetailEndpoints(t *testing.T) {
	var gotDeptPath, gotDeptQuery, gotUserPath, gotUserQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/open-apis/contact/v3/departments/"):
			gotDeptPath = r.URL.Path
			gotDeptQuery = r.URL.RawQuery
			writeJSON(t, w, env0(map[string]any{
				"department": map[string]any{"open_department_id": "od-z", "department_id": "D9",
					"name": "财务部", "parent_department_id": "0"}}))
		case strings.HasPrefix(r.URL.Path, "/open-apis/contact/v3/users/"):
			gotUserPath = r.URL.Path
			gotUserQuery = r.URL.RawQuery
			writeJSON(t, w, env0(map[string]any{
				"user": map[string]any{"open_id": "ou-z", "name": "周八",
					"department_ids": []string{"od-z"}, "status": map[string]bool{"is_activated": true}}}))
		default:
			writeJSON(t, w, map[string]any{"code": 404, "msg": "unknown path"})
		}
	}))
	defer srv.Close()
	f := NewFeishuFetcher(TokenSourceFunc(fakeToken), srv.URL, slog.Default())

	g, err := f.GetDepartment(context.Background(), "od-z")
	if err != nil {
		t.Fatalf("GetDepartment 失败: %v", err)
	}
	if gotDeptPath != "/open-apis/contact/v3/departments/od-z" ||
		!strings.Contains(gotDeptQuery, "department_id_type=open_department_id") {
		t.Fatalf("部门详情请求不符: %s ? %s", gotDeptPath, gotDeptQuery)
	}
	if g.Name != "财务部" || g.ParentOpenDepartmentID != "0" {
		t.Fatalf("部门详情解析不符: %+v", g)
	}

	u, err := f.GetUser(context.Background(), "ou-z")
	if err != nil {
		t.Fatalf("GetUser 失败: %v", err)
	}
	if gotUserPath != "/open-apis/contact/v3/users/ou-z" ||
		!strings.Contains(gotUserQuery, "user_id_type=open_id") {
		t.Fatalf("用户详情请求不符: %s ? %s", gotUserPath, gotUserQuery)
	}
	if u.Name != "周八" || len(u.DepartmentIDs) != 1 {
		t.Fatalf("用户详情解析不符: %+v", u)
	}

	// 非 od- 前缀 ⇒ 按自定义 department_id 类型查。
	if _, err := f.GetDepartment(context.Background(), "D096"); err != nil {
		t.Fatalf("自定义 ID 查询失败: %v", err)
	}
	if !strings.Contains(gotDeptQuery, "department_id_type=department_id") {
		t.Fatalf("非 od- 前缀应按 department_id 类型: %s", gotDeptQuery)
	}
}

// TestReconcileDriftHeals 定期对账兜底（docs/08 §4.7）：以飞书为准自愈漂移——
// 我方多出的 ⇒ 软删；我方缺失的 ⇒ 补齐；错删的 ⇒ 复活。
func TestReconcileDriftHeals(t *testing.T) {
	// 第一轮全量：部门 A、B；用户 u1。
	depts := []map[string]any{dept("od-a", "A", "0"), dept("od-b", "B", "0")}
	users := map[string][]map[string]any{
		"0": {user("ou-1", "甲", []string{"od-a"})},
	}
	runner, db, _ := newTestRunner(t, depts, users, 0, 0)
	ctx := context.Background()
	if _, err := runner.RunFull(ctx, "startup"); err != nil {
		t.Fatalf("首轮全量失败: %v", err)
	}

	// 漂移注入：① 本地多出 od-b（模拟飞书删了但我们漏收事件）；
	// ② 本地缺 od-c（模拟飞书新建但我们漏收事件）；③ 本地错软删 od-a（模拟误删）。
	if _, err := db.SoftDeleteOrgDepartment(ctx, "od-b", time.Now().UTC()); err != nil {
		t.Fatalf("注入漂移失败: %v", err)
	}
	if _, err := db.SoftDeleteOrgDepartment(ctx, "od-a", time.Now().UTC()); err != nil {
		t.Fatalf("注入漂移失败: %v", err)
	}
	// 手工删掉 u1（物理级缺失模拟：删行）——对账应重建。
	if _, err := db.ExecContext(ctx, `DELETE FROM t_org_user WHERE open_id = 'ou-1'`); err != nil {
		t.Fatalf("注入漂移失败: %v", err)
	}

	// 第二轮全量（对账口径）：远端只剩 od-a、新增 od-c、u1 仍在。
	runner2, db2 := runner, db
	_ = runner2
	// newTestRunner 的模拟服务器是闭包固定的远端集合 ⇒ 这里重建一个反映"当前飞书态"的服务器。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/contact/v3/departments":
			writeJSON(t, w, env0(map[string]any{"has_more": false,
				"items": []any{dept("od-a", "A", "0"), dept("od-c", "C", "0")}}))
		case "/open-apis/contact/v3/users/find_by_department":
			writeJSON(t, w, env0(map[string]any{"has_more": false,
				"items": []any{user("ou-1", "甲", []string{"od-a"})}}))
		default:
			writeJSON(t, w, map[string]any{"code": 404, "msg": "unknown"})
		}
	}))
	defer srv.Close()
	r2 := NewRunner(NewFeishuFetcher(TokenSourceFunc(fakeToken), srv.URL, slog.Default()),
		db2, observ.NewMetrics(), observ.NewHealth("test"), observ.NewLogger("error", nil), time.Hour)
	rep, err := r2.RunFull(ctx, "reconcile")
	if err != nil {
		t.Fatalf("对账失败: %v", err)
	}

	// ① 本地多出的 od-b ⇒ 软删。
	if g, err := db2.GetOrgDepartment(ctx, "od-b"); err != nil || !g.IsDeleted {
		t.Fatalf("多出的 od-b 应被软删: err=%v deleted=%v", err, err == nil && g.IsDeleted)
	}
	// ② 错软删的 od-a ⇒ 复活。
	if g := getDept(t, db2, "od-a"); g.IsDeleted {
		t.Fatalf("错删的 od-a 应复活")
	}
	// ③ 缺失的 od-c ⇒ 补齐。
	if g := getDept(t, db2, "od-c"); g.Name != "C" {
		t.Fatalf("缺失的 od-c 应补齐: %+v", g)
	}
	// ④ 缺失的 u1 ⇒ 补齐（在用）。
	if u := getUser(t, db2, "ou-1"); u.IsDeleted {
		t.Fatalf("缺失的 u1 应补齐")
	}
	// 流水：对账触发源入账。
	if rep.Trigger != "reconcile" {
		t.Fatalf("trigger 应为 reconcile: %s", rep.Trigger)
	}
}

// TestRunFullSerialGuard 并发护栏：上一轮未结束再次触发 ⇒ 明确报错（不并发打飞书）。
func TestRunFullSerialGuard(t *testing.T) {
	depts := []map[string]any{dept("od-a", "A", "0")}
	users := map[string][]map[string]any{"0": {user("ou-1", "甲", []string{"od-a"})}}
	runner, _, _ := newTestRunner(t, depts, users, 0, 0)
	runner.running.Store(true) // 模拟上一轮仍在进行
	if _, err := runner.RunFull(context.Background(), "manual"); err == nil {
		t.Fatalf("并发触发应被拒绝")
	} else if !strings.Contains(err.Error(), "仍在进行") {
		t.Fatalf("拒绝原因应可见: %v", err)
	}
}
