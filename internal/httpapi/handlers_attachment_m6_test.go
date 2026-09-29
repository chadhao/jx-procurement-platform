package httpapi

// M6 验收：附件上传暂存 → 提交绑定 → 字节可下载；owner 边界与不可绑定可见失败。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/access"
	"github.com/chadhao/jx-procurement-platform/internal/objectstore"
)

// memObjectStore 内存对象存储（测试假件）。
type memObjectStore struct{ m map[string][]byte }

func newMemObjectStore() *memObjectStore { return &memObjectStore{m: map[string][]byte{}} }

func (s *memObjectStore) Put(_ context.Context, key string, data []byte) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	s.m[key] = cp
	return nil
}
func (s *memObjectStore) Get(_ context.Context, key string) ([]byte, error) {
	b, ok := s.m[key]
	if !ok {
		return nil, objectstore.ErrNotFound
	}
	return b, nil
}
func (s *memObjectStore) Has(_ context.Context, key string) (bool, error) {
	_, ok := s.m[key]
	return ok, nil
}
func (s *memObjectStore) Kind() string { return "local" }

// uploadFile 发起 multipart 上传，返回解析后的 Envelope。
func uploadFile(t *testing.T, e *echo.Echo, cookie, fileName string, content []byte) (int, Envelope) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/approval/attachments", &buf)
	req.Header.Set(echo.HeaderContentType, w.FormDataContentType())
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: access.CookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	var env Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec.Code, env
}

func TestAttachmentUploadBindDownload(t *testing.T) {
	objects := newMemObjectStore()
	e, db, auth := newSubmitM4AppObj(t, true, nil, objects)
	cookie := auth.Establish("ou_app")

	// ① 上传暂存
	content := []byte("PDF-BYTES-样本附件")
	code, env := uploadFile(t, e, cookie, "../../恶意名.pdf", content)
	if code != http.StatusOK {
		t.Fatalf("上传失败 %d：%s", code, env.Message)
	}
	up, _ := env.Data.(map[string]any)
	fileID, _ := up["file_id"].(string)
	if !strings.HasPrefix(fileID, "stg_") {
		t.Fatalf("file_id = %q", fileID)
	}
	if got, _ := up["file_name"].(string); got != "恶意名.pdf" {
		t.Errorf("文件名未净化：%q", got)
	}

	// ② 提交并绑定（在**根对象**内追加 attachment_ids —— 不能拼进 fields）
	body := withAttachmentIDs(baSubmitBody, fileID)
	code2, env2 := postSubmit(t, e, cookie, body, "")
	if code2 != http.StatusOK {
		t.Fatalf("提交失败 %d：%s", code2, env2.Message)
	}
	d, _ := env2.Data.(map[string]any)
	bizNo, _ := d["biz_no"].(string)

	// ③ t_attachment 已登记 + 暂存行已回填
	var instCode, storageKey string
	if err := db.QueryRowContext(context.Background(),
		`SELECT instance_code, COALESCE(storage_key,'') FROM t_attachment WHERE file_id=?`,
		fileID).Scan(&instCode, &storageKey); err != nil {
		t.Fatalf("t_attachment 未登记: %v", err)
	}
	if storageKey == "" {
		t.Error("storage_key 为空（下载将回源飞书并失败）")
	}
	stg, err := db.GetStaging(context.Background(), fileID)
	if err != nil {
		t.Fatal(err)
	}
	if stg.BoundBizNo != bizNo {
		t.Errorf("bound_biz_no = %q，应为 %q", stg.BoundBizNo, bizNo)
	}

	// ④ 下载字节一致（行级可见：申请人是本人）
	req := httptest.NewRequest(http.MethodGet, "/api/attachment/"+fileID, nil)
	req.AddCookie(&http.Cookie{Name: access.CookieName, Value: cookie})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("下载失败 %d：%s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Errorf("下载字节不一致：%q", rec.Body.Bytes())
	}
}

func TestAttachmentUploadOwnerBoundary(t *testing.T) {
	objects := newMemObjectStore()
	e, _, auth := newSubmitM4AppObj(t, true, nil, objects)
	cookieApp := auth.Establish("ou_app")
	cookieOps := auth.Establish("ou_ops")

	code, env := uploadFile(t, e, cookieApp, "a.txt", []byte("x"))
	if code != http.StatusOK {
		t.Fatalf("上传失败 %d", code)
	}
	up, _ := env.Data.(map[string]any)
	fileID, _ := up["file_id"].(string)

	// 他人读取未绑定暂存件 ⇒ 404（不泄漏存在性）
	req := httptest.NewRequest(http.MethodGet, "/api/approval/attachments/"+fileID, nil)
	req.AddCookie(&http.Cookie{Name: access.CookieName, Value: cookieOps})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("他人读取应 404，实为 %d", rec.Code)
	}
}

func TestAttachmentBindForeignIDRejected(t *testing.T) {
	objects := newMemObjectStore()
	e, _, auth := newSubmitM4AppObj(t, true, nil, objects)
	cookie := auth.Establish("ou_app")

	// 他人（或不存在）的暂存 id ⇒ 提交整体失败 400（不静默丢附件）
	body := withAttachmentIDs(baSubmitBody, "stg_does_not_exist")
	code, env := postSubmit(t, e, cookie, body, "")
	if code != http.StatusBadRequest {
		t.Fatalf("不可绑定应 400，实为 %d（%s）", code, env.Message)
	}
}

// withAttachmentIDs 在 submit JSON 的**根对象**内追加 attachment_ids。
func withAttachmentIDs(body, fileID string) string {
	trimmed := strings.TrimSpace(body)
	trimmed = trimmed[:len(trimmed)-1] // 只剥根对象闭合 '}'
	return trimmed + fmt.Sprintf(`,"attachment_ids":[%q]}`, fileID)
}
