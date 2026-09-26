// Package observ 提供结构化日志、关键指标埋点与健康检查聚合。
// 对应架构 §8：日志字段含 feishu_log_id，指标覆盖事件入库/幂等命中/对账补录/飞书调用量。
package observ

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// NewLogger 构造 JSON 结构化日志器。
// level：debug/info/warn/error；out 为 nil 时写入 stdout。
func NewLogger(level string, out io.Writer) *slog.Logger {
	if out == nil {
		out = os.Stdout
	}
	opts := &slog.HandlerOptions{Level: parseLevel(level)}
	return slog.New(slog.NewJSONHandler(out, opts))
}

// WithComponent 给日志器附加 component 字段（如 inbox / worker / feishu）。
func WithComponent(l *slog.Logger, component string) *slog.Logger {
	return l.With(slog.String("component", component))
}

// WithFeishuLogID 透传飞书调用返回的 log_id，用于与飞书侧联合排障（架构 §8.1）。
func WithFeishuLogID(l *slog.Logger, logID string) *slog.Logger {
	return l.With(slog.String("feishu_log_id", logID))
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
