package worker

import "time"

// backoffSchedule 详情拉取失败后的指数退避序列（架构 §4.4：如 5s/30s/2m/10m/1h）。
var backoffSchedule = []time.Duration{
	5 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
	time.Hour,
}

// Backoff 返回第 attempt 次失败后的重试延迟（1-based）。
func Backoff(attempt int) time.Duration {
	if attempt <= 0 {
		return backoffSchedule[0]
	}
	idx := attempt - 1
	if idx >= len(backoffSchedule) {
		return backoffSchedule[len(backoffSchedule)-1]
	}
	return backoffSchedule[idx]
}
