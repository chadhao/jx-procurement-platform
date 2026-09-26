//go:build !windows

package singlelock

import "syscall"

// platformAcquire 在类 Unix 平台使用 flock(LOCK_EX|LOCK_NB)。
// flock 随进程退出自动释放，天然规避僵尸锁。
func platformAcquire(l *Lock) error {
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return ErrLocked
	}
	return nil
}

// platformRelease 释放 flock。
func platformRelease(l *Lock) error {
	if l.file == nil {
		return nil
	}
	return syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
}
