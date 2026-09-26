package objectstore

import "log/slog"

// Build 按装配顺序构造对象存储（架构 §7.5 / ADR-08）：
//
//	① 主存：`primary` 非空 → **S3**；否则退回 `localDir`（**本地落盘**）；
//	② 备份：`backup` 非空 → 与其组成 **MirrorStore**（主备双写）；
//	③ 都为空 → 返回 **nil**，调用方据此**降级为"不缓存、每次回源"**（不是静默丢功能）。
//
// ★ 为什么把装配收在一个函数里：避免"三处各写一遍装配逻辑"，从而出现
// 「配了 S3 却仍走本地」这类**假配置**（本项目已发生多次的缺陷形态）。
func Build(primary, backup *S3Config, localDir string, log *slog.Logger) (Store, error) {
	var main Store
	if primary != nil {
		s, err := NewS3(*primary)
		if err != nil {
			return nil, err
		}
		main = s
	} else {
		l, err := NewLocal(localDir)
		if err != nil {
			return nil, err
		}
		main = l // 可能为 nil（未配置本地目录）→ 由 NewMirror 处理
	}

	var bk Store
	if backup != nil {
		s, err := NewS3(*backup)
		if err != nil {
			return nil, err
		}
		bk = s
	}
	return NewMirror(main, bk, log), nil
}
