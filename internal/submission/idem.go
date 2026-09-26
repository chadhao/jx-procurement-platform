package submission

// 幂等判定（Idempotency-Key）——「同键同载荷 → 复用首次结果；同键异载荷 → 40900」。
//
// 依据 docs/05-API.md §2.2（幂等头：服务端命中则返回首次结果）与 §8 幂等约定
// （报送登记：命中返回首次结果，409 仅在业务唯一键冲突时）+ §3.5 错误码「40900（幂等冲突）」。
//
// ★ 为什么必须比对载荷指纹而不是「命中即 40900」：
//   - 网络重试（超时后客户端重发）与客户端重复点击是**同一意图的同一载荷**，
//     此时返回首次结果才是正确的幂等语义；直接 40900 会把「重试」误判为「冲突」，
//     调用方无法区分「已成功」与「参数被改坏了」。
//   - 而同一个键被用于**不同载荷**（代码 bug / 键复用），才是真正的冲突，必须显式报错，
//     否则会静默丢弃第二次写入意图。
//
// 指纹只覆盖「决定这条报送记录身份与内容」的业务字段，不含请求时间 / trace 等易变项。

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// IdemItem 幂等载荷中的关联单据项。
type IdemItem struct {
	BizNo string
	Type  string
}

// IdemPayload 幂等判定用的规范化载荷。
//
// HasAmount 与 AmountCents 分离：区分「未传金额」与「传了 0」——两者是不同的登记意图，
// 若不加区分，`amount_cents` 省略与 `0` 会互相误判为同一载荷。
type IdemPayload struct {
	BizNo        string
	SubjectType  string
	AmountCents  int64
	HasAmount    bool
	PayMethod    string
	HNFinishDate string
	SubmitDate   string
	ReceiptRef   string
	SubmitState  string
	// ★ Q14-B 第 5 项：身份字段参与指纹 —— 同一 biz_no 换了归属部门 / 申请人 / 验收人，
	//   属**不同载荷**，不应被判为「同键同载荷」而幂等复用上一次的结果。
	Department      string
	ApplicantOpenID string
	AssignedOpenID  string
	Acceptors       []string
	Items           []IdemItem
}

// Fingerprint 计算规范化载荷的 SHA-256 指纹（十六进制小写）。
//
// 规范化规则：
//   - 全部字段 TrimSpace（避免前后空白造成「看似不同载荷」）；
//   - 关联单据项按「单号:类型」排序后再拼接（顺序不敏感，重复项保留计数）；
//   - ★ 分隔符统一用 0x1F（单元分隔符）——**项内部与项之间都是**。
//     不可用逗号之类可打印字符：若项内允许出现同一字符，
//     `[("A,B","C")]` 与 `[("A","B,C")]` 会拼成同一串而产生**指纹碰撞**
//     （碰撞后果是「不同载荷被判为同一载荷」→ 幂等误复用，静默吞掉第二次写入）。
func (p IdemPayload) Fingerprint() string {
	const unitSep = "\x1f"
	items := make([]string, 0, len(p.Items))
	for _, it := range p.Items {
		no := strings.TrimSpace(it.BizNo)
		if no == "" {
			continue // 与处理器一致：空单号的项不落库、也不参与指纹
		}
		items = append(items, no+unitSep+strings.TrimSpace(it.Type))
	}
	sort.Strings(items)

	amount := ""
	if p.HasAmount {
		amount = strconv.FormatInt(p.AmountCents, 10)
	}
	// 验收人集合：去空白、去空项、排序后再拼（顺序不敏感），与关联单据项同一处理。
	acc := make([]string, 0, len(p.Acceptors))
	for _, s := range p.Acceptors {
		if s = strings.TrimSpace(s); s != "" {
			acc = append(acc, s)
		}
	}
	sort.Strings(acc)

	parts := []string{
		strings.TrimSpace(p.BizNo),
		strings.TrimSpace(p.SubjectType),
		amount,
		strings.TrimSpace(p.PayMethod),
		strings.TrimSpace(p.HNFinishDate),
		strings.TrimSpace(p.SubmitDate),
		strings.TrimSpace(p.ReceiptRef),
		strings.TrimSpace(p.SubmitState),
		strings.TrimSpace(p.Department),
		strings.TrimSpace(p.ApplicantOpenID),
		strings.TrimSpace(p.AssignedOpenID),
		strings.Join(acc, unitSep),
		strings.Join(items, unitSep),
	}

	h := sha256.New()
	for i, s := range parts {
		if i > 0 {
			h.Write([]byte(unitSep))
		}
		h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))
}
