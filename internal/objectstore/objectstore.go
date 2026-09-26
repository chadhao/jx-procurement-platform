// Package objectstore 附件对象存储抽象。
//
// ★ 口径（架构 §7.5 / ADR-08）：**主存＝云侧 S3 兼容对象存储，异地备份＝RustFS**。
//
// ★ 现状与取舍（B39）：
//   - 本包先提供 **`LocalStore`（本地文件系统）** 作为**可运行的最小实现**，供开发与联调走通
//     「下载 → 落主存 → 按引用取回」整条链；
//   - **S3 实现待定**：接入 S3 需要 SDK 或自写 SigV4 签名（前者要引入新依赖，按本项目纪律
//     **新增依赖需先经用户同意**）。故此处只留接口与文档，**不擅自引入依赖**；
//   - 凭据/依赖一旦到位，只需实现 `Store` 接口并在装配处换一行，**业务代码零改动**。
//
// ★ 安全：`key` 来自飞书 `file_id`，一律经 `sanitizeKey` 收敛（拒绝路径穿越、
// 只允许 `[A-Za-z0-9._-]`），绝不允许外部输入直接落到文件路径上。
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound 对象不存在。
var ErrNotFound = errors.New("objectstore: 对象不存在")

// Store 附件对象存储。
type Store interface {
	// Put 写入对象（幂等：同 key 覆盖）。
	Put(ctx context.Context, key string, data []byte) error
	// Get 读取对象；不存在返回 ErrNotFound。
	Get(ctx context.Context, key string) ([]byte, error)
	// Has 判断对象是否存在。
	Has(ctx context.Context, key string) (bool, error)
	// Kind 返回后端类型（"local" / "s3"），用于回读校验与排障。
	Kind() string
}

// LocalStore 本地文件系统实现（开发/联调用；生产应以 S3 为主存）。
type LocalStore struct {
	dir string
}

var _ Store = (*LocalStore)(nil)

// NewLocal 构造本地存储；dir 为空时返回 nil（调用方据此降级为"不落盘、直接转发"）。
func NewLocal(dir string) (*LocalStore, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("objectstore: 创建附件目录失败: %w", err)
	}
	return &LocalStore{dir: dir}, nil
}

// Kind 后端类型。
func (l *LocalStore) Kind() string { return "local" }

// Put 写入对象（先写临时文件再改名，避免读到半个文件）。
func (l *LocalStore) Put(_ context.Context, key string, data []byte) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return fmt.Errorf("objectstore: 写附件失败: %w", err)
	}
	return os.Rename(tmp, p)
}

// Get 读取对象。
func (l *LocalStore) Get(_ context.Context, key string) ([]byte, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("objectstore: 读附件失败: %w", err)
	}
	return b, nil
}

// Has 判断对象是否存在。
func (l *LocalStore) Has(_ context.Context, key string) (bool, error) {
	p, err := l.path(key)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(p); err == nil {
		return true, nil
	} else if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else {
		return false, err
	}
}

// path 由 key 推导绝对路径（已做穿越防护）。
func (l *LocalStore) path(key string) (string, error) {
	k, err := sanitizeKey(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(l.dir, k), nil
}

// sanitizeKey 收敛 key：去空白、拒绝空 / `.` / `..` / 含路径分隔符 / 含非法字符。
//
// ★ 必须严防路径穿越：key 直接来自外部（file_id），若含 `../` 就能读写目录外文件。
func sanitizeKey(key string) (string, error) {
	k := strings.TrimSpace(key)
	if k == "" || k == "." || k == ".." {
		return "", fmt.Errorf("objectstore: 非法对象键 %q", key)
	}
	if strings.ContainsAny(k, `/\:`) || strings.Contains(k, "..") {
		return "", fmt.Errorf("objectstore: 对象键含路径分隔符或上跳，已拒绝: %q", key)
	}
	for _, r := range k {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.'
		if !ok {
			return "", fmt.Errorf("objectstore: 对象键含非法字符 %q: %q", r, key)
		}
	}
	return k, nil
}
