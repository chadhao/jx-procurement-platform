package httpapi

// BJ 判据验收：5 条提交时点 hard（各拦+放）＋ 提交后关键三列不可变自检
// （含结构性保障：系统不存在修改实例业务字段的路由）。

import (
	"context"
	"strings"
	"testing"
)

func bjBody(fields map[string]any) *approvalSubmitBody {
	return &approvalSubmitBody{DocType: "BJ", Fields: fields}
}

// 明细样本：每行一家，行内含 有效/技术符合 标注。
const bjQuotes3 = `甲公司 | 报价10000 | 有效 | 技术符合
乙公司 | 报价10200 | 有效 | 技术符合
丙公司 | 报价10500 | 有效 | 技术符合`

func TestBJMinThreeQuotes(t *testing.T) {
	d := Deps{DB: hcDB(t)}
	form := metaTestBundle(t).Forms["BJ"]
	ctx := context.Background()

	mustBlock(t, "明细为空", checkBJMinThreeQuotes(ctx, d, form,
		bjBody(map[string]any{}), "ou_app"))
	mustBlock(t, "仅2家", checkBJMinThreeQuotes(ctx, d, form,
		bjBody(map[string]any{"quotes": "甲公司 | 10000 | 有效 | 技术符合\n乙公司 | 10200 | 有效 | 技术符合"}), "ou_app"))
	mustPass(t, "3家", checkBJMinThreeQuotes(ctx, d, form,
		bjBody(map[string]any{"quotes": bjQuotes3}), "ou_app"))
	// 手填 quote_count=3 但明细只有 2 家 —— 核心抓的形态，必拦
	mustBlock(t, "手填3明细2", checkBJMinThreeQuotes(ctx, d, form,
		bjBody(map[string]any{
			"quotes":      "甲公司 | 10000 | 有效 | 技术符合\n乙公司 | 10200 | 有效 | 技术符合",
			"quote_count": 3,
		}), "ou_app"))
}

func TestBJQuotesIndependent(t *testing.T) {
	d := Deps{DB: hcDB(t)}
	form := metaTestBundle(t).Forms["BJ"]
	ctx := context.Background()

	mustBlock(t, "未声明", checkBJQuotesIndependent(ctx, d, form,
		bjBody(map[string]any{"quotes": bjQuotes3}), "ou_app"))
	mustBlock(t, "声明否", checkBJQuotesIndependent(ctx, d, form,
		bjBody(map[string]any{"quotes": bjQuotes3, "all_quotes_independent": false}), "ou_app"))
	mustPass(t, "声明是", checkBJQuotesIndependent(ctx, d, form,
		bjBody(map[string]any{"quotes": bjQuotes3, "all_quotes_independent": true}), "ou_app"))
}

func TestBJTechnicalCompliance(t *testing.T) {
	d := Deps{DB: hcDB(t)}
	form := metaTestBundle(t).Forms["BJ"]
	ctx := context.Background()

	mustBlock(t, "空", checkBJTechnicalCompliance(ctx, d, form,
		bjBody(map[string]any{"quotes": bjQuotes3}), "ou_app"))
	// 3 家只提 1 处符合判断 —— 不逐家 ⇒ 拦
	mustBlock(t, "不足逐家", checkBJTechnicalCompliance(ctx, d, form,
		bjBody(map[string]any{
			"quotes":               bjQuotes3,
			"technical_compliance": "甲公司技术符合",
		}), "ou_app"))
	mustPass(t, "逐家", checkBJTechnicalCompliance(ctx, d, form,
		bjBody(map[string]any{
			"quotes":               bjQuotes3,
			"technical_compliance": "甲公司符合；乙公司符合；丙公司不符合（缺一项参数）",
		}), "ou_app"))
}

func TestBJSelectedValidCompliant(t *testing.T) {
	d := Deps{DB: hcDB(t)}
	form := metaTestBundle(t).Forms["BJ"]
	ctx := context.Background()

	mustBlock(t, "未选", checkBJSelectedValidCompliant(ctx, d, form,
		bjBody(map[string]any{"quotes": bjQuotes3}), "ou_app"))
	mustBlock(t, "未在明细", checkBJSelectedValidCompliant(ctx, d, form,
		bjBody(map[string]any{
			"quotes":            bjQuotes3,
			"selected_supplier": "丁公司",
		}), "ou_app"))
	// 选定行未标注有效/技术符合（陪标形态）⇒ 拦
	mustBlock(t, "行未标注", checkBJSelectedValidCompliant(ctx, d, form,
		bjBody(map[string]any{
			"quotes":            "甲公司 | 10000 | 有效 | 技术符合\n乙公司 | 10200 | 待定 | 未判断\n丙公司 | 10500 | 有效 | 技术符合",
			"selected_supplier": "乙公司",
		}), "ou_app"))
	mustPass(t, "命中且合规", checkBJSelectedValidCompliant(ctx, d, form,
		bjBody(map[string]any{
			"quotes":            bjQuotes3,
			"selected_supplier": "乙公司",
		}), "ou_app"))
}

func TestBJNotSingleSource(t *testing.T) {
	d := Deps{DB: hcDB(t)}
	form := metaTestBundle(t).Forms["BJ"]
	ctx := context.Background()

	mustBlock(t, "单一来源", checkBJNotSingleSource(ctx, d, form,
		bjBody(map[string]any{"procure_method": "单一来源"}), "ou_app"))
	mustPass(t, "比价", checkBJNotSingleSource(ctx, d, form,
		bjBody(map[string]any{"procure_method": "比价"}), "ou_app"))
	mustPass(t, "未填放行", checkBJNotSingleSource(ctx, d, form,
		bjBody(map[string]any{}), "ou_app"))
}

// 全合规 body：3 hard 之外还有 soft 判据 —— evaluate 只执行 hard 应全过。
func TestBJHardAllPass(t *testing.T) {
	d := Deps{DB: hcDB(t)}
	form := metaTestBundle(t).Forms["BJ"]
	body := bjBody(map[string]any{
		"quotes":                 bjQuotes3,
		"all_quotes_independent": true,
		"technical_compliance":   "甲公司符合；乙公司符合；丙公司符合",
		"selected_supplier":      "乙公司",
		"procure_method":         "比价",
	})
	if err := d.evaluateHardChecks(context.Background(), form, body, "ou_app"); err != nil {
		t.Fatalf("全合规应放行: %v", err)
	}
}

// 提交后不可变自检：ext_json 冻结值与提交值一致 → 放；篡改 → 拦且点名字段。
func TestBJPostSubmitImmutability(t *testing.T) {
	t.Parallel()
	d := Deps{DB: hcDB(t)}
	ctx := context.Background()
	const bizNo = "BJ-2610-0008"
	if _, err := d.DB.ExecContext(ctx, `
INSERT INTO t_instance(instance_code,approval_code,biz_no,doc_type,status,applicant_open_id,ext_json,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?)`,
		"I-"+bizNo, "ac-bj", bizNo, "BJ", "PENDING", "ou_a",
		`{"selected_reason":"三家比价最优","procure_method":"比价","selected_supplier":"甲公司"}`,
		"2026-10-01 10:00:00", "2026-10-01 10:00:00"); err != nil {
		t.Fatal(err)
	}

	good := bjBody(map[string]any{
		"selected_reason":   "三家比价最优",
		"procure_method":    "比价",
		"selected_supplier": "甲公司",
	})
	if err := d.verifyBJPostSubmitImmutability(ctx, good, bizNo); err != nil {
		t.Fatalf("原样应放行: %v", err)
	}
	bad := bjBody(map[string]any{"selected_reason": "事后改口"})
	err := d.verifyBJPostSubmitImmutability(ctx, bad, bizNo)
	if err == nil {
		t.Fatal("篡改选定理由应拦")
	}
	if !strings.Contains(err.Error(), "selected_reason") {
		t.Fatalf("报错须点名字段 selected_reason: %v", err)
	}
	if err := d.verifyBJPostSubmitImmutability(ctx,
		bjBody(map[string]any{"selected_supplier": "乙公司"}), bizNo); err == nil {
		t.Fatal("篡改选定单位应拦")
	}
}

// 结构性保障：系统**不存在**任何修改实例业务字段的路由（无路径 ⇒ 不可改）。
// 与 verify 自检互为双保险：自检抓"值不一致"，本断言抓"居然有修改口"。
func TestBJNoInstanceMutationRoute(t *testing.T) {
	t.Parallel()
	e, _, _ := newSubmitM4App(t, false)
	for _, r := range e.Routes() {
		if r.Method == "PUT" || r.Method == "PATCH" || r.Method == "DELETE" {
			if strings.Contains(r.Path, "instance") || strings.Contains(r.Path, "approval/") {
				t.Errorf("存在实例/审批修改路由 %s %s —— BJ 提交后不可改的结构性前提被破坏", r.Method, r.Path)
			}
		}
	}
}
