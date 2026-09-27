package httpapi

// 探针（probe）：仅供 scripts/audit_silent.py 的 C5 双向检查做「能报」验证。
// 目录以 `_` 开头 → 对齐 Go 工具链，真扫描会自动忽略它（见 audit_silent.py 的 walk）。
//
// 复刻真语料里那处误报的**形状**：文档用**链式概览** `GET/POST/PATCH /api/foo`（集合路径、
// 无参数），而代码里 `PATCH` 只存在于**带参**路径 `/api/foo/:id`。
//
//	· 若把链式概览里的 `PATCH` 当真声明 → 会派生幻影 `PATCH foo` ↔ 注册的 `PATCH foo/*`
//	  对不上 → 反向**误报**（正是变更记录 V1.1 行 `GET/POST/PATCH /api/admin/users` 的老病）。
//	· 加了「方法前是 `/` ⇒ 链式概览，跳过」的降噪后 → 不再派生幻影。
func NewRouter() {
	e.GET("/api/foo", h)
	e.POST("/api/foo", h)
	e.PATCH("/api/foo/:id", h) // PATCH 只在带参路径上
	// 只注册、不文档 → 应触发「forward」（注册未文档）。
	e.GET("/api/registered_only", h)
}
