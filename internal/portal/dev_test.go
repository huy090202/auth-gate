package portal

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/huy090202/auth-gate/internal/portal/avatar"
)

var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")

func upload(t *testing.T, h http.Handler, key string, file []byte) *httptest.ResponseRecorder {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	_ = mw.WriteField("key", key)
	fw, _ := mw.CreateFormFile("file", "a.png")
	_, _ = fw.Write(file)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, DevAvatarUploadPath, &b)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Why: the dev server must take an upload through the same form the S3 path uses and serve it back, while refusing what the store refuses.
func TestDevAvatarUploadAndServe(t *testing.T) {
	root := t.TempDir()
	dir, err := avatar.NewDir(root, "http://x"+DevAvatarUploadPath)
	if err != nil {
		t.Fatal(err)
	}
	h := DevRoutes(New(Deps{Config: Config{Table: "t"}}), root, dir)
	if rec := upload(t, h, "avatars/u1/a.png", pngHeader); rec.Code != http.StatusNoContent {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(root, "avatars/u1/a.png")); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/avatars/avatars/u1/a.png", nil))
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), pngHeader) {
		t.Fatalf("serve: %d", rec.Code)
	}
	if rec := upload(t, h, "../escape.png", pngHeader); rec.Code != 400 {
		t.Fatalf("path escape: %d", rec.Code)
	}
	if rec := upload(t, h, "avatars/u1/b.png", []byte("<html>")); rec.Code != 400 {
		t.Fatalf("wrong type: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nope", nil))
	if rec.Code != 404 {
		t.Fatalf("api through dev wrapper: %d", rec.Code)
	}
}
