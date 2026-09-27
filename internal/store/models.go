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
	CreatedAt       time.Time
	UpdatedAt       time.Time

	// ★ 架构转向 ③（04a §1.1）：以下 6 列为 migration 0007 新增，**只加列、不改既有列语义**。
	//   UpdateTime 为推送版本号，**单调递增**——不递增会让飞书侧推送**被拒且静默**（04a §3.1）。
	UpdateTime   int64      // 推送版本号（逻辑版本，非时间戳）
	PrevBizNo    string     // 重新发起时指向旧单号（撤回/驳回重提的因果链，04a §5.4）
	CancelReason string     // 撤回原因
	CancelAt     *time.Time // 撤回时刻
	PushHash     string     // 上次推送快照 hash（相同则跳过推送，不消耗 update_time）
	PushAt       *time.Time // 上次推送成功时刻
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
	ExtJSON        string
	CreatedAt      time.Time
	UpdatedAt      time.Time
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
	TaskID         string // 确定性 task_id（{node_id}-{assignee}-{round}-{seq}）
	BizNo          string // 关联业务单号
	NodeID         string // 节点标识（会签聚合键）
	NodeName       string // 节点名（展示）
	NodeSeq        int    // 节点顺序
	Round          int    // 轮次（回退重激活 +1）
	AssigneeOpenID string // 审批人
	AssigneeName   string // 审批人姓名（展示）
	Status         string // PENDING/APPROVED/REJECTED/TRANSFERRED/DONE
	ActionContext  string // 回调定位（原样回传）
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ClosedAt       *time.Time // 终结时刻
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
	CreatedAt   time.Time
}

// ApprovalDef 三方审批定义注册表行（t_approval_def，04a §1.1 / §3）。
type ApprovalDef struct {
	ApprovalCode     string // PK（命中即更新、未命中即新建）
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
	DefVersion       int
	UpdatedAt        time.Time
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
}
