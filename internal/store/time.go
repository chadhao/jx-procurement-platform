package store

import "time"

// 时间统一以 RFC3339（UTC）字符串落库；账期用 YYYY-MM。
const timeLayout = time.RFC3339

// timeNow 可被测试替换（默认 time.Now）。
var timeNow = func() time.Time { return time.Now() }

// fmtTime 将时间格式化为 UTC RFC3339 字符串。
func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// parseTime 解析 RFC3339 字符串为 time.Time（UTC）；失败返回零值时间。
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// parseTimePtr 解析可空时间字符串为 *time.Time。
func parseTimePtr(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}
