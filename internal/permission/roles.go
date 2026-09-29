package permission

// 角色与资源枚举 —— **权限域的单一权威源**（批 0 · A4）。
//
// ★ 为什么从 seed 迁入：seed 是播种器，不是枚举的家 —— HTTP 层曾 `import seed`
//   只为读角色名（命名与职责不符，审计问题 18）。枚举归权限域后：
//   seed 播种引用本包（数据与枚举同源），handlers 直接引用本包（不再借道播种器）。
// ★ 内容与原 internal/seed/seed.go 逐字一致（迁移不改口径；PRD §4.1 —— 采购岗/
//   财务岗/出纳已取消，不得出现）。

// Roles 角色枚举（t_user_role.role 的合法值域）。
var Roles = []string{
	"申请人", "主管领导", "项目总经理", "副总", "综合运营主管",
	"采购经办人", "验收人", "集团财务", "集团（审批）", "系统管理员",
}

// Resources 权限矩阵覆盖的资源枚举。
// 至少覆盖 ledger:* / dashboard:1..4 / api:instances（API §3.9）；另补 api:audit 供审计页使用。
var Resources = []string{
	"ledger:*",
	"dashboard:1", "dashboard:2", "dashboard:3", "dashboard:4",
	"api:instances",
	"api:audit",
}
