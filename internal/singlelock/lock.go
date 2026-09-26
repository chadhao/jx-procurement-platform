// Package singlelock 提供进程级单实例文件锁（架构 §7.2 / ADR-01 / TC-04）。
// 契约：第二个实例获取锁失败必须以非 0 退出码拒绝启动，并给出明确日志。
package singlelock

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ErrLocked 表示锁已被其他实例持有。
var ErrLocked = errors.New("singlelock: 单实例锁已被其他进程持有")

// Lock 表示一个独占文件锁。
type Lock struct {
	path string
	file *os.File
	plat any // 平台相关的锁状态（Windows 为 OVERLAPPED）
}

// New 构造锁对象（路径通常为 <datadir>/jxapproval.lock）。
func New(path string) *Lock {
	return &Lock{path: path}
}

// Path 返回锁文件路径。
func (l *Lock) Path() string { return l.path }

// Acquire 尝试获取独占非阻塞锁；失败返回 ErrLocked 或底层错误。
func (l *Lock) Acquire() error {
	if l.path == "" {
		return errors.New("singlelock: 锁路径为空")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("singlelock: 打开锁文件失败: %w", err)
	}
	l.file = f

	if err := platformAcquire(l); err != nil {
		_ = f.Close()
		l.file = nil
		if errors.Is(err, ErrLocked) {
			return fmt.Errorf("%w（锁文件 %s，持有者 PID=%s）", ErrLocked, l.path, readPID(f))
		}
		return fmt.Errorf("singlelock: 获取文件锁失败: %w", err)
	}

	// 持锁后写入本进程 PID，便于排查僵尸锁。
	_ = f.Truncate(0)
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		// 写入 PID 仅用于诊断，失败不视为获取锁失败。
		_ = err
	}
	return nil
}

// Release 释放锁并关闭文件。
func (l *Lock) Release() error {
	if l.file == nil {
		return nil
	}
	err := platformRelease(l)
	if cerr := l.file.Close(); err == nil {
		err = cerr
	}
	l.file = nil
	return err
}

// readPID 读取锁文件中记录的 PID（可能为空）。
func readPID(f *os.File) string {
	buf := make([]byte, 32)
	n, err := f.ReadAt(buf, 0)
	if err != nil && n == 0 {
		return "未知"
	}
	return strings.TrimSpace(string(buf[:n]))
}
