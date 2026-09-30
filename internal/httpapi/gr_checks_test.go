package httpapi

// GR 判据验收：6 条提交时点 hard（各拦+放）＋ 提交后 L07 五列自检。

import (
	"context"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

func grBody(fields map[string]any) *approvalSubmitBody {
	return &approvalSubmitBody{DocType: "GR", Fields: fields}
}

// seedRelatedInstance 造关联单（CT/BA）。
func seedRelatedInstance(t *testing.T, db *store.DB, code, docType, biz string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO t_instance (instance_code, approval_code, doc_type, biz_no, status,
  applicant_open_id, amount_cents, source, created_at, updated_at)
VALUES (?, ?, ?, ?, 'APPROVED', 'ou_app', 1000, 'flow', ?, ?)`,
		"I-"+biz, "ac-x", docType, biz, now, now); err != nil {
		t.Fatal(err)
	}
}

func TestGRRelatedOrderOrRecord(t *testing.T) {
	db := hcDB(t)
	d := Deps{DB: db}
	form := metaTestBundle(t).Forms["GR"]
	ctx := context.Background()
	seedRelatedInstance(t, db, "ac-ct", "CT", "CT-2609-0001")
	seedRelatedInstance(t, db, "ac-ba", "BA", "BA-2609-0001")
	seedRelatedInstance(t, db, "ac-pr", "PR", "PR-2609-0001")

	mustBlock(t, "缺关联", checkGRRelatedOrderOrRecord(ctx, d, form, grBody(map[string]any{}), "ou_app"))
	mustBlock(t, "不存在", checkGRRelatedOrderOrRecord(ctx, d, form,
		grBody(map[string]any{"related_order_or_record_no": "CT-0000-0000"}), "ou_app"))
	mustPass(t, "CT等值命中", checkGRRelatedOrderOrRecord(ctx, d, form,
		grBody(map[string]any{"related_order_or_record_no": "CT-2609-0001"}), "ou_app"))
	// ★ 采一档 BA 必须接受（只认 CT 会让采一档验收永远录不进来）
	mustPass(t, "BA等值命中", checkGRRelatedOrderOrRecord(ctx, d, form,
		grBody(map[string]any{"related_order_or_record_no": "BA-2609-0001"}), "ou_app"))
	mustBlock(t, "PR类型拒", checkGRRelatedOrderOrRecord(ctx, d, form,
		grBody(map[string]any{"related_order_or_record_no": "PR-2609-0001"}), "ou_app"))
}

func TestGRAcceptanceGroupMembers(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["GR"]

	// P01 三方齐 → 放
	mustPass(t, "P01三方齐", checkGRAcceptanceGroupMembers(context.Background(), d, form,
		grBody(map[string]any{"acceptance_group": "P01",
			"member_purchaser": "ou1", "member_qc": "ou2", "member_warehouse": "ou3"}), "ou_app"))
	// P01 缺库管 → 拦
	mustBlock(t, "P01缺库管", checkGRAcceptanceGroupMembers(context.Background(), d, form,
		grBody(map[string]any{"acceptance_group": "P01",
			"member_purchaser": "ou1", "member_qc": "ou2"}), "ou_app"))
	// P02–P04 双方齐 → 放；★ 填了库管（不该有的）→ 拦（双向）
	mustPass(t, "P02双方齐", checkGRAcceptanceGroupMembers(context.Background(), d, form,
		grBody(map[string]any{"acceptance_group": "P02–P04",
			"member_purchaser": "ou1", "member_qc": "ou2"}), "ou_app"))
	mustBlock(t, "P02多填库管", checkGRAcceptanceGroupMembers(context.Background(), d, form,
		grBody(map[string]any{"acceptance_group": "P02–P04",
			"member_purchaser": "ou1", "member_qc": "ou2", "member_warehouse": "ou3"}), "ou_app"))
	// P05–P08：purchaser+ops 放；缺 ops 拦；多填 qc 拦
	mustPass(t, "P05双方齐", checkGRAcceptanceGroupMembers(context.Background(), d, form,
		grBody(map[string]any{"acceptance_group": "P05-P08",
			"member_purchaser": "ou1", "member_ops": "ou4"}), "ou_app"))
	mustBlock(t, "P05缺运营", checkGRAcceptanceGroupMembers(context.Background(), d, form,
		grBody(map[string]any{"acceptance_group": "P05-P08", "member_purchaser": "ou1"}), "ou_app"))
	mustBlock(t, "P05多填质检", checkGRAcceptanceGroupMembers(context.Background(), d, form,
		grBody(map[string]any{"acceptance_group": "P05-P08",
			"member_purchaser": "ou1", "member_ops": "ou4", "member_qc": "ou2"}), "ou_app"))
	// 未知组 → 可见失败
	mustBlock(t, "未知组", checkGRAcceptanceGroupMembers(context.Background(), d, form,
		grBody(map[string]any{"acceptance_group": "P99"}), "ou_app"))
}

func TestGRNoOpsSupervisorMember(t *testing.T) {
	db := hcDB(t)
	ctx := context.Background()
	// 在岗综合运营主管
	if err := db.UpsertUserRole(ctx, store.UserRole{
		OpenID: "ou_ops", Name: "运营主管甲", Role: "综合运营主管",
		Department: "综合运营部", Active: true, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	// 正常成员（综合运营**部人员**，非主管）——放行（按部门判会误拦这里）
	if err := db.UpsertUserRole(ctx, store.UserRole{
		OpenID: "ou_clerk", Name: "部员乙", Role: "申请人",
		Department: "综合运营部", Active: true, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: db}
	form := metaTestBundle(t).Forms["GR"]

	mustPass(t, "综合运营部员为成员_放行", checkGRNoOpsSupervisorMember(ctx, d, form,
		grBody(map[string]any{"member_ops": "ou_clerk"}), "ou_app"))
	mustBlock(t, "主管本人为成员_拦", checkGRNoOpsSupervisorMember(ctx, d, form,
		grBody(map[string]any{"member_ops": "ou_ops"}), "ou_app"))
	mustBlock(t, "主管按姓名拦", checkGRNoOpsSupervisorMember(ctx, d, form,
		grBody(map[string]any{"member_qc": "运营主管甲"}), "ou_app"))
}

func TestGRNoApproverInGroup(t *testing.T) {
	db := hcDB(t)
	ctx := context.Background()
	seedRelatedInstance(t, db, "ac-ct", "CT", "CT-2609-0100")
	// 关联单链上有审批任务（审批人 ou_sup）
	if _, err := db.ExecContext(ctx, `
INSERT INTO t_flow_task (task_id, biz_no, node_id, node_name, round,
  assignee_open_id, status, created_at, updated_at)
VALUES ('task-1','CT-2609-0100','supervisor_approval','主管审批',1,'ou_sup','APPROVED',?,?)`,
		time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: db}
	form := metaTestBundle(t).Forms["GR"]

	// 取不到审批记录 ⇒ 可见失败（fail-closed）
	mustBlock(t, "无审批记录", checkGRNoApproverInGroup(ctx, d, form,
		grBody(map[string]any{"related_order_or_record_no": "CT-2609-0101"}), "ou_app")) // 不存在的关联（tasks 空）

	mustBlock(t, "审批人入组", checkGRNoApproverInGroup(ctx, d, form,
		grBody(map[string]any{"related_order_or_record_no": "CT-2609-0100",
			"member_purchaser": "ou_sup"}), "ou_app"))
	mustPass(t, "非审批人入组", checkGRNoApproverInGroup(ctx, d, form,
		grBody(map[string]any{"related_order_or_record_no": "CT-2609-0100",
			"member_purchaser": "ou_other"}), "ou_app"))
}

func TestGRConcessionDualSign(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["GR"]
	// 让步接收：双签齐且不同人 → 放
	mustPass(t, "让步双签齐", checkGRConcessionDualSign(context.Background(), d, form,
		grBody(map[string]any{"acceptance_conclusion": "让步接收",
			"concession_qc_signer": "ou_qc", "concession_user_dept_signer": "ou_dept"}), "ou_app"))
	mustBlock(t, "让步缺签", checkGRConcessionDualSign(context.Background(), d, form,
		grBody(map[string]any{"acceptance_conclusion": "让步接收",
			"concession_qc_signer": "ou_qc"}), "ou_app"))
	mustBlock(t, "让步同人", checkGRConcessionDualSign(context.Background(), d, form,
		grBody(map[string]any{"acceptance_conclusion": "让步接收",
			"concession_qc_signer": "ou_x", "concession_user_dept_signer": "ou_x"}), "ou_app"))
	// 非让步：双签必须为空
	mustPass(t, "合格无双签", checkGRConcessionDualSign(context.Background(), d, form,
		grBody(map[string]any{"acceptance_conclusion": "合格"}), "ou_app"))
	mustBlock(t, "合格却带双签", checkGRConcessionDualSign(context.Background(), d, form,
		grBody(map[string]any{"acceptance_conclusion": "合格",
			"concession_qc_signer": "ou_qc"}), "ou_app"))
}

func TestGRReceivedQuantityPositive(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["GR"]
	mustBlock(t, "数量缺", checkGRReceivedQuantityPositive(context.Background(), d, form,
		grBody(map[string]any{}), "ou_app"))
	mustBlock(t, "数量0", checkGRReceivedQuantityPositive(context.Background(), d, form,
		grBody(map[string]any{"received_quantity": 0}), "ou_app"))
	mustPass(t, "数量>0", checkGRReceivedQuantityPositive(context.Background(), d, form,
		grBody(map[string]any{"received_quantity": 12.5}), "ou_app"))
}

func TestGRPostSubmitL07SelfCheck(t *testing.T) {
	db := hcDB(t)
	d := Deps{DB: db}
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	seed := func(biz, ext string) {
		if _, err := db.ExecContext(ctx, `
INSERT INTO t_ledger_archive (ledger_type, biz_no, instance_code, source_doc_type, department,
  applicant_open_id, ext_json, created_at, updated_at)
VALUES ('L07', ?, ?, 'GR', '质检技术部', 'ou_app', ?, ?, ?)`,
			biz, "I-"+biz, ext, now, now); err != nil {
			t.Fatal(err)
		}
	}
	full := `{"related_order_or_record_no":"CT-2609-0001","received_quantity":100,` +
		`"acceptance_conclusion":"合格","acceptance_members":"甲、乙、丙"}`
	seed("GR-2609-0001", full)
	seed("GR-2609-0002", `{"related_order_or_record_no":"CT-1","received_quantity":100,"acceptance_conclusion":"合格"}`) // 缺 members

	mustPass(t, "5列齐", d.verifyGRPostSubmitL07(ctx, "GR-2609-0001"))
	mustBlock(t, "缺acceptance_members", d.verifyGRPostSubmitL07(ctx, "GR-2609-0002"))
	mustBlock(t, "行不存在", d.verifyGRPostSubmitL07(ctx, "GR-9999-9999"))
}
