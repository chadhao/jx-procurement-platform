package store

import "time"

// ConfigMappingRow 配置映射表行（approval_code / field_id / threshold / enum）。
type ConfigMappingRow struct {
	MapKind  string
	MapKey   string
	MapValue string
	DocType  string
	Remark   string
}

// Instance 实例主表模型（M2）。
type Instance struct {
	ID              int64
	InstanceCode    string
	ApprovalCode    string
	DocType         string
	BizNo           string
	BizNoPrefix     string
	BizNoYYMM       string
	BizNoSeq        string
	Status          string
	StatusRaw       string
	ApplicantOpenID string
	ApplicantName   string
	Department      string
	AmountCents     *int64
	PurposeClassL1  string
	PurposeClassL2  string
	Supplier        string
	Source          string
	// ★ N-042 权限位点（0019）：行级 ASSIGNED / PARTICIPATED 的数据承载。
	//   写入端＝designation 同事务写 designated_open_id（SetInstanceDesignatedTx）、
	//   GR 提交写 acceptors（SetInstanceAcceptors）；消费端＝RowFilterForInstances 规范列过滤。
	DesignatedOpenID string // 被指定经办人（ASSIGNED）
	Acceptors        string // 验收人集合（PARTICIPATED，JSON 数组串）
	CreatedAt        time.Time
	UpdatedAt        time.Time

	// ★ 架构转向 ③（04a §1.1）：以下 6 列为 migration 0007 新增，**只加列、不改既有列语义**。
	//   UpdateTime 为推送版本号，**单调递增**——不递增会让飞书侧推送**被拒且静默**（04a §3.1）。
	UpdateTime   int64      // 推送版本号（逻辑版本，非时间戳）
	PrevBizNo    string     // 重新发起时指向旧单号（撤回/驳回重提的因果链，04a §5.4）
	CancelReason string     // 撤回原因
	CancelAt     *time.Time // 撤回时刻
	PushHash     string     // 上次推送快照 hash（相同则跳过推送，不消耗 update_time）
	PushAt       *time.Time // 上次推送成功时刻
	// ★ 0010 增列：非规范表单字段（键值 JSON）。Submit 时按「规范字段→列 / 非规范字段→此列」分流构造；
	//   finalize 落台账时据此带入 `t_ledger_archive.ext_json`（契约键 `contract_no`/`related_biz_no`，决策 #28）。
	ExtJSON string
}

// InstanceField 实例表单字段（键值对，M2）。
type InstanceField struct {
	ID           int64
	InstanceCode string
	FieldID      string
	FieldName    string
	BizField     string
	ValueText    string
	ValueType    string
	RawJSON      string
	CreatedAt    time.Time
}

// StatusHistory 状态变更史（追加式，M2/M7）。
type StatusHistory struct {
	ID             int64
	InstanceCode   string
	Status         string
	TaskNode       string
	OperatorOpenID string
	Opinion        string
	OccurredAt     time.Time
	EventSeq       int64
	CreatedAt      time.Time
}

// InboxRow 事件收件箱行（幂等，M3）。
type InboxRow struct {
	ID           int64
	IdemKey      string
	EventType    string
	InstanceCode string
	Status       string
	EventID      string
	Payload      string
	ProcessState string
	RetryCount   int
	LastError    string
	ReceivedAt   time.Time
	ProcessedAt  *time.Time
}

// WorkerJob 异步作业行（M3）。
type WorkerJob struct {
	ID        int64
	InboxID   int64
	JobType   string
	State     string
	Attempts  int
	NextRunAt *time.Time
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Deadletter 死信行（M3）。
type Deadletter struct {
	ID             int64
	InboxID        int64
	Reason         string
	Payload        string
	ReplayCount    int
	CreatedAt      time.Time
	LastReplayedAt *time.Time
}

// SyncCursor 同步游标行（M0）。
type SyncCursor struct {
	ID           int64
	ApprovalCode string
	CursorKind   string
	LastSyncedAt *time.Time
	LastRunAt    *time.Time
	LastMissing  int
	LastFilled   int
}

// SubscribeState 订阅状态行（M0）。
type SubscribeState struct {
	ID            int64
	ApprovalCode  string
	DocType       string
	Subscribed    bool
	LastResult    string
	LastError     string
	LastAttemptAt *time.Time
	UpdatedAt     time.Time
}

// ---------- 通讯录镜像（migration 0012；docs/08 §4.2） ----------
//
// ★★ 镜像 ≠ 权限（docs/08 §4.10）：以下模型只承载「人事目录」属性，
// 不含任何准入/角色列；与 t_user_role 物理分离、无外键。软删（IsDeleted）只置标记，
// 永不物理删除；软删行仍参与历史名称解析（解析查询不得过滤 is_deleted，docs/08 C-E）。

// OrgDepartment 部门镜像行。权威 ID＝OpenDepartmentID（od-，系统生成不可编辑）；
// DepartmentID 为可变自定义 ID（桥接键），每次全量刷新。
type OrgDepartment struct {
	OpenDepartmentID       string // 根部门 = "0"
	DepartmentID           string
	ParentOpenDepartmentID string
	Name                   string
	NamePath               string // 全路径（A/B/C，本地派生）
	IsDeleted              bool
	RawJSON                string
	FirstSeenAt            time.Time
	LastSeenAt             time.Time
	UpdatedAt              time.Time
	Source                 string // full / manual / event
}

// OrgUser 人员镜像行。
type OrgUser struct {
	OpenID              string
	UnionID             string
	UserID              string
	Name                string
	EmployeeStatus      string // 归一在职态：在职/离职/冻结/未激活/未入职
	IsResigned          bool
	IsExited            bool
	IsFrozen            bool
	IsActivated         bool
	IsUnjoin            bool
	PrimaryDepartmentID string
	DepartmentIDs       []string
	IsDeleted           bool
	RawJSON             string
	FirstSeenAt         time.Time
	LastSeenAt          time.Time
	UpdatedAt           time.Time
	Source              string
}

// OrgSyncState 通讯录同步状态（单行表 t_org_sync_state）。
type OrgSyncState struct {
	LastFullAttemptAt time.Time
	LastFullSuccessAt time.Time // 失败不推进（N6）
	LastFullError     string
	LastFullDeptCount int
	LastFullUserCount int
	LastEventAt       time.Time
	UpdatedAt         time.Time
}

// OrgSyncRun 全量运行流水行（追加式，供差异报告可见）。
type OrgSyncRun struct {
	ID              int64
	RunAt           time.Time
	Trigger         string // startup / weekly / manual
	Result          string // ok / failed
	DeptAdded       int
	DeptUpdated     int
	DeptSoftDeleted int
	UserAdded       int
	UserUpdated     int
	UserSoftDeleted int
	FieldGapsJSON   string
	Error           string
	DurationMS      int64
}

// OrgUserView 人员目录展示视图（/api/org/users 与办理人展示兜底用）：
// 镜像提供姓名/部门（全员覆盖），角色仍来自 t_user_role（镜像 ≠ 权限）。
type OrgUserView struct {
	OpenID     string
	Name       string
	Role       string // 未配角色者＝空串
	Department string // 主部门名称（镜像解析；根部门/无部门＝空串）
}

// UserRole 用户角色映射行（M5）。
type UserRole struct {
	ID         int64
	OpenID     string
	Name       string
	Role       string
	Department string
	ExtraDepts []string
	Active     bool
	UpdatedAt  time.Time
}

// PermissionRule 行·列权限规则行（M5，配置驱动）。
type PermissionRule struct {
	ID             int64
	Resource       string
	Role           string
	RowScope       string
	ColumnAllow    []string
	ColumnDeny     []string
	WritableFields []string
	EffectiveFrom  *time.Time
	Remark         string
	UpdatedAt      time.Time
}

// LedgerArchive 台账·同步存档行（只读，M4）。
type LedgerArchive struct {
	ID              int64
	LedgerType      string
	BizNo           string
	InstanceCode    string
	SourceDocType   string
	Department      string
	ApplicantOpenID string
	SubmitterOpenID string
	AmountCents     *int64
	Supplier        string
	// SupplierNorm 供应商名称**归一分组键**（PRD Q20）：去空白 + 全角半角归一 + 大小写归一。
	// 仅用于分组/比较（防拆分「同供应商当月累计」）；**展示一律用 Supplier 原名**。
	// 写入点收在 store.upsertArchive 一处，保证「列加了就一定有写入者」。
	SupplierNorm   string
	PurposeClassL1 string
	PurposeClassL2 string
	BizDate        string
	// ★ N-042 权限位点（0019）：行级 ASSIGNED / PARTICIPATED 的数据承载。
	//   写入端＝finalize 从 ext_json 取 designated_purchaser / acceptors（同源 ext ⇒ 两处一致）。
	DesignatedOpenID string // 被指定经办人（ASSIGNED）
	Acceptors        string // 验收人集合（PARTICIPATED，JSON 数组串）
	ExtJSON          string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// LedgerOps 台账·运营表行（可写，M4）。
type LedgerOps struct {
	ID         int64
	LedgerType string
	BizNo      string
	OpsJSON    string
	UpdatedBy  string
	UpdatedAt  time.Time
}

// Submission 报送登记行（M6）。
type Submission struct {
	ID           int64
	BizNo        string
	SubjectType  string
	AmountCents  *int64
	PayMethod    string
	HNFinishDate string
	SubmitDate   string
	ReceiptRef   string
	SubmitState  string
	GrpAcceptNo  string
	GrpState     string
	PaidDate     string
	RejectReason string
	// ★ Q14-B 第 5 项（2026-09-26）：行级权限按真实列重建 —— 以下四列由 migrations/0003 加入，
	//   使 DEPT / CHARGE_DEPT / ASSIGNED / PARTICIPATED 四个 row_scope 令牌不再降级为 created_by。
	Department      string // 申请人所属部门（DEPT / CHARGE_DEPT）
	ApplicantOpenID string // 申请人（SELF）
	AssignedOpenID  string // 被指定经办人（ASSIGNED）
	Acceptors       string // 验收人集合（PARTICIPATED，JSON 数组串）
	CreatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SubmissionItem 报送关联单据行（M6）。
type SubmissionItem struct {
	ID           int64
	SubmissionID int64
	ItemBizNo    string
	ItemType     string
}

// ExpenseTrack 集团报销跟踪表行（M1，人工登记，FR-M1-02）。
//
// 口径：报销类由人工走集团，**不进入本办法审批流程**；本表只做「审批外登记」，
// 供台账与看板引用（关联事前申请单号 → 移交 → 集团付款）。
type ExpenseTrack struct {
	ID              int64
	SrcBizNo        string
	ApplicantOpenID string
	Department      string
	ActualCents     *int64
	InvoiceCount    *int
	ReviewState     string
	HandoverDate    string
	PaidDate        string
	PaidCents       *int64
	OverrunNote     string
	CreatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// AuditLogRow 审计日志行（M7）。
type AuditLogRow struct {
	ID          int64
	ActorOpenID string
	ActorRole   string
	Action      string
	Resource    string
	TargetID    string
	Result      string
	DetailJSON  string
	FeishuLogID string
	IP          string
	CreatedAt   time.Time
}

// LedgerFieldDef 台账字段定义行（M4）。
type LedgerFieldDef struct {
	ID          int64
	LedgerType  string
	FieldKey    string
	FieldLabel  string
	IsFormula   bool
	FormulaKind string
	IsSensitive bool
}

// ---------- 架构转向 ③ · 审批核心（migration 0007，04a §1.1） ----------

// DocSeq 单据编号器游标行（PK(doc_type,yymm)）。
//
// ★ 锁号前提 P1：本表**单调递增、只增不减、不参与任何归档/清理**（04a §6.4）。
// 破坏（如归档后清表使游标归零）会导致**终态单号被复用**，且**静默**。
type DocSeq struct {
	DocType   string
	YYMM      string
	LastSeq   int64
	UpdatedAt time.Time
}

// FlowTask 我方任务/节点行（t_flow_task，04a §1.1）。
// 状态：PENDING/APPROVED/REJECTED/TRANSFERRED/DONE；会签聚合按 NodeID 分组。
type FlowTask struct {
	TaskID         string // 确定性 task_id（{biz_no}-{node_id}-{assignee}-{round}-{seq}；含 biz_no 以保全库唯一）
	BizNo          string // 关联业务单号
	NodeID         string // 节点标识（会签聚合键）
	NodeName       string // 节点名（展示）
	NodeSeq        int    // 节点顺序
	Round          int    // 轮次（回退重激活 +1）
	AssigneeOpenID string // 审批人
	AssigneeName   string // 审批人姓名（展示）
	Status         string // PENDING/APPROVED/REJECTED/TRANSFERRED/DONE
	ActionContext  string // 回调定位（原样回传）
	// ★ 0008 增列（04a §1.1 / §2.3）：顺序会签「分段释放」。
	//   HELD＝未释放（飞书侧不推、不生成待办）；RELEASED＝已释放（当前可办理）。
	ReleaseState string // HELD / RELEASED
	// ★ 0008 增列：票签 / 并行会签扩展位；一期「不可配为并签」→ 恒 nil。
	Weight *int
	// ★ 0009 增列：同节点内审批人**声明序**（1-based）。顺序会签释放次序键。
	//   ★ 唯一顺序契约：禁止回退到按 `rowid` 排序（rowid 会随 REPLACE/VACUUM 漂移）。
	TaskOrder int
	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  *time.Time // 终结时刻
}

// FlowOpLog 操作留痕行（t_flow_op_log，04a §1.1）。
type FlowOpLog struct {
	OpID        int64
	BizNo       string
	NodeID      string
	TaskID      string
	OpType      string // SUBMIT/APPROVE/REJECT/TRANSFER/ADDSIGN/ROLLBACK/CANCEL
	ActorOpenID string
	FromStatus  string
	ToStatus    string
	Reason      string
	ExtraJSON   string
	// ★ 0011 增列：轮次（回退重激活 +1）。回调幂等键含 round（ux_flow_op_callback），
	//   回退复用同一 task_id 后再次 APPROVE/REJECT 不得被误判为重复回调（静默不推进）。
	//   仅 APPROVE/REJECT 两处写入点要求携带任务当前 round；其余 op_type 保持 0。
	Round int
	// ★ 0013 增列：飞书回调报文 `message_id`（卡片操作时官方必填）。
	//   写入者＝recordCallback（internal/flow/callback.go，docs/16 §2-A-4）；
	//   消费者＝第 3 批「失败时调 message/update 更新卡片」（docs/16 §2-F，本期只落盘）。
	//   ★ 可空、无回填（既有行无对应卡片语义，0013 头注释既定）。
	MessageID string
	CreatedAt time.Time
}

// ApprovalDef 三方审批定义注册表行（t_approval_def，04a §1.1 / §3）。
type ApprovalDef struct {
	ApprovalCode     string // PK（命中即更新、未命中即新建）；★ 恒为我方自定义 code（docs/16 G-8）
	DocType          string // 我方单据类型（11 类；唯一）
	Name             string
	GroupName        string
	VisibleScopeJSON string
	CreateLinkPC     string
	CreateLinkMobile string
	CallbackURL      string
	CallbackToken    string
	CallbackKey      string
	FormSummaryJSON  string
	// ★ 0013 增列：飞书侧「真实 code」候选（docs/16 G-8 双 code 池）。
	//   写入者＝Registry.Register（把 POST external_approvals 响应回填值落此列）；
	//   读取者＝Pusher.Push（推实例优先取 feishu_code，空则回退 approval_code）。
	//   ★ 双池归属未实测（docs/16 §7 V-4）⇒ 双写、不猜。
	FeishuCode string
	DefVersion int
	UpdatedAt  time.Time
}

// PushRecord 推送流水行（t_push_record，04a §1.1 / §10 S4）。
type PushRecord struct {
	ID           int64
	BizNo        string
	PushSeq      int64
	SnapshotHash string
	Status       string // PENDING/SENT/FAILED
	Attempts     int
	LastError    string
	CreatedAt    time.Time
	SentAt       *time.Time
}

// NotifyLog 通知流水行（t_notify_log，04a §1.1 / §5.5；漏发可检出）。
type NotifyLog struct {
	ID           int64
	BizNo        string
	TargetOpenID string
	Channel      string // feishu_bot / inapp
	Event        string
	Status       string // PENDING/SENT/FAILED
	Attempts     int
	LastError    string
	SentAt       *time.Time
	CreatedAt    time.Time
	// ★ 0014 增列（可空、无回填）：message/send 成功回执的 data.message_id，
	//   由 NotifySender.Send 写入。卡片操作的回调报文【不带】message_id（2026-09-28
	//   实测留痕为空字段）⇒ 卡片刷新（message/update）只能靠本列定位卡片。
	MessageID string
}
