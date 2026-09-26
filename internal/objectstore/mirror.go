package objectstore

import (
	"context"
	"errors"
	"log/slog"
)

// MirrorStore 主备双写：**主存 S3 + 异地备份 RustFS**（架构 §7.5 / ADR-08）。
//
// 语义（刻意不对称，理由写在每一条上）：
//
//	Put：**先写主存**（主存失败＝本次失败，向外报错）；**再写备份**（备份失败只记 warn、不影响结果）。
//	     理由：主存是权威副本，写不进去就必须让调用方知道；备份是异地冗余，
//	     它的失败**不应该**让用户的下载请求失败 —— 但**必须留痕**，否则"备份静默失效"无人察觉。
//	Get：先主存；主存 404 或不可用时**回退备份**（异地兜底的价值正在于此），并记 warn。
//	Has：同 Get 的降级次序。
//
// ★ 为什么备份要读：主存故障（如云侧对象存储欠费/被删）时，异地备份是唯一的恢复手段；
// 只写不读等于"备份从未被验证过"。
type MirrorStore struct {
	primary Store
	backup  Store
	log     *slog.Logger
}

var _ Store = (*MirrorStore)(nil)

// NewMirror 构造主备存储；backup 为 nil 时直接返回 primary（不做无意义的包装）。
func NewMirror(primary, backup Store, log *slog.Logger) Store {
	if primary == nil {
		return nil
	}
	if backup == nil {
		return primary
	}
	return &MirrorStore{primary: primary, backup: backup, log: log}
}

// Kind 形如 "s3+rustfs"，便于启动日志与排障直接看出双写是否生效。
func (m *MirrorStore) Kind() string { return m.primary.Kind() + "+" + m.backup.Kind() }

// Put 先写主存（失败即失败），再写备份（失败仅留痕）。
func (m *MirrorStore) Put(ctx context.Context, key string, data []byte) error {
	if err := m.primary.Put(ctx, key, data); err != nil {
		return err
	}
	if err := m.backup.Put(ctx, key, data); err != nil {
		m.warn("附件异地备份写入失败（主存已成功，本次不受影响）", key, err)
	}
	return nil
}

// Get 先主存，失败或未命中则回退备份。
func (m *MirrorStore) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := m.primary.Get(ctx, key)
	if err == nil {
		return b, nil
	}
	if !errors.Is(err, ErrNotFound) {
		m.warn("附件主存读取失败，回退异地备份", key, err)
	}
	backupBytes, backupErr := m.backup.Get(ctx, key)
	if backupErr != nil {
		// 两边都失败：把**主存的错误**作为主因返回（它才是权威副本的问题），备份错误附在后面。
		if errors.Is(err, ErrNotFound) && errors.Is(backupErr, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, errors.Join(err, backupErr)
	}
	return backupBytes, nil
}

// Has 先主存，失败或不存在则看备份。
func (m *MirrorStore) Has(ctx context.Context, key string) (bool, error) {
	ok, err := m.primary.Has(ctx, key)
	if err == nil && ok {
		return true, nil
	}
	if err != nil {
		m.warn("附件主存存在性检查失败，回退异地备份", key, err)
	}
	okBackup, backupErr := m.backup.Has(ctx, key)
	if backupErr != nil {
		return false, errors.Join(err, backupErr)
	}
	return okBackup, nil
}

// warn 记录降级/失败（logger 为 nil 时静默——仅测试环境会出现）。
func (m *MirrorStore) warn(msg, key string, err error) {
	if m.log == nil {
		return
	}
	m.log.Warn(msg, "key", key, "error", err.Error())
}
