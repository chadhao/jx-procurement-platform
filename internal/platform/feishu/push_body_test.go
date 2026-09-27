package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// push_body_test.go —— 2026-09-28 联调实测缺陷（99992402 field validation failed）回归：
//
//	推实例的 task_list[*] 缺 links / create_time / end_time / update_time（均必填），
//	且审批人字段名错用 assignee_open_id（官方是 open_id，未知字段被静默忽略 ⇒ 任务不被指派）；
//	★ task_list[*] 补齐后，下一层暴露 **实例级（顶层）** 缺 start_time / end_time /
//	i18n_resources（同样 99992402）——两层必填项独立，须逐层核对（见 InstanceSnapshot 注释）。
//
// 本文件三层锁死：
//  1. 纯 BuildSnapshot 层：task 级字段齐、时间戳为 Unix 毫秒**字符串**、end_time 语义；
//     ★ 实例级 start_time / end_time / open_id / i18n_resources（数组形态、texts 齐备）；
//  2. httptest 假飞书端点层：断言**实际发出的 body**（防「结构体对了但 body 组装漏字段」）；
//  3. 回归：第 6 批 task_list[*] 的成果不回退。

var (
	snapCreatedAt = time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	snapUpdatedAt = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	snapClosedAt  = time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC)
)

const testDetailBase = "https://jx.example.com"

// isMilliString 断言 s 是纯数字（Unix 毫秒时间戳字符串形态）。
func isMilliString(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}

// TestBuildSnapshotRequiredFields 实例级 + task 级必填字段的纯结构层断言。
func TestBuildSnapshotRequiredFields(t *testing.T) {
	bizNo := "PR-2609-0009"
	inst := &store.Instance{
		BizNo: bizNo, ApprovalCode: "code-pr", InstanceCode: "app:" + bizNo,
		DocType: "PR", UpdateTime: 3, Status: "PENDING",
		CreatedAt: snapCreatedAt, UpdatedAt: snapUpdatedAt, ApplicantOpenID: "ou_user",
	}
	tasks := []store.FlowTask{
		// 未终态：end_time 必须为 "0"。
		{TaskID: "t-pending", NodeID: "n1", AssigneeOpenID: "ou_a", Status: "PENDING",
			ReleaseState: "RELEASED", CreatedAt: snapCreatedAt, UpdatedAt: snapUpdatedAt},
		// 终态 + closed_at：end_time ＝ closed_at 毫秒。
		{TaskID: "t-closed", NodeID: "n2", AssigneeOpenID: "ou_b", Status: "APPROVED",
			ReleaseState: "RELEASED", CreatedAt: snapCreatedAt, UpdatedAt: snapUpdatedAt,
			ClosedAt: &snapClosedAt},
		// 终态但 closed_at 缺失：退化取 updated_at（数据异常语义，值形态仍须正确）。
		{TaskID: "t-closed-no-ts", NodeID: "n3", AssigneeOpenID: "ou_c", Status: "REJECTED",
			ReleaseState: "RELEASED", CreatedAt: snapCreatedAt, UpdatedAt: snapUpdatedAt},
	}
	snap, err := BuildSnapshot(inst, tasks, nil, testDetailBase, "采购申请审批", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.TaskList) != 3 {
		t.Fatalf("task_list = %d 条, 期望 3", len(snap.TaskList))
	}

	// ① 时间戳形态：Unix 毫秒字符串（非 ISO8601）；值来源正确。
	if got := snap.TaskList[0].CreateTime; got != strconv.FormatInt(snapCreatedAt.UnixMilli(), 10) {
		t.Errorf("create_time = %q, 期望 %q（ISO8601 严禁直塞）", got, strconv.FormatInt(snapCreatedAt.UnixMilli(), 10))
	}
	if got := snap.TaskList[0].UpdateTime; got != strconv.FormatInt(snapUpdatedAt.UnixMilli(), 10) {
		t.Errorf("update_time = %q, 期望 updated_at 毫秒 %q", got, strconv.FormatInt(snapUpdatedAt.UnixMilli(), 10))
	}
	if got := snap.TaskList[0].EndTime; got != "0" {
		t.Errorf("未终态任务 end_time = %q, 期望 \"0\"", got)
	}
	if got := snap.TaskList[1].EndTime; got != strconv.FormatInt(snapClosedAt.UnixMilli(), 10) {
		t.Errorf("终态任务 end_time = %q, 期望 closed_at 毫秒 %q", got, strconv.FormatInt(snapClosedAt.UnixMilli(), 10))
	}
	if got := snap.TaskList[2].EndTime; got != strconv.FormatInt(snapUpdatedAt.UnixMilli(), 10) {
		t.Errorf("终态缺 closed_at 时 end_time = %q, 期望退化 updated_at 毫秒 %q", got, strconv.FormatInt(snapUpdatedAt.UnixMilli(), 10))
	}

	// ② 审批人字段：结构体 JSON tag 必须是 open_id，且不含历史错名 assignee_open_id（防回退）。
	raw, err := json.Marshal(snap.TaskList[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"open_id":"ou_a"`) {
		t.Errorf("task JSON 应含 open_id=ou_a（官方字段名），实际: %s", raw)
	}
	if strings.Contains(string(raw), "assignee_open_id") {
		t.Errorf("task JSON 不得再含历史错名 assignee_open_id（静默不指派缺陷），实际: %s", raw)
	}

	// ③ task 级 links：pc_link + mobile_link，指向 <detailBase>/approval/<biz_no>。
	wantDetail := testDetailBase + "/approval/" + bizNo
	for i, wantID := range []string{"ou_a", "ou_b", "ou_c"} {
		lnk := snap.TaskList[i].Links
		if lnk.PCLink != wantDetail || lnk.MobileLink != wantDetail {
			t.Errorf("task[%d](%s) links = %+v, 期望 pc/mobile 均为 %q", i, wantID, lnk, wantDetail)
		}
	}
	// ④ 实例级 links：同构同源（externalLinks 统一生成）。
	if snap.Links.PCLink != wantDetail || snap.Links.MobileLink != wantDetail {
		t.Errorf("实例级 links = %+v, 期望 pc/mobile 均为 %q", snap.Links, wantDetail)
	}
	// ⑤ action_context 形态不变（回调侧同批约定）。
	if got := snap.TaskList[0].ActionContext; got != `{"biz_no":"`+bizNo+`","task_id":"t-pending"}` {
		t.Errorf("action_context = %q, 期望压缩 JSON {biz_no,task_id}", got)
	}

	// ⑥ 实例级 start_time（官方必填）：created_at 的 Unix 毫秒**字符串**（非 ISO8601）。
	if got := snap.StartTime; got != strconv.FormatInt(snapCreatedAt.UnixMilli(), 10) {
		t.Errorf("start_time = %q, 期望 created_at 毫秒 %q（ISO8601 严禁直塞）",
			got, strconv.FormatInt(snapCreatedAt.UnixMilli(), 10))
	}
	// ⑦ 实例级 end_time（官方必填）：PENDING 未终态 ⇒ "0"。
	if got := snap.EndTime; got != "0" {
		t.Errorf("未终态实例 end_time = %q, 期望 \"0\"", got)
	}
	// ⑧ 实例级发起人 open_id（官方 open_id/user_id 二选一必传）。
	if got := snap.OpenID; got != "ou_user" {
		t.Errorf("实例级 open_id = %q, 期望 ou_user（t_instance.applicant_open_id）", got)
	}
	// ⑨ 实例级 i18n_resources（官方必填；★ 数组形态，texts[].key/value 齐备）。
	if len(snap.I18nResources) != 1 {
		t.Fatalf("i18n_resources = %d 项, 期望 1", len(snap.I18nResources))
	}
	res := snap.I18nResources[0]
	if res.Locale != "zh-CN" || !res.IsDefault {
		t.Errorf("i18n_resources[0] = {locale:%s,is_default:%v}, 期望 zh-CN/true（官方枚举＋显式默认）",
			res.Locale, res.IsDefault)
	}
	if len(res.Texts) != 1 || res.Texts[0].Key != I18nKeyInstanceTitle || res.Texts[0].Value != "采购申请审批" {
		t.Errorf("i18n_resources[0].texts = %+v, 期望 [{key:%s,value:采购申请审批}]（数组形态）",
			res.Texts, I18nKeyInstanceTitle)
	}
}

// TestBuildSnapshotInstanceEndTimeTerminal 实例级 end_time 的终态语义（官方：必填，未结束为 0）：
//
//	APPROVED/REJECTED ⇒ updated_at 毫秒；CANCELED ⇒ cancel_at 优先、缺则退化 updated_at；
//	终态且无任何结束时刻 ⇒ "0"（不编造时间）；非终态 ⇒ "0"。
func TestBuildSnapshotInstanceEndTimeTerminal(t *testing.T) {
	cancelAt := snapClosedAt
	base := store.Instance{
		BizNo: "PR-1", ApprovalCode: "c", InstanceCode: "app:PR-1", UpdateTime: 1,
		CreatedAt: snapCreatedAt, UpdatedAt: snapUpdatedAt,
	}
	wantUpdated := strconv.FormatInt(snapUpdatedAt.UnixMilli(), 10)
	wantCancel := strconv.FormatInt(cancelAt.UnixMilli(), 10)
	cases := []struct {
		name string
		mut  func(*store.Instance)
		want string
	}{
		{"APPROVED 取 updated_at", func(i *store.Instance) { i.Status = "APPROVED" }, wantUpdated},
		{"REJECTED 取 updated_at", func(i *store.Instance) { i.Status = "REJECTED" }, wantUpdated},
		{"CANCELED 取 cancel_at", func(i *store.Instance) {
			i.Status = "CANCELED"
			i.CancelAt = &cancelAt
		}, wantCancel},
		{"CANCELED 缺 cancel_at 退化 updated_at", func(i *store.Instance) { i.Status = "CANCELED" }, wantUpdated},
		{"终态且无任何结束时刻退 0", func(i *store.Instance) {
			i.Status = "APPROVED"
			i.UpdatedAt = time.Time{}
		}, "0"},
		{"非终态为 0", func(i *store.Instance) { i.Status = "PENDING" }, "0"},
	}
	for _, tc := range cases {
		inst := base
		tc.mut(&inst)
		snap, err := BuildSnapshot(&inst, nil, nil, testDetailBase, "名称", nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := snap.EndTime; got != tc.want {
			t.Errorf("%s: end_time = %q, 期望 %q", tc.name, got, tc.want)
		}
	}
}

// TestBuildSnapshotInstanceStartTimeZero 实例缺 created_at ⇒ start_time 退 "0"
// （不编造时间；feishuMilli 零值纪律）。
func TestBuildSnapshotInstanceStartTimeZero(t *testing.T) {
	inst := &store.Instance{BizNo: "PR-2", ApprovalCode: "c", InstanceCode: "app:PR-2",
		Status: "PENDING", DocType: "PR"}
	snap, err := BuildSnapshot(inst, nil, nil, testDetailBase, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.StartTime; got != "0" {
		t.Errorf("缺 created_at 时 start_time = %q, 期望 \"0\"", got)
	}
}

// TestBuildSnapshotInstanceI18nFallback 定义缺失 ⇒ i18n 文案明确降级（DocType → BizNo），
// 绝不静默发空（i18n_resources 官方必填）。
func TestBuildSnapshotInstanceI18nFallback(t *testing.T) {
	inst := &store.Instance{BizNo: "PR-3", ApprovalCode: "c", InstanceCode: "app:PR-3",
		Status: "PENDING", DocType: "PR"}
	// ① defName 缺 ⇒ 降级取 DocType。
	snap, err := BuildSnapshot(inst, nil, nil, testDetailBase, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.I18nResources) != 1 || len(snap.I18nResources[0].Texts) != 1 {
		t.Fatalf("i18n_resources = %+v, 期望 1 项 1 text", snap.I18nResources)
	}
	if got := snap.I18nResources[0].Texts[0].Value; got != "PR" {
		t.Errorf("降级文案 = %q, 期望 DocType \"PR\"", got)
	}
	// ② DocType 也缺 ⇒ 降级取 BizNo。
	inst2 := &store.Instance{BizNo: "PR-4", ApprovalCode: "c", InstanceCode: "app:PR-4", Status: "PENDING"}
	snap2, err := BuildSnapshot(inst2, nil, nil, testDetailBase, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap2.I18nResources[0].Texts[0].Value; got != "PR-4" {
		t.Errorf("降级文案 = %q, 期望 BizNo \"PR-4\"", got)
	}
}

// TestBuildSnapshotEmptyDetailBaseFails detailBase 为空 ⇒ 可见失败（links 必填，不编造 URL）。
func TestBuildSnapshotEmptyDetailBaseFails(t *testing.T) {
	inst := &store.Instance{BizNo: "PR-1", Status: "PENDING"}
	tasks := []store.FlowTask{{TaskID: "t1", ReleaseState: "RELEASED", Status: "PENDING"}}
	if _, err := BuildSnapshot(inst, tasks, nil, "", "", nil); err == nil {
		t.Fatal("detailBase 为空应报错（实例级与 task_list[*].links 均必填）")
	}
}

// TestPushSendsRequiredFieldsOverHTTP httptest 假飞书端点：断言**实际发出的 body**
// 具备全部必填字段（2026-09-28 实测 99992402 回归）。
func TestPushSendsRequiredFieldsOverHTTP(t *testing.T) {
	var mu sync.Mutex
	var pushBody []byte

	mux := http.NewServeMux()
	// token 端点（鉴权基础设施）。
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
	})
	// external_instances 端点：捕获请求体。
	mux.HandleFunc("/open-apis/approval/v4/external_instances", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读请求体失败: %v", err)
		}
		mu.Lock()
		pushBody = b
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"success"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 真实 HTTPClient 指向假端点（baseURL + token 端点同址替换）。
	hc := NewHTTPClient("cli-test", "secret-test", nil, nil)
	hc.baseURL = srv.URL
	hc.tokens = &tokenManager{appID: "cli-test", appSecret: "secret-test", baseURL: srv.URL, hc: hc.hc}

	// 本地先行：真实落库（t_instance + t_flow_task + t_approval_def），再走 Pusher.Push 全链路。
	db := storetest.NewDB(t)
	ctx := context.Background()
	bizNo := "PR-2609-0008"
	seedInstance(t, db, bizNo, 1)
	// 定义行：Push.Push 读 def → approval_code 优先 feishu_code + i18n 文案取 name。
	if err := db.UpsertApprovalDef(ctx, &store.ApprovalDef{
		ApprovalCode: "code-pr", DocType: "PR", Name: "采购申请审批", FeishuCode: "REAL-CODE-PR",
	}); err != nil {
		t.Fatal(err)
	}
	p := NewPusher(db, hc, testDetailBase, nil)
	res, err := p.Push(context.Background(), bizNo)
	if err != nil {
		t.Fatalf("推送失败: %v", err)
	}
	if !res.Pushed {
		t.Fatalf("期望真实推送，实际 %+v", res)
	}

	mu.Lock()
	raw := pushBody
	mu.Unlock()
	if len(raw) == 0 {
		t.Fatal("假端点未收到 external_instances 请求体")
	}
	var body struct {
		ApprovalCode  string                `json:"approval_code"`
		InstanceID    string                `json:"instance_id"`
		UpdateTime    any                   `json:"update_time"`
		Status        string                `json:"status"`
		StartTime     string                `json:"start_time"`
		EndTime       string                `json:"end_time"`
		OpenID        string                `json:"open_id"`
		Links         *ExternalInstanceLink `json:"links"`
		Tasks         []map[string]any      `json:"task_list"`
		I18nResources []map[string]any      `json:"i18n_resources"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("解析发出 body 失败: %v\nbody: %s", err, raw)
	}

	// ⓪ 双 code 池：approval_code 应取 t_approval_def.feishu_code（docs/16 G-8，不回退）。
	if body.ApprovalCode != "REAL-CODE-PR" {
		t.Errorf("approval_code = %q, 期望 REAL-CODE-PR（feishu_code 优先）", body.ApprovalCode)
	}
	// ① 实例级 start_time / end_time（官方必填，2026-09-28 实测 99992402）。
	if got := body.StartTime; got != strconv.FormatInt(pushAt.UnixMilli(), 10) {
		t.Errorf("start_time = %q, 期望 created_at 毫秒字符串 %q（非 ISO8601）",
			got, strconv.FormatInt(pushAt.UnixMilli(), 10))
	}
	if got := body.EndTime; got != "0" {
		t.Errorf("end_time = %q, 期望 \"0\"（PENDING 未终态）", got)
	}
	// ② 实例级 open_id（官方 open_id/user_id 二选一必传；seed 落 applicant_open_id）。
	if body.OpenID != "ou_user" {
		t.Errorf("实例级 open_id = %q, 期望 ou_user", body.OpenID)
	}
	// ③ update_time 官方标 string：必须为字符串形态（int64 数字会被文档判不符）。
	if got, ok := body.UpdateTime.(string); !ok || got != "1" {
		t.Errorf("update_time = %v(%T), 期望字符串 \"1\"（官方字段表标 string）", body.UpdateTime, body.UpdateTime)
	}
	// ④ 实例级 i18n_resources：★ 数组形态 + texts[].key/value 齐备 + 定义名。
	if len(body.I18nResources) != 1 {
		t.Fatalf("i18n_resources = %d 项, 期望 1（body=%s）", len(body.I18nResources), raw)
	}
	ir := body.I18nResources[0]
	if ir["locale"] != "zh-CN" || ir["is_default"] != true {
		t.Errorf("i18n_resources[0] = %v, 期望 locale=zh-CN / is_default=true", ir)
	}
	texts, ok := ir["texts"].([]any)
	if !ok || len(texts) != 1 {
		t.Fatalf("i18n_resources[0].texts = %v, 期望数组形态且 1 项（★ 传 map ⇒ 9499 实测缺陷）", ir["texts"])
	}
	text0, _ := texts[0].(map[string]any)
	if text0["key"] != I18nKeyInstanceTitle || text0["value"] != "采购申请审批" {
		t.Errorf("texts[0] = %v, 期望 {key:%s,value:采购申请审批}", text0, I18nKeyInstanceTitle)
	}

	// ⑤ 实例级 links（官方字段表必填；本次实测飞书未报但应补齐）。
	wantDetail := testDetailBase + "/approval/" + bizNo
	if body.Links == nil || body.Links.PCLink != wantDetail || body.Links.MobileLink != wantDetail {
		t.Errorf("实例级 links = %+v, 期望 pc/mobile 均为 %q", body.Links, wantDetail)
	}
	// ⑥ task_list[0]（RELEASED 的 PENDING 任务）：四必填字段齐 + open_id + 无历史错名
	//    （★ 第 6 批成果回归，不得回退）。
	if len(body.Tasks) != 1 {
		t.Fatalf("task_list = %d 条, 期望 1（只含 RELEASED）", len(body.Tasks))
	}
	task := body.Tasks[0]
	if got, _ := task["open_id"].(string); got != "ou_a" {
		t.Errorf("task_list[0].open_id = %v, 期望 ou_a（官方审批人字段名）", task["open_id"])
	}
	if strings.Contains(string(raw), "assignee_open_id") {
		t.Errorf("发出 body 不得再含历史错名 assignee_open_id，实际: %s", raw)
	}
	for _, key := range []string{"links", "create_time", "end_time", "update_time"} {
		if _, ok := task[key]; !ok {
			t.Errorf("task_list[0] 缺必填字段 %q（2026-09-28 实测 99992402）", key)
		}
	}
	// ⑦ 时间戳形态：毫秒字符串；未终态 end_time ＝ "0"；create/update ＝ 落库时刻毫秒。
	for _, key := range []string{"create_time", "update_time"} {
		got, _ := task[key].(string)
		if !isMilliString(got) {
			t.Errorf("task_list[0].%s = %v, 期望 Unix 毫秒字符串（非 ISO8601）", key, task[key])
		}
	}
	if got, _ := task["end_time"].(string); got != "0" {
		t.Errorf("task_list[0].end_time = %v, 期望 \"0\"（未终态）", task["end_time"])
	}
	if task["create_time"] != task["update_time"] {
		t.Errorf("seed 的 created_at=updated_at ⇒ 两时间戳应相等，实际 %v / %v",
			task["create_time"], task["update_time"])
	}
	// ⑧ task 级 links。
	lnk, ok := task["links"].(map[string]any)
	if !ok {
		t.Fatalf("task_list[0].links 不是对象: %v", task["links"])
	}
	if got, _ := lnk["pc_link"].(string); got != wantDetail {
		t.Errorf("task_list[0].links.pc_link = %q, 期望 %q", got, wantDetail)
	}
	if got, _ := lnk["mobile_link"].(string); got != wantDetail {
		t.Errorf("task_list[0].links.mobile_link = %q, 期望 %q", got, wantDetail)
	}
	// ⑨ action_context 形态不变（回调侧 biz_no 主读来源，docs/16 §2-B）。
	taskID := bizNo + "-n1-ou_a-1-1"
	if got, _ := task["action_context"].(string); got != `{"biz_no":"`+bizNo+`","task_id":"`+taskID+`"}` {
		t.Errorf("action_context = %q, 期望 {\"biz_no\":…,\"task_id\":…}", got)
	}
}
