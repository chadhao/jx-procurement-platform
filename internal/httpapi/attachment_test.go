package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/inbox"
	"github.com/chadhao/jx-procurement-platform/internal/objectstore"
	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/seed"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
	jsync "github.com/chadhao/jx-procurement-platform/internal/sync"
	"github.com/chadhao/jx-procurement-platform/internal/worker"
)

// 本文件覆盖 B39 附件最小集（**只做下载**）：
//
//	① 元数据登记后可按实例列出；
//	② 下载走「主存命中 → 直接返回；未命中 → 回源拉取 → 落主存 → 回填」；
//	③ **行级权限以所属实例为准**（不是看附件自己的字段，也不是"人人可见"）；
//	④ 未登记的 file_id 一律 404（无法判定归属 → 不放行）。

// newAttachmentApp 构造带「飞书 FakeClient + 本地对象存储」的测试应用。
func newAttachmentApp(t *testing.T, dl func(context.Context, string) ([]byte, error)) (
	*echo.Echo, *store.DB, *access.Authenticator, *objectstore.LocalStore) {
	t.Helper()
	db := storetest.NewDB(t)
	if _, err := seed.SeedQ3Defaults(context.Background(), db); err != nil {
		t.Fatalf("播种默认权限口径失败: %v", err)
	}
	metrics := observ.NewMetrics()
	env := &config.Env{DevMode: true, InternalToken: testInternalToken, RunEnv: "test"}
	maps := &config.Maps{}
	client := &feishu.FakeClient{DownloadFn: dl}
	objs, err := objectstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("构造本地对象存储失败: %v", err)
	}
	inboxSvc := inbox.NewService(db, metrics, nil)
	ingestor := worker.NewIngestor(db, maps, nil)
	wk := worker.NewWorker(db, client, ingestor, metrics, nil)
	sub := jsync.NewSubscriber(db, client, maps, metrics, nil)
	rec := jsync.NewReconciler(db, client, maps, metrics, nil)
	perm := permission.NewLoader(db)
	sessions := access.NewStore("test-session-key", time.Hour)
	auth := access.NewAuthenticator(db, sessions, nil, true, nil)

	e := NewRouter(Deps{
		Env: env, DB: db, Log: observ.NewLogger("error", io.Discard),
		Metrics: metrics, Health: observ.NewHealth("test"),
		Inbox: inboxSvc, Worker: wk, Subscriber: sub, Reconciler: rec,
		Perm: perm, Auth: auth, Maps: maps, WebUI: nil, Version: "test",
		Feishu: client, Objects: objs,
	})
	return e, db, auth, objs
}

// doDownload 发起下载并取回**原始字节**（附件是二进制流，不能按 JSON 信封解析）。
func doDownload(e *echo.Echo, path, cookie string) (*httptest.ResponseRecorder, []byte) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: access.CookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec, rec.Body.Bytes()
}

func seedAttachmentInstance(t *testing.T, db *store.DB, code, dept, applicant string) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.UpsertInstance(context.Background(), &store.Instance{
		InstanceCode: code, ApprovalCode: "CODE-GR", DocType: "GR", BizNo: "GR-2609-0001",
		Status: "APPROVED", StatusRaw: "APPROVED",
		Department: dept, ApplicantOpenID: applicant, Source: "event",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写实例失败: %v", err)
	}
}

// TestAttachmentDownloadAndCache 下载、落主存、二次命中不再回源。
func TestAttachmentDownloadAndCache(t *testing.T) {
	calls := 0
	e, db, auth, objs := newAttachmentApp(t, func(_ context.Context, fileID string) ([]byte, error) {
		calls++
		return []byte("PDF-BYTES-" + fileID), nil
	})
	seedAttachmentInstance(t, db, "I-ATT-1", "生产部", "ou_a")
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true})

	if err := db.UpsertAttachment(context.Background(), &store.Attachment{
		FileID: "fld_aaa", InstanceCode: "I-ATT-1", BizNo: "GR-2609-0001", FileName: "验收单.pdf",
	}); err != nil {
		t.Fatalf("登记附件失败: %v", err)
	}

	// ① 列表（元数据，不拉文件 → 不应产生飞书调用）
	rec, env := doRequest(e, http.MethodGet, "/api/instances/I-ATT-1/attachments", auth.Establish("ou_gm"), "")
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("列出附件: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	if got := mustData(t, env)["total"].(float64); got != 1 {
		t.Errorf("附件条数 = %v, 期望 1", got)
	}
	if calls != 0 {
		t.Errorf("列出元数据不应触发下载，实际调用 %d 次", calls)
	}

	// ② 首次下载 → 回源 1 次 + 落主存
	rec, body := doDownload(e, "/api/attachment/fld_aaa", auth.Establish("ou_gm"))
	if rec.Code != http.StatusOK {
		t.Fatalf("下载附件: http=%d body=%s", rec.Code, rec.Body.String())
	}
	if string(body) != "PDF-BYTES-fld_aaa" {
		t.Errorf("下载内容 = %q", string(body))
	}
	if calls != 1 {
		t.Fatalf("首次下载应回源 1 次，实际 %d", calls)
	}
	if ok, _ := objs.Has(context.Background(), "fld_aaa"); !ok {
		t.Error("附件未落主存")
	}
	// 元数据应已回填 fetched
	meta, err := db.GetAttachmentByFileID(context.Background(), "fld_aaa")
	if err != nil {
		t.Fatalf("回读附件失败: %v", err)
	}
	if meta.StorageKey == "" || meta.FetchedAt == nil {
		t.Errorf("存储信息未回填: key=%q fetched=%v", meta.StorageKey, meta.FetchedAt)
	}

	// ③ 二次下载 → **不再回源**（命中主存）
	rec, body = doDownload(e, "/api/attachment/fld_aaa", auth.Establish("ou_gm"))
	if rec.Code != http.StatusOK || string(body) != "PDF-BYTES-fld_aaa" {
		t.Fatalf("二次下载异常: http=%d body=%q", rec.Code, string(body))
	}
	if calls != 1 {
		t.Errorf("二次下载不应回源，实际累计 %d 次（缓存未生效 → 会白白消耗 API 配额）", calls)
	}
}

// TestAttachmentRowScopeByInstance 行级：附件可见性以**所属实例**为准，不得人人可见。
func TestAttachmentRowScopeByInstance(t *testing.T) {
	e, db, auth, _ := newAttachmentApp(t, func(_ context.Context, fileID string) ([]byte, error) {
		return []byte("x"), nil
	})
	seedAttachmentInstance(t, db, "I-ATT-PROD", "生产部", "ou_a")
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_lead_prod", Role: roleDeptLead, Department: "生产部", Active: true})
	if err := db.UpsertAttachment(context.Background(), &store.Attachment{
		FileID: "fld_prod", InstanceCode: "I-ATT-PROD", FileName: "生产部附件.pdf",
	}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}

	// 本部门主管可见
	rec, _ := doDownload(e, "/api/attachment/fld_prod", auth.Establish("ou_lead_prod"))
	if rec.Code != http.StatusOK {
		t.Errorf("本部门主管应可下载: http=%d", rec.Code)
	}
	// 他部门主管不可见（DEPT 令牌 → 不在其部门范围）
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_lead_sale", Role: roleDeptLead, Department: "销售部", Active: true})
	rec, _ = doDownload(e, "/api/attachment/fld_prod", auth.Establish("ou_lead_sale"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("他部门主管下载应被拒: http=%d, 期望 403", rec.Code)
	}
	// 未登记的 file_id → 404（无法判定归属 → 不放行）
	rec, _ = doDownload(e, "/api/attachment/fld_not_registered", auth.Establish("ou_lead_prod"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("未登记附件应 404: http=%d", rec.Code)
	}
}

// TestAttachmentDegradesWithoutStore 未配置对象存储时**不缓存、直接转发**（降级可用，不静默丢功能）。
func TestAttachmentDegradesWithoutStore(t *testing.T) {
	e, db, auth, _ := newAttachmentApp(t, func(_ context.Context, fileID string) ([]byte, error) {
		return []byte("RAW"), nil
	})
	seedAttachmentInstance(t, db, "I-ATT-2", "生产部", "ou_a")
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true})
	if err := db.UpsertAttachment(context.Background(), &store.Attachment{
		FileID: "fld_nostore", InstanceCode: "I-ATT-2",
	}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	rec, body := doDownload(e, "/api/attachment/fld_nostore", auth.Establish("ou_gm"))
	if rec.Code != http.StatusOK || string(body) != "RAW" {
		t.Fatalf("无存储时应直接转发: http=%d body=%q", rec.Code, string(body))
	}
}

// TestSubmissionPackageIncludesAttachments 凭证包必须含**附件清单**（B39 缺口②）。
//
// ★ 为什么必须测：凭证包是「报送集团」的交付物。若只含单据清单、不含附件引用，
// 集团收到的是**没有文件**的空包 —— 这条链在事实上不成立，而接口仍返回 200。
func TestSubmissionPackageIncludesAttachments(t *testing.T) {
	e, db, auth, _ := newAttachmentApp(t, func(_ context.Context, fileID string) ([]byte, error) {
		return []byte("x"), nil
	})
	seedDefaultUsers(t, db,
		store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true},
		store.UserRole{OpenID: "ou_gm", Role: roleProjectGM, Active: true},
	)
	// 实例 + 附件（biz_no 与报送的关联单据一致）
	seedAttachmentInstance(t, db, "I-PKG-1", "生产部", "ou_a")
	if err := db.UpsertAttachment(context.Background(), &store.Attachment{
		FileID: "fld_pkg", InstanceCode: "I-PKG-1", BizNo: "CT-2609-7777", FileName: "合同扫描件.pdf",
	}); err != nil {
		t.Fatalf("登记附件失败: %v", err)
	}

	cookie := auth.Establish("ou_ops")
	// 建报送（关联单据 = 上面那个合同号）
	rec, env := doRequest(e, http.MethodPost, "/api/submission", cookie,
		`{"biz_no":"SUB-PKG-1","subject_type":"公户付款","pay_method":"公户转账",
		  "items":[{"item_biz_no":"CT-2609-7777","item_type":"CT"}]}`)
	if rec.Code != http.StatusOK || env.Code != codeOK {
		t.Fatalf("建报送失败: http=%d code=%d body=%s", rec.Code, env.Code, rec.Body.String())
	}
	id := int64(mustData(t, env)["id"].(float64))

	// 取凭证包（zip）并读回内容
	pkgRec, body := doDownload(e, "/api/submission/"+itoa(id)+"/package?format=zip", cookie)
	if pkgRec.Code != http.StatusOK {
		t.Fatalf("取凭证包失败: http=%d body=%s", pkgRec.Code, string(body))
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("凭证包不是合法 zip: %v", err)
	}
	var attachCSV string
	found := false
	for _, f := range zr.File {
		if f.Name != "附件清单.csv" {
			continue
		}
		found = true
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("打开附件清单失败: %v", err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		attachCSV = string(b)
	}
	if !found {
		t.Fatalf("凭证包缺少「附件清单.csv」——集团会收到只有清单没有文件的空包")
	}
	if !strings.Contains(attachCSV, "fld_pkg") || !strings.Contains(attachCSV, "合同扫描件.pdf") {
		t.Errorf("附件清单未含预期附件:\n%s", attachCSV)
	}
	if !strings.Contains(attachCSV, "否") {
		t.Errorf("附件清单应标注未入库状态（尚未拉取）:\n%s", attachCSV)
	}
}

// itoa 小工具（避免为本文件再引 strconv 造成无用导入告警）。
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
