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
