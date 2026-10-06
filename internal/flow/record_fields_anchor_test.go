package flow

// N-069 锚定（M3）：`nodeFieldSpecFor` 的 `RecordKeys` ↔ spec `routes[*].nodes[*].record_fields`
// 逐字互锁 —— 防第二份真相：spec 改字段名/增删节点而表未同批改 ⇒ 本测试当场红。
// （同 `designation_anchor_test` 的双向锚思路；本表在包内 ⇒ 直接互锁结构本身。）

import (
	"reflect"
	"testing"

	specfs "github.com/chadhao/jx-procurement-platform"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

func TestRecordFieldsAnchorN069(t *testing.T) {
	b, err := specload.Load(specfs.FS)
	if err != nil {
		t.Fatalf("spec 加载失败: %v", err)
	}
	// route → doc 反查（doc_chains[*].route；先到先得即可 —— 同路由多单据时任一 doc 均可命中同表行）。
	docOfRoute := map[string]string{}
	for doc, dc := range b.Chain.DocChains {
		if dc.Route != "" {
			if _, ok := docOfRoute[dc.Route]; !ok {
				docOfRoute[dc.Route] = doc
			}
		}
	}
	seenSpec := 0
	for rn, r := range b.Chain.Routes {
		for _, n := range r.Nodes {
			if len(n.RecordFields) == 0 {
				continue
			}
			seenSpec++
			doc := docOfRoute[rn]
			if doc == "" {
				t.Fatalf("route %s 的节点 %s 带 record_fields，但 doc_chains 无 route 反查（无法定位表行 docType）", rn, n.ID)
			}
			rule := nodeFieldSpecFor(doc, n.ID)
			if rule == nil || len(rule.RecordKeys) == 0 {
				t.Errorf("表缺行：%s × %s 在 spec 有 record_fields=%v，但 nodeFieldSpecFor 无 RecordKeys（判据要的字段没人录）",
					doc, n.ID, n.RecordFields)
				continue
			}
			if !reflect.DeepEqual(rule.RecordKeys, n.RecordFields) {
				t.Errorf("表与 spec 不一致：%s × %s 表=%v spec=%v（spec 改名 ⇒ 须同批改表）",
					doc, n.ID, rule.RecordKeys, n.RecordFields)
			}
		}
	}
	// 计数钉（N-026：spec 增删 record_fields 节点 ⇒ 同批改本测试，不静默漂）。
	if seenSpec != 2 {
		t.Errorf("spec 带 record_fields 的节点数 = %d, 期望 2（新增/删除须同批锚定）", seenSpec)
	}
	// 反向：表中已知两行必须在 spec 逐字存在（表增行而 spec 无 ⇒ 第二份真相）。
	for _, probe := range []struct{ doc, node string }{
		{"BA", baReturnReceiptNodeID}, {"BA", baDisburseNodeID},
	} {
		rule := nodeFieldSpecFor(probe.doc, probe.node)
		if rule == nil || len(rule.RecordKeys) == 0 {
			t.Fatalf("表缺 %s × %s", probe.doc, probe.node)
		}
		found := false
		for _, r := range b.Chain.Routes {
			for _, n := range r.Nodes {
				if n.ID == probe.node && reflect.DeepEqual(n.RecordFields, rule.RecordKeys) {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("表行 %s × %s 的 RecordKeys 在 spec 无逐字对应（第二份真相）: %v",
				probe.doc, probe.node, rule.RecordKeys)
		}
	}
}
