// Package singlelock 提供进程级单实例文件锁（架构 §7.2 / ADR-01 / TC-04）。
//
// 契约：第二个实例获取锁失败必须以非 0 退出码拒绝启动，并给出明确日志。
//
// ★ 关于「僵尸锁 / stale-lock」（架构 §11 问题 3，2026-09-27 定案）：
//
// 本包使用的原语在**进程消亡时由内核自动释放**锁（Unix `flock(LOCK_EX|LOCK_NB)`、
// Windows `LockFileEx`）。因此「持有者已死但锁仍被持有」在本机文件系统上
// **结构上不可能**，无需独立的 stale-lock 清理器。
//
// 特别地：**不要**添加「检测到陈旧就强行夺锁」的开关 —— 强抢会让两个实例
// 同时写库（SQLite WAL 多写者会互相踩），比拒绝启动危险得多。
//
// 真正的风险是「进程**存活但僵死**」：此时持锁是**正确行为**，应由 systemd
// 重启策略与端口独占兜底，不由本包处理。
//
// 锁文件内容（`PID=` / `HOST=` / `START=`）是**纯诊断信息**，不参与任何加锁判定；
// 获取失败时随错误一并输出，用于人工判读「占用者是谁」「是否来自另一台主机」
// （主机名与本机不一致 → 锁文件很可能位于共享存储，属**异常部署**）。
package singlelock

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrLocked 表示锁已被其他实例持有。
var ErrLocked = errors.New("singlelock: 单实例锁已被其他进程持有")

// Lock 表示一个独占文件锁。
type Lock struct {
	path string
	file *os.File
	plat any // 平台相关的锁状态（Windows 为 OVERLAPPED）
}

// Info 是锁文件里记录的持有者诊断信息。
//
// ★ **不参与加锁判定**：即便 Info 解析失败或为空，也不影响锁的获取与释放。
type Info struct {
	PID     int    // 写入者的进程号；0 表示未知
	Host    string // 写入者的主机名；空表示未知
	Started string // 写入者获取锁的时刻（RFC3339）；空表示未知
}

// String 渲染为单行诊断串。
func (i Info) String() string {
	pid := "未知"
	if i.PID > 0 {
		pid = strconv.Itoa(i.PID)
	}
	host := i.Host
	if host == "" {
		host = "未知"
	}
	s := fmt.Sprintf("PID=%s 主机=%s", pid, host)
	if i.Started != "" {
		s += " 启动于=" + i.Started
	}
	return s
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
		info := readInfo(f)
		_ = f.Close()
		l.file = nil
		if errors.Is(err, ErrLocked) {
			return fmt.Errorf("%w（锁文件 %s，持有者 %s）%s",
				ErrLocked, l.path, info.String(), foreignHostHint(info))
		}
		return fmt.Errorf("singlelock: 获取文件锁失败: %w", err)
	}

	// 持锁后写入诊断信息，便于排查占用者。
	// ★ 失败不视为获取锁失败：诊断信息缺失不影响单实例保证。
	_ = writeInfo(f)
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

// foreignHostHint 在「锁文件记录的主机名 ≠ 本机」时给出部署异常提示。
//
// 本机文件系统上不可能出现他人的锁记录；一旦出现，说明锁文件位于
// **共享 / 网络存储**（如 NFS 挂载点），而 `flock` 在部分网络文件系统上
// 语义不可靠 —— 这是应当人工介入的部署问题，必须在日志里说清楚。
func foreignHostHint(info Info) string {
	if info.Host == "" {
		return ""
	}
	self, err := os.Hostname()
	if err != nil || self == "" || info.Host == self {
		return ""
	}
	return fmt.Sprintf("　★ 持有者主机（%s）与本机（%s）不同 —— 锁文件可能位于共享存储，"+
		"该场景下文件锁语义不可靠，请改为各主机本地路径", info.Host, self)
}

// writeInfo 把本进程的诊断信息写入锁文件（覆盖旧内容）。
func writeInfo(f *os.File) error {
	host, _ := os.Hostname()
	content := strings.Join([]string{
		"PID=" + strconv.Itoa(os.Getpid()),
		"HOST=" + host,
		"START=" + time.Now().Format(time.RFC3339),
		"",
	}, "\n")
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(content), 0)
	return err
}

// readInfo 解析锁文件里的诊断信息。
//
// 容错优先：本函数**永不报错**，解析不出的部分留零值。锁文件可能是
// 被其他工具改写的、旧格式（仅一行 PID）或截断的，这些都不应影响
// 「告诉运维占用者是谁」这一唯一目的。
func readInfo(f *os.File) Info {
	var info Info
	buf := make([]byte, 512)
	n, err := f.ReadAt(buf, 0)
	if n <= 0 {
		_ = err
		return info
	}
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, found := strings.Cut(line, "=")
		if !found {
			// 旧格式：文件里只有一行裸 PID。
			if pid, cerr := strconv.Atoi(line); cerr == nil {
				info.PID = pid
			}
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(key)) {
		case "PID":
			if pid, cerr := strconv.Atoi(strings.TrimSpace(val)); cerr == nil {
				info.PID = pid
			}
		case "HOST":
			info.Host = strings.TrimSpace(val)
		case "START":
			info.Started = strings.TrimSpace(val)
		}
	}
	return info
}
