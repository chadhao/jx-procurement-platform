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

// lockOffset 是 Windows 下被锁字节的偏移量。
//
// ★ 为什么不是 0（实测踩过）：
// Windows 的 `LockFileEx` 是**强制锁**（mandatory）—— 被锁区间不只挡写，
// **连读也挡**。若锁在偏移 0，第二个实例在获取失败后想读取锁文件里
// 「持有者 PID / 主机名」这段诊断信息，会直接读不到（表现为 `PID=未知`），
// 恰好把本包唯一的排查线索弄掉。
//
// 故把锁区间放到诊断区（前 512 字节）之外的高位偏移：锁语义不变
// （同一进程/他进程都要抢同一区间），但诊断信息对谁都可见。
// 注意 `LockFileEx` 允许锁定**超出文件末尾**的区间，无需先把文件撑大。
//
// Unix 侧用 `flock`，是**劝告锁**且作用于整个文件，读不受影响 —— 故
// `lock_unix.go` 无需对应改动。两端语义一致：独占、非阻塞、第二个失败。
const lockOffset = 1 << 20 // 1 MiB

// overlapped 对应 Win32 OVERLAPPED 结构（此处仅需锁区间定位）。
type overlapped struct {
	internal     uintptr
	internalHigh uintptr
	offset       uint32
	offsetHigh   uint32
	hEvent       syscall.Handle
}

// platformAcquire 非阻塞独占锁定锁文件 `lockOffset` 处的 1 字节。
func platformAcquire(l *Lock) error {
	ov := &overlapped{
		offset:     uint32(lockOffset & 0xFFFFFFFF),
		offsetHigh: uint32(uint64(lockOffset) >> 32),
	}
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
