package singlelock

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestAcquireReleaseReacquire 覆盖基本生命周期：获取 → 释放 → 再次获取成功。
func TestAcquireReleaseReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jx.lock")

	l1 := New(path)
	if err := l1.Acquire(); err != nil {
		t.Fatalf("首次获取锁应成功，实际失败: %v", err)
	}
	if err := l1.Release(); err != nil {
		t.Fatalf("释放锁应成功，实际失败: %v", err)
	}

	l2 := New(path)
	if err := l2.Acquire(); err != nil {
		t.Fatalf("释放后再次获取锁应成功，实际失败: %v", err)
	}
	defer func() { _ = l2.Release() }()
}

// TestSecondAcquireFailsAndReportsHolder 是本包的核心契约：
// 第二个实例必须**失败**，且错误信息里要能看出**占用者是谁**（B43 处置点）。
func TestSecondAcquireFailsAndReportsHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jx.lock")

	holder := New(path)
	if err := holder.Acquire(); err != nil {
		t.Fatalf("持有者获取锁应成功，实际失败: %v", err)
	}
	defer func() { _ = holder.Release() }()

	second := New(path)
	err := second.Acquire()
	if err == nil {
		_ = second.Release()
		t.Fatal("第二个实例必须获取锁失败（单实例硬约束），实际却成功了")
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("失败原因必须是 ErrLocked（供调用方以非 0 退出码拒绝启动），实际: %v", err)
	}

	msg := err.Error()
	// 诊断信息必须落到错误串里 —— 否则运维只能看到"锁被占用"，无从下手。
	if !strings.Contains(msg, "PID="+strconv.Itoa(os.Getpid())) {
		t.Errorf("错误信息应包含持有者 PID（%d），实际: %s", os.Getpid(), msg)
	}
	if host, herr := os.Hostname(); herr == nil && host != "" {
		if !strings.Contains(msg, host) {
			t.Errorf("错误信息应包含持有者主机名（%s），实际: %s", host, msg)
		}
	}
	if !strings.Contains(msg, path) {
		t.Errorf("错误信息应包含锁文件路径，实际: %s", msg)
	}
}

// TestAcquireWritesDiagnostics 校验持锁后锁文件里**确实**写入了诊断信息。
//
// ★ 这条用例的存在理由：诊断信息是"没有消费者就不该写"的反例 ——
// 它唯一的消费者就是本用例与运维人员的眼睛；若写入被误删，上面的
// TestSecondAcquireFailsAndReportsHolder 只会看到"PID=未知"，
// 断言仍会失败，故两例互相兜底。
func TestAcquireWritesDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jx.lock")

	l := New(path)
	if err := l.Acquire(); err != nil {
		t.Fatalf("获取锁应成功，实际失败: %v", err)
	}
	defer func() { _ = l.Release() }()

	// ★ 用**持锁句柄自身**读取，而不是另开一个句柄：Windows 的字节范围锁是
	// **强制锁**，另一句柄读取被锁区间会直接失败。持锁者读自己的区间是允许的。
	info := readInfo(l.file)

	if info.PID != os.Getpid() {
		t.Errorf("锁文件 PID = %d，期望 %d", info.PID, os.Getpid())
	}
	if info.Host == "" {
		t.Error("锁文件应写入主机名（用于识别共享存储上的异地锁文件）")
	}
	if info.Started == "" {
		t.Error("锁文件应写入获取时刻（START）")
	}
}

// TestReadInfoTolerant 覆盖三种"不完美"的锁文件内容，均**不得**导致解析失败。
func TestReadInfoTolerant(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantPID  int
		wantHost string
	}{
		{"旧格式：仅一行裸 PID", "12345\n", 12345, ""},
		{"当前格式", "PID=999\nHOST=node-a\nSTART=2026-09-27T10:00:00Z\n", 999, "node-a"},
		{"空文件", "", 0, ""},
		{"乱码/被改写", "hello world\n", 0, ""},
		{"PID 非数字", "PID=abc\nHOST=x\n", 0, "x"},
		{"小写键名", "pid=77\nhost=node-b\n", 77, "node-b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jx.lock")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("写测试锁文件失败: %v", err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("打开测试锁文件失败: %v", err)
			}
			defer func() { _ = f.Close() }()

			got := readInfo(f)
			if got.PID != tc.wantPID {
				t.Errorf("PID = %d，期望 %d", got.PID, tc.wantPID)
			}
			if got.Host != tc.wantHost {
				t.Errorf("Host = %q，期望 %q", got.Host, tc.wantHost)
			}
		})
	}
}

// TestInfoString 校验诊断串的渲染（含缺失字段的降级渲染）。
func TestInfoString(t *testing.T) {
	full := Info{PID: 4242, Host: "node-a", Started: "2026-09-27T10:00:00Z"}
	s := full.String()
	for _, want := range []string{"4242", "node-a", "2026-09-27T10:00:00Z"} {
		if !strings.Contains(s, want) {
			t.Errorf("完整 Info 的渲染应含 %q，实际: %s", want, s)
		}
	}

	empty := Info{}
	es := empty.String()
	if !strings.Contains(es, "未知") {
		t.Errorf("空 Info 的渲染应以「未知」降级，实际: %s", es)
	}
	if strings.Contains(es, "启动于") {
		t.Errorf("空 Info 不应渲染 START 段，实际: %s", es)
	}
}

// TestForeignHostHint 覆盖「锁文件来自另一台主机」的部署异常提示。
//
// ★ 这是本包唯一真正的"僵尸锁"形态：本机文件系统上不可能出现他人的锁记录，
// 一旦出现即说明锁文件位于共享 / 网络存储，而 flock 在那里语义不可靠。
func TestForeignHostHint(t *testing.T) {
	self, err := os.Hostname()
	if err != nil || self == "" {
		t.Skip("本机主机名不可得，跳过")
	}

	if hint := foreignHostHint(Info{Host: self}); hint != "" {
		t.Errorf("同主机不应给出提示，实际: %s", hint)
	}
	if hint := foreignHostHint(Info{Host: ""}); hint != "" {
		t.Errorf("主机未知时不应臆断，实际: %s", hint)
	}
	hint := foreignHostHint(Info{Host: self + "-other"})
	if hint == "" {
		t.Fatal("异主机锁文件必须给出部署异常提示（共享存储 + flock 不可靠）")
	}
	if !strings.Contains(hint, self) {
		t.Errorf("提示应说明本机主机名，实际: %s", hint)
	}
}

// TestAcquireEmptyPath 覆盖非法入参：必须报错，不得静默成功。
func TestAcquireEmptyPath(t *testing.T) {
	l := New("")
	if err := l.Acquire(); err == nil {
		t.Fatal("空锁路径必须报错，不得静默成功")
	} else if errors.Is(err, ErrLocked) {
		t.Errorf("空路径是配置错误，不应冒充为 ErrLocked（会误导成「已有实例在跑」）: %v", err)
	}
}

// TestReleaseIdempotent 释放必须幂等且未持锁时安全。
func TestReleaseIdempotent(t *testing.T) {
	l := New(filepath.Join(t.TempDir(), "jx.lock"))
	if err := l.Release(); err != nil {
		t.Fatalf("未持锁时释放应安全返回 nil，实际: %v", err)
	}
	if err := l.Acquire(); err != nil {
		t.Fatalf("获取锁应成功，实际失败: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("首次释放应成功，实际: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("重复释放应安全返回 nil，实际: %v", err)
	}
}

// TestPathAccessor 覆盖 Path()（错误信息与日志要用）。
func TestPathAccessor(t *testing.T) {
	p := filepath.Join(t.TempDir(), "jx.lock")
	if got := New(p).Path(); got != p {
		t.Errorf("Path() = %q，期望 %q", got, p)
	}
}
