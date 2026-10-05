package seed

// N-060 H3.4：seed 可写白名单 ⊇ 规格登记面（抓「seed 过期名」活缺陷）。
// ★ 对照面 ＝ spec/ledger-mapping.json#ledgers.L06.fields 中 writable:true 的 label
//（L06 运营表＝writable_fields 的唯一消费面 —— 0001 注释「仅运营表有效」；
// L01/L08 等台账的可写列不走本白名单，见回执收窄理由）。
// ★ 判据价值（先证后修）：H3.1 的两处过期名（提交日期/集团受理编号）在此必红。

import (
	"encoding/json"
	"testing"

	specfs "github.com/chadhao/jx-procurement-platform"
)

func TestSeedWritableCoversSpecL06(t *testing.T) {
	raw, err := specfs.FS.ReadFile("spec/ledger-mapping.json")
	if err != nil {
		t.Fatal(err)
	}
	var lm struct {
		Ledgers map[string]struct {
			Fields []struct {
				Label    string `json:"label"`
				Writable bool   `json:"writable"`
			} `json:"fields"`
		} `json:"ledgers"`
	}
	if err := json.Unmarshal(raw, &lm); err != nil {
		t.Fatal(err)
	}
	l06, ok := lm.Ledgers["L06"]
	if !ok {
		t.Fatal("spec/ledger-mapping.json 缺 L06")
	}
	inSeed := map[string]bool{}
	for _, k := range opsSupervisor {
		inSeed[k] = true
	}
	missing := []string{}
	for _, f := range l06.Fields {
		if f.Writable && !inSeed[f.Label] {
			missing = append(missing, f.Label)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("seed opsSupervisor 未覆盖 L06 规格可写键（H3.1 过期名/缺登记 —— 界面按规格名写入会 403）: 缺 %v", missing)
	}
}
