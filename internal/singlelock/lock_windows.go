//go:build windows

package singlelock

import (
	"syscall"
	"unsafe"
)

// Windows 文件锁：调用 kernel32!LockFileEx / UnlockFileEx（纯 stdlib，无 cgo）。
// 锁随进程句柄关闭自动释放，规避僵尸锁。

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
	errorLockViolation      = syscall.Errno(33) // ERROR_LOCK_VIOLATION
)

// overlapped 对应 Win32 OVERLAPPED 结构（此处仅需锁区间定位）。
type overlapped struct {
	internal     uintptr
	internalHigh uintptr
	offset       uint32
	offsetHigh   uint32
	hEvent       syscall.Handle
}

// platformAcquire 非阻塞独占锁定锁文件首字节。
func platformAcquire(l *Lock) error {
	ov := &overlapped{}
	r1, _, e1 := procLockFileEx.Call(
		l.file.Fd(),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0,
		1, 0,
		uintptr(unsafe.Pointer(ov)),
	)
	if r1 == 0 {
		if e1 == errorLockViolation {
			return ErrLocked
		}
		if e1 != syscall.Errno(0) {
			return e1
		}
		return ErrLocked
	}
	l.plat = ov
	return nil
}

// platformRelease 释放锁区间。
func platformRelease(l *Lock) error {
	ov, ok := l.plat.(*overlapped)
	if !ok || ov == nil {
		return nil
	}
	r1, _, e1 := procUnlockFileEx.Call(
		l.file.Fd(),
		0,
		1, 0,
		uintptr(unsafe.Pointer(ov)),
	)
	if r1 == 0 && e1 != syscall.Errno(0) {
		return e1
	}
	l.plat = nil
	return nil
}
