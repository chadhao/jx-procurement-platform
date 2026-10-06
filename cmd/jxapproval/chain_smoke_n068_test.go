package main

// N-068 ②：启动自检烟测 —— ①带具名探针身份（applicant 节点不得恒 unresolved、
// 与 preview 口径对齐）；②日志逐条点名（节点名＋角色＋原因，数字可执行）。
// ★ 反面对照：不带身份的旧事实 ⇒ applicant 必进 unresolved —— 证明修复在承重。

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	specfs "github.com/chadhao/jx-procurement-platform"
	"github.com/chadhao/jx-procurement-platform/internal/chain"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// emptyRoles 空角色源：业务角色全部算不到（模拟「配角色前」），
// 而 applicant 节点不查角色表 ⇒ 只要有探针身份就不该出现在 unresolved 里。
type emptyRoles struct{}

func (emptyRoles) Candidates(ctx context.Context, q chain.RoleQuery) ([]chain.RoleCandidate, error) {
	return nil, nil
}

func TestChainSmokeFactsResolveApplicantN068(t *testing.T) {
	b, err := specload.Load(specfs.FS)
	if err != nil {
		t.Fatalf("spec 加载失败: %v", err)
	}
	svc := &chain.Service{B: b, Roles: emptyRoles{}}
	ctx := context.Background()

	// ① 新事实（带探针身份）⇒ applicant 节点**不得**进 unresolved。
	rc, err := svc.Compute(ctx, chainSmokeFacts())
	if err != nil {
		t.Fatalf("烟测 Compute 失败: %v", err)
	}
	for _, u := range rc.Unresolved {
		if u.Role == "applicant" {
			t.Errorf("applicant 节点不得进 unresolved（探针身份未生效？）: %+v", u)
		}
	}
	// ② 真实配置缺失**不得被掩盖**：空角色源 ⇒ 业务角色仍必须全部暴露。
	if len(rc.Unresolved) == 0 {
		t.Fatal("空角色源下 unresolved 应非空（不得用探针身份掩盖真实缺人）")
	}
	// ③ 明细可点名：每条必须带节点名与角色（日志/排查的最小可执行信息）。
	for _, u := range rc.Unresolved {
		if u.NodeName == "" || u.Role == "" {
			t.Errorf("unresolved 明细缺节点名或角色（无法点名）: %+v", u)
		}
	}
	// ④ 日志断言：logChainSmokeUnresolved 必须写出明细。
	var buf bytes.Buffer
	logChainSmokeUnresolved(slog.New(slog.NewTextHandler(&buf, nil)), rc.Unresolved)
	if !strings.Contains(buf.String(), "算不到人的角色明细") || !strings.Contains(buf.String(), "role=") {
		t.Errorf("日志未写出明细: %s", buf.String())
	}

	// ⑤ 反面对照：旧事实（不带申请人身份）⇒ applicant 恒 unresolved（＝改前行为，修复的承重证明）。
	probeAmt := int64(99999)
	rcOld, err := svc.Compute(ctx, chain.Facts{
		DocType: chain.DocBA, AmountCents: &probeAmt, UsageCategoryL1: "P01",
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range rcOld.Unresolved {
		if u.Role == "applicant" {
			found = true
		}
	}
	if !found {
		t.Error("对照组：不带身份时 applicant 应进 unresolved（反面证据缺失）")
	}
}
