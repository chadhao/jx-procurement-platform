package objectstore

import (
	"context"
	"errors"
	"testing"
)

// fakeStore 内存实现（供主备双写用例；可注入 Put/Get/Has 的错误）。
type fakeStore struct {
	kind    string
	data    map[string][]byte
	putErr  error
	getErr  error
	putCall int
}

func newFake(kind string) *fakeStore { return &fakeStore{kind: kind, data: map[string][]byte{}} }

func (f *fakeStore) Kind() string { return f.kind }

func (f *fakeStore) Put(_ context.Context, key string, data []byte) error {
	f.putCall++
	if f.putErr != nil {
		return f.putErr
	}
	f.data[key] = append([]byte(nil), data...)
	return nil
}

func (f *fakeStore) Get(_ context.Context, key string) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	b, ok := f.data[key]
	if !ok {
		return nil, ErrNotFound
	}
	return b, nil
}

func (f *fakeStore) Has(_ context.Context, key string) (bool, error) {
	if _, ok := f.data[key]; !ok {
		return false, f.getErr
	}
	return true, nil
}

// TestMirrorDualWrite Put 必须**双写**（主 + 备）。
func TestMirrorDualWrite(t *testing.T) {
	primary, backup := newFake("s3"), newFake("rustfs")
	m := NewMirror(primary, backup, nil)

	if err := m.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("PUT 失败: %v", err)
	}
	if string(primary.data["k"]) != "v" {
		t.Error("主存未写入")
	}
	if string(backup.data["k"]) != "v" {
		t.Error("备份未写入（异地备份形同虚设）")
	}
	if m.Kind() != "s3+rustfs" {
		t.Errorf("Kind = %q, 期望 s3+rustfs（便于启动日志确认双写生效）", m.Kind())
	}
}

// TestMirrorBackupFailureDoesNotFailPut 备份写失败**不使整体失败**，但主存必须仍成功。
func TestMirrorBackupFailureDoesNotFailPut(t *testing.T) {
	primary, backup := newFake("s3"), newFake("rustfs")
	backup.putErr = errors.New("备份不可达")
	m := NewMirror(primary, backup, nil)

	if err := m.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("备份失败不应让 Put 失败，实际: %v", err)
	}
	if string(primary.data["k"]) != "v" {
		t.Error("主存未写入")
	}
}

// TestMirrorPrimaryFailureFailsPut 主存写失败**必须**失败（主存是权威副本）。
func TestMirrorPrimaryFailureFailsPut(t *testing.T) {
	primary, backup := newFake("s3"), newFake("rustfs")
	primary.putErr = errors.New("主存不可达")
	m := NewMirror(primary, backup, nil)

	if err := m.Put(context.Background(), "k", []byte("v")); err == nil {
		t.Fatal("主存写失败必须报错（否则数据只落在备份上，语义错位且无人察觉）")
	}
}

// TestMirrorGetFallbackToBackup 主存 404 或不可用时**回退备份**（异地备份的价值所在）。
func TestMirrorGetFallbackToBackup(t *testing.T) {
	ctx := context.Background()

	t.Run("主存 404 → 回退备份", func(t *testing.T) {
		primary, backup := newFake("s3"), newFake("rustfs")
		backup.data["k"] = []byte("from-backup")
		m := NewMirror(primary, backup, nil)
		b, err := m.Get(ctx, "k")
		if err != nil {
			t.Fatalf("应回退备份: %v", err)
		}
		if string(b) != "from-backup" {
			t.Errorf("内容 = %q", string(b))
		}
	})

	t.Run("主存故障 → 回退备份", func(t *testing.T) {
		primary, backup := newFake("s3"), newFake("rustfs")
		primary.getErr = errors.New("主存 500")
		backup.data["k"] = []byte("from-backup")
		m := NewMirror(primary, backup, nil)
		b, err := m.Get(ctx, "k")
		if err != nil {
			t.Fatalf("主存故障时应回退备份: %v", err)
		}
		if string(b) != "from-backup" {
			t.Errorf("内容 = %q", string(b))
		}
	})

	t.Run("两侧都没有 → ErrNotFound", func(t *testing.T) {
		m := NewMirror(newFake("s3"), newFake("rustfs"), nil)
		if _, err := m.Get(ctx, "k"); !errors.Is(err, ErrNotFound) {
			t.Errorf("两侧皆无应返回 ErrNotFound，实际 %v", err)
		}
	})
}

// TestMirrorHasFallback Has 的降级次序与 Get 一致。
func TestMirrorHasFallback(t *testing.T) {
	primary, backup := newFake("s3"), newFake("rustfs")
	backup.data["k"] = []byte("v")
	m := NewMirror(primary, backup, nil)
	ok, err := m.Has(context.Background(), "k")
	if err != nil || !ok {
		t.Fatalf("主存未命中时应看备份: (%v, %v)", ok, err)
	}
}

// TestNewMirrorDegrade 装配语义：无备份直接用主存；无主存返回 nil（调用方据此降级为直通）。
func TestNewMirrorDegrade(t *testing.T) {
	primary := newFake("s3")
	if got := NewMirror(primary, nil, nil); got != Store(primary) {
		t.Error("无备份时应直接返回主存（不做无意义包装）")
	}
	if got := NewMirror(nil, primary, nil); got != nil {
		t.Error("无主存时应返回 nil（＝降级为不缓存、每次回源）")
	}
	if got := NewMirror(nil, nil, nil); got != nil {
		t.Error("两侧皆无应返回 nil")
	}
}
