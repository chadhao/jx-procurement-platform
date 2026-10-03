# 19 · 权限位点规格（T04 · 行级 `row_scope` 的数据承载）

> **定位**：`docs/18 §3.2 #2①` 那句「`T04` 权限位点（`t_instance` / `t_ledger_archive` / `t_user_role` 新列）」
> 的**展开规格**。★ 该句是**压缩表述**，本文把它落成可交办的列清单。
> **权威源**：`docs/01-PRD.md §4.2`（行·列口径，Q3 定案）· `FR-M5-09/10/11` · `internal/permission/dataset.go`（现有实现）
> **编制**：WorkBuddy（2026-10-03）· **状态**：待 mimo 实现（`COLLAB.md#N-042`）

---

## 0. 一句话结论

★ **`t_submission` 已经被同款问题修过（`migrations/0003`），但 `t_instance` 与 `t_ledger_archive` 没有做同样的修复**
⇒ 所以 `ASSIGNED` / `PARTICIPATED` 两个令牌在**实例主表上恒为 `1=0`**（`dataset.go#RowFilterForInstances` 明写）。
本文补的就是**这两张表的两列**。★ `t_user_role` **不需要新列**（`extra_depts` 已在，见 §2）。

---

## 1. 现状盘点（已核，非推断）

| `row_scope` 令牌 | 需要的数据 | `t_submission` | `t_instance` | `t_ledger_archive` |
|---|---|---|---|---|
| `SELF` | 申请人 | ✅ `applicant_open_id` | ✅ `applicant_open_id` | ✅ `applicant_open_id` |
| `DEPT` | 部门 | ✅ `department` | ✅ `department` | ✅ `department` |
| `CHARGE_DEPT` | 用户分管部门 | ✅（来自 `t_user_role.extra_depts`） | ✅ 同左 | ✅ 同左 |
| `ALL` | — | ✅ 无需列 | ✅ | ✅ |
| **`ASSIGNED`** | **被指定经办人** | ✅ `assigned_open_id`（`0003` 补） | ❌ **缺列 ⇒ `1=0`** | ❌ **缺列** |
| **`PARTICIPATED`** | **验收人集合** | ✅ `acceptors`（`0003` 补） | ❌ **缺列 ⇒ `1=0`** | ❌ **缺列** |

★ **`1=0` 的语义**：fail-closed（宁可少不可多），**不是安全漏洞**；但它使
「采购经办人只能看本人被指定经办的记录」「验收人只能看本人参与验收的记录」（`§4.2`）**在本系统上一次都没生效过** —— 这两种角色看到的是**空集**。

---

## 2. `t_user_role` 为何不需要新列

- 「分管部门」的承载 **已存在**：`t_user_role.extra_depts`（`TEXT`，JSON 数组，`0001_init.sql` 注释原文「**分管部门**（JSON 数组，供行级范围令牌）」）。
- 「启用 / 停用」的承载 **已存在**：`t_user_role.active`（`INTEGER NOT NULL DEFAULT 1`）。
- 「未映射默认拒绝」的承载 **已存在**：`open_id UNIQUE` + 查不到即 `DENY`。
⇒ ★ **`t_user_role` 侧只需 UI（见 §5），无需 DDL 变更。** ★ 这也说明 `docs/18` 那句把三张表并列属于**压缩表述**。

---

## 3. 规格：两张表各补两列（照抄 `0003` 对 `t_submission` 的做法）

### 3.1 DDL（新增迁移 `0019_instance_scope.sql`，★ 编号顺延、勿复用）

```sql
-- 0019: t_instance / t_ledger_archive 行级权限**按真实列重建**（T04 权限位点）
--       同款先例：0003 对 t_submission 的修复（assigned_open_id / acceptors）。
-- 背景：两表原无「被指定经办人」与「验收人集合」列 ⇒ ASSIGNED / PARTICIPATED
--       只能一律 1=0（fail-closed，见 internal/permission/dataset.go），
--       使「采购经办人 / 验收人」两类角色在这两张表上恒见空集。
-- 注：SQLite 的 ALTER TABLE ... ADD COLUMN 不支持 IF NOT EXISTS；
--     由 t_schema_migrations 保证只执行一次（同 0003 注释口径）。

ALTER TABLE t_instance       ADD COLUMN designated_open_id TEXT;  -- 被指定经办人（ASSIGNED）
ALTER TABLE t_instance       ADD COLUMN acceptors        TEXT;  -- 验收人集合（PARTICIPATED，JSON 数组串）
ALTER TABLE t_ledger_archive ADD COLUMN designated_open_id TEXT;
ALTER TABLE t_ledger_archive ADD COLUMN acceptors        TEXT;

CREATE INDEX IF NOT EXISTS idx_instance_designated ON t_instance(designated_open_id);
CREATE INDEX IF NOT EXISTS idx_arch_designated     ON t_ledger_archive(designated_open_id);
```

### 3.2 谁写、何时写（★ 这是本规格的**关键**，决定了它会不会又变成「有列无数据」）

| 列 | 写入者 | 写入时点 | 依据 |
|---|---|---|---|
| `t_instance.designated_open_id` | **审批链「指定经办人」动作**（`internal/flow` 的 designation 路径） | 该节点**提交指定**时（与 `t_ledger_archive` 的 L03 同批，避免两处不一致） | `§4.2`「采购经办人：本人被指定经办的记录」；`L03` 已有同源数据 |
| `t_instance.acceptors` | **验收单（`GR`）提交**时按 `member_*` 落 | `GR` 提交/落账时 | `§4.2`「验收人：本人参与验收的记录」；`GR.member_purchaser/member_qc/member_warehouse/member_ops` 已由服务端按入库类型计算（见 `N-039` 的 `carried_by`） |
| `t_ledger_archive.designated_open_id` | 同上（L03 写入路径） | L03 落账时 | `L03` 的定义就是「采购经办登记台账」 |
| `t_ledger_archive.acceptors` | 同上（`GR` 落 L05/L07 时） | 落账时 | 与 `t_instance` 同源 |

★★ **硬要求**：四列**必须在写入路径上真正被赋值**；★ 否则就是「**有列无数据**」——与
`N-036`／`N-039` 反复踩过的「**声明了却没人执行**」同族。⇒ 见 §4 的**可机检判据**。

### 3.3 消费点（改 `1=0`）

`internal/permission/dataset.go#RowFilterForInstances` 现为：

```go
case ScopeAssigned, ScopeParticipated:
    return Condition{SQL: "1=0"}
```

改为：`ASSIGNED → designated_open_id = ?`、`PARTICIPATED → textOrJSONContains("acceptors")`（该 helper 已存在，专为「可能是 JSON 数组、也可能是普通文本」的列设计，且**绝不用 LIKE** —— 注释已说明 `ou_ab` 是 `ou_abc` 前缀的越权风险）。★ `t_ledger_archive` 侧同款处理。

---

## 4. 验收判据（可机检，★ 必须双向）

| # | 判据 | 为什么必须 |
|---|---|---|
| 1 | `ASSIGNED` 命中：造一条 `designated_open_id = ou_A` 的实例 ⇒ 以 `ou_A` 身份查询**能看见** | 正向 |
| 2 | `ASSIGNED` 不越权：以 `ou_B` 身份查询**看不见** `ou_A` 的记录 | ★ 反向（防「改成恒真」） |
| 3 | 同上两条对 `PARTICIPATED`（`acceptors` 为 JSON 数组） | 数组列专测 |
| 4 | **前缀越权专测**：`ou_ab` 不得命中 `ou_abc` 的记录 | ★ 防回退到 `LIKE` |
| 5 | **脏 JSON**：`acceptors` 写成非法 JSON ⇒ 该行**不参与匹配**（fail-closed），且**整条查询不报错**（`json_valid` 守卫） | 与既有实现同款约束 |
| 6 | `t_ledger_archive` 同款 1–5 | 两表对称 |
| 7 | ★ **写入端**：指定经办 ⇒ `t_instance.designated_open_id` 与 `t_ledger_archive.designated_open_id` **同时有值**；`GR` 提交 ⇒ `acceptors` 有值 | ★ **防「有列无数据」** |

---

## 5. 配套：`FR-M5-10` 人员角色管理页（＝ `REMAINING.md` 的 `A3`，与本文同批）

- **现状**：`web/src/views/Admin.vue` 仅三个页签（**权限规则 / 常量 / 角色代理人**）⇒ **无「人员角色管理」页**。
- **契约（依 `FR-M5-10`）**：维护 `t_user_role`（`open_id` ↔ 角色 / 部门 / **分管部门**），支持**启用 / 停用**；**未映射人员默认拒绝**。
- ★ **数据源必须用镜像，不得手填 `open_id`**：`GET /api/org/users`（已存在，`ApprovalConsole` 已在用于转交选人）⇒ 本页改为**从镜像选人**。
- ★ **停用即拒**：停用某人后其访问立即被拒（`FR-M5-10` 验收口径 `TC-34`）。
- ★ **留痕（`FR-M5-11`）**：人员角色变更写 `t_audit_log`（谁、何时、改前/改后 diff）。

---

## 6. 与其它文档的关系

| 文档 | 关系 |
|---|---|
| `docs/18 §3.2 #2` | 本文是其 `①`（权限位点）的展开；`②`（人员管理页）＝本文 §5 |
| `docs/01-PRD §4.2` | 行·列口径权威源（本文不重复，只补数据承载） |
| `migrations/0003` | ★ **同款先例**（`t_submission`）—— 本文即「把 0003 的做法补到另两张表」 |
| `COLLAB.md#N-042` | 交办入口（含验收判据与边界） |
