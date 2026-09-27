#### `GET /api/foo`（已注册·已文档 → 两向都不报）

#### `PATCH /api/foo/{id}`（已注册·已文档 → 两向都不报）

#### `GET /api/documented_only`（**只文档、未注册** → 应触发「reverse」）

| 变更 | 端点 |
| --- | --- |
| Vx | `GET/POST/PATCH /api/foo` —— 链式概览，**不应**派生 `PATCH foo` 幻影 |
