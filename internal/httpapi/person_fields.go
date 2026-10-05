package httpapi

// N-060 F5 · FR-M9-11 提交页人员防错的服务端半边：
// person 类型字段（source=user、提交段）的值必须**命中镜像在职名单**（t_org_user，
// is_deleted=0 由 ListOrgUsers 保证）—— 按 open_id 精确或显示名精确匹配；
// 命不中 ⇒ 可见失败（400），**不静默放行**。
// ★ 与前端的两层防错配合：前端 person 下拉只列镜像在职（不可选离职/停用 = 接口已滤）；
// 本函数拦「绕过下拉的手填野值」。★ D6 实时回源（orgVerifyAtSubmit，超时告警放行）
// 是提交时对申请人/部门的复核，与本校验不同层、并存不冲突。
// ★ 匹配口径备案：person 字段现值形态既有 open_id（组织字段）也有人名（GR 验收组），
// 故双键精确匹配；**重名姓名**取「命中任一在职即过」（满足 FR「命不中 ⇒ 阻断」字面；
// 若要求唯一性/open_id-only 属口径细化，回执提请裁定，此处不擅自收窄）。

import (
	"context"
	"fmt"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// validatePersonFields 校验提交段 person 字段命中镜像在职；无此类字段或值为空 ⇒ 通过
// （空值由结构化 required 判据负责）。
func (d Deps) validatePersonFields(ctx context.Context, form specload.FormDoc, provided map[string]any) error {
	need := false
	for _, sec := range form.Sections {
		if strings.TrimSpace(sec.FilledAt) != "" || sec.Repeating {
			continue // 非提交段 / 行内组不在本校验面
		}
		for _, f := range sec.Fields {
			if f.Source != "user" || f.Type != "person" {
				continue
			}
			v := strings.TrimSpace(fmt.Sprint(orphValue(provided[f.Name])))
			if v == "" || v == "<nil>" {
				continue // 空值归 required 判据
			}
			need = true
		}
	}
	if !need {
		return nil
	}
	users, err := d.DB.ListOrgUsers(ctx)
	if err != nil {
		// 镜像读失败 ⇒ fail-closed（不静默放行 —— 否则防错整体失效）。
		return fmt.Errorf("人员镜像读取失败（FR-M9-11 防错不可静默失效）: %w", err)
	}
	hit := func(v string) bool {
		for _, u := range users {
			// ★ ListOrgUsers 含软删行（对账用）—— 必须滤掉 is_deleted=1
			//（否则「离职不可选/命不中阻断」被软删行打穿）。
			if u.IsDeleted {
				continue
			}
			if strings.TrimSpace(u.OpenID) == v || strings.TrimSpace(u.Name) == v {
				return true
			}
		}
		return false
	}
	for _, sec := range form.Sections {
		if strings.TrimSpace(sec.FilledAt) != "" || sec.Repeating {
			continue
		}
		for _, f := range sec.Fields {
			if f.Source != "user" || f.Type != "person" {
				continue
			}
			v := strings.TrimSpace(fmt.Sprint(orphValue(provided[f.Name])))
			if v == "" || v == "<nil>" {
				continue
			}
			if !hit(v) {
				return fmt.Errorf("字段「%s」（%s）值 %q 未命中在职人员镜像 —— 离职/停用/野值不可提交（FR-M9-11 阻断）",
					f.Label, f.Name, v)
			}
		}
	}
	return nil
}

// orphValue provided 值转字符串（nil/缺失 ⇒ ""）。
func orphValue(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(v)
	}
}
