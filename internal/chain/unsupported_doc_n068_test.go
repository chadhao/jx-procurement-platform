package chain

// N-068 ③：`ErrUnsupportedDoc` 文案据实（不写批次号 —— 批次号会过期、把排查引向错误结论）。
// ★ 先红后绿：改文案前本用例必红（旧文案含「批 1 仅 BA/PR/SA」）。

import (
	"strings"
	"testing"
)

func TestErrUnsupportedDocMessageN068(t *testing.T) {
	msg := ErrUnsupportedDoc.Error()
	if strings.Contains(msg, "批 1") || strings.Contains(msg, "批 2") || strings.Contains(msg, "仅 BA/PR/SA") {
		t.Errorf("文案不得含过时批次号/清单（会随版本过期）, 实为: %s", msg)
	}
	if !strings.Contains(msg, "POST /api/submission") {
		t.Errorf("SUB 是最常见的命中场景，文案须指路独立提交通道, 实为: %s", msg)
	}
	// 变量引用形态不得破坏（errors.Is 家族语义）。
	if !strings.Contains(msg, "chain:") {
		t.Errorf("错误前缀应保持 chain: 家族形态, 实为: %s", msg)
	}
}
