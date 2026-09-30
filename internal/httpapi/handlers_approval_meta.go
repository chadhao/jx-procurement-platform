package httpapi

// GET /api/approval/meta —— 发起页表单元数据（D1 / N-008③ 定案）：
//   表单 schema **后端权威**（spec/forms/*.json 内嵌加载），前端 meta 驱动渲染；
//   响应必含 spec_version，供前后端与运维核对「线上跑的是哪一版契约」。
//
// 鉴权＝普通会话（挂 api 组，requireSession）——区别 admin-only 的 GET /approval/defs。
// 分档/链的**计算不在 meta**：预览走 POST /api/approval/preview（M7，与提交共算，D5）。

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

type approvalMetaResponse struct {
	SpecVersion       string             `json:"spec_version"`
	DocTypesAvailable []string           `json:"doc_types_available"`
	Forms             []specload.FormDoc `json:"forms"`
	// ApprovalCodes doc_type → approval_code（提交必填；来自 t_config_mapping 反查，
	// 前端不猜码值）。未配置的 doc_type 不出现在 map 中（可见缺失，不静默）。
	ApprovalCodes map[string]string `json:"approval_codes"`
	Enums         json.RawMessage   `json:"enums"`
	Bands         []specload.Band   `json:"bands"`
	// Constants 运营性常量表（T2）：table → **active** 值（retired 不下发 ⇒ 新单据选不到）。
	// constant_ref 字段渲染下拉用；停用项历史单据不受影响（值快照在 ext_json）。
	Constants map[string][]string `json:"constants"`
}

func (d *Deps) handleApprovalMeta(c echo.Context) error {
	ctx := c.Request().Context()
	if d.Spec == nil {
		return fail(c, http.StatusServiceUnavailable, codeNotReady, "机读规格未装配")
	}
	docTypes := make([]string, 0, len(d.Spec.Forms))
	for dt := range d.Spec.Forms {
		docTypes = append(docTypes, dt)
	}
	sort.Strings(docTypes) // BA, PR, SA …（前端稳定渲染顺序）
	forms := make([]specload.FormDoc, 0, len(docTypes))
	for _, dt := range docTypes {
		forms = append(forms, d.Spec.Forms[dt])
	}
	// doc_type → approval_code（遍历已配置映射反查；Maps 未装配 ⇒ 空 map，可见不猜）
	approvalCodes := map[string]string{}
	if d.Maps != nil && d.Maps.Approval != nil {
		for _, code := range d.Maps.Approval.Codes() {
			if dt, ok := d.Maps.Approval.DocType(code); ok {
				if _, exists := d.Spec.Forms[dt]; exists {
					approvalCodes[dt] = code
				}
			}
		}
	}
	// T2：常量表只下发 active（新单据选不到 retired；历史显示走快照）
	constants := map[string][]string{}
	if d.Spec.Constants != nil {
		for _, tb := range d.Spec.Constants.Tables {
			vals, err := d.DB.ListActiveConstantValues(ctx, tb.Key)
			if err != nil {
				return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
			}
			constants[tb.Key] = vals
		}
	}
	return ok(c, approvalMetaResponse{
		SpecVersion:       d.Spec.SpecVersion,
		DocTypesAvailable: docTypes,
		Forms:             forms,
		ApprovalCodes:     approvalCodes,
		Enums:             d.Spec.Enums.Raw,
		Bands:             d.Spec.Chain.Thresholds.Purchase.Bands,
		Constants:         constants,
	})
}
