package chain

import (
	"encoding/json"
	"io/fs"
	"testing"

	specfs "github.com/chadhao/jx-procurement-platform"
)

// TestRoleNodeActorKindPinsGoClassification —— `N-049` ② 的**交叉钉**（我方域：测试）。
//
// ★ 它治什么：`chain.json#roles` 是角色定义的**权威表**，而 Go 侧另有**两张硬编码表**
// 决定「节点的 actor 到底跑不跑」——
//
//	`approverRoles`（生成 flow 审批任务）· `isActionActor`（动作环节，不生成审批任务）。
//
// 两张表与 spec 之间**没有任何机检**：★ 只要有人往任一表里加/删一个角色，spec 侧不会红
// ⇒ 就会重现 `N-044`/`N-045` 的缺陷类别（**`required: true` 却无人审**）。
//
// ★ 本钉的做法：把 `spec/chain.json#roles.<role>.node_actor_kind`（受控取值
// `approver`/`action`/`none`；约定正本见 `chain.json#conventions.node_actor_kind`）
// 与 Go 的两张表**双向互锁** —— 任一侧漂移即本条测试转红。
//
// ★ 诚实划界：本钉**不**使 `S19` 的白名单变精确（那需要两侧同批扩点路径引擎的 `[k=v]` 过滤，
// 见 `N-049` ② 第二半）；★ 它只保证「**分类声明**与**实现**不打架」。
func TestRoleNodeActorKindPinsGoClassification(t *testing.T) {
	raw, err := fs.ReadFile(specfs.FS, "spec/chain.json")
	if err != nil {
		t.Fatalf("读取内嵌 spec/chain.json：%v", err)
	}
	var doc struct {
		Roles map[string]struct {
			NodeActorKind string `json:"node_actor_kind"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 spec/chain.json：%v", err)
	}
	if len(doc.Roles) == 0 {
		t.Fatal("roles 为空 —— 夹具或 spec 结构变了")
	}

	seen := map[string]int{}
	for key, role := range doc.Roles {
		kind := role.NodeActorKind
		seen[kind]++
		approved, acted := approverRoles[key], isActionActor(key)
		switch kind {
		case "approver":
			if !approved {
				t.Errorf("roles.%s 声明 node_actor_kind=approver，★ 但 Go `approverRoles` 不含它"+
					"（⇒ 该角色作 actor 时**不会生成审批任务**，与实际声明相反）", key)
			}
			if acted {
				t.Errorf("roles.%s 声明 approver，但 Go `isActionActor` 把它当动作 actor（两侧互斥）", key)
			}
		case "action":
			if !acted {
				t.Errorf("roles.%s 声明 node_actor_kind=action，★ 但 Go `isActionActor` 不含它", key)
			}
			if approved {
				t.Errorf("roles.%s 声明 action，但 Go `approverRoles` 含它（两侧互斥）", key)
			}
		case "none":
			if approved || acted {
				t.Errorf("roles.%s 声明 node_actor_kind=none，★ 但 Go 把它当节点 actor"+
					"（approverRoles=%v isActionActor=%v）—— ★ 这正是 `S19` 覆盖不到的那类「惰性必需节点」",
					key, approved, acted)
			}
		default:
			t.Errorf("roles.%s.node_actor_kind = %q 不在受控词表 {approver, action, none} 内"+
				"（约定正本：chain.json#conventions.node_actor_kind）", key, kind)
		}
		// 反向（防「spec 漏标」把实现挡住）：Go 认为可执行的 actor，spec 必须如实标注。
		if approved && kind != "approver" {
			t.Errorf("Go `approverRoles` 含 %s，但 spec 标 node_actor_kind=%q（★ spec 与实现漂移）", key, kind)
		}
		if acted && kind != "action" {
			t.Errorf("Go `isActionActor` 含 %s，但 spec 标 node_actor_kind=%q（★ spec 与实现漂移）", key, kind)
		}
	}

	// ① 三值都要真实出现（防词表被掏空成单值 ⇒ 本钉失去鉴别力）。
	for _, k := range []string{"approver", "action", "none"} {
		if seen[k] == 0 {
			t.Errorf("受控值 %q 在 roles 表里**一个都没有**（词表被掏空？本钉失去鉴别力）", k)
		}
	}
	// ② `approverRoles` 的每个键都必须在 spec roles 表里能找到（否则该 actor 无角色定义）。
	for key := range approverRoles {
		if _, ok := doc.Roles[key]; !ok {
			t.Errorf("Go `approverRoles` 含 %s，但 spec/chain.json#roles 没有该键（角色定义缺失）", key)
		}
	}
	// ③ `system` 必须**不在** roles 表（它是动作 actor，由 S19 的 extra_allowed 承载）。
	//    否则同一 actor 出现两处定义 —— 那正是「第三份真相」的入口。
	if _, ok := doc.Roles["system"]; ok {
		t.Error("chain.json#roles 不得包含 `system`（动作 actor，不在角色表内；见 checks.json#S19 的 extra_allowed）")
	}
}
