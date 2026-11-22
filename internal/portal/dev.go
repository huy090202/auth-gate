package portal

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/huy090202/auth-gate/internal/platform/webapi"
	"github.com/huy090202/auth-gate/internal/portal/avatar"
)

// DevAvatarUploadPath is where the dev server receives avatar uploads in place of S3.
const DevAvatarUploadPath = "/dev/avatars-upload"

// noListing refuses to list a directory, as the bucket would.
type noListing struct{ fs http.FileSystem }

func (n noListing) Open(name string) (http.File, error) {
	f, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err != nil || st.IsDir() {
		_ = f.Close()
		return nil, os.ErrNotExist
	}
	return f, nil
}

// DevRoutes wraps the Portal handler with the two routes only the dev server
// has: GET /avatars/<userId>/<file> serves the local avatar directory at the production path, and POST
// /dev/avatars-upload takes the multipart form the dev avatar store hands out
// (the key field, then the file) and saves it with dir.Save. They sit outside
// New's wrapping because an upload is not JSON. main registers them only when
// PORTAL_DEV=1, so a deployed Portal has neither.
func DevRoutes(next http.Handler, root string, dir *avatar.Dir) http.Handler {
	mux := http.NewServeMux()
	// Confirmed keys are avatars/<userId>/<ulid>.<ext>, and production serves
	// "/" + key through CloudFront's /avatars/* behaviour, so the root is served
	// as is; uploads waiting under pending/ are not served, as in production.
	mux.Handle("GET /avatars/", http.FileServer(noListing{http.Dir(root)}))
	mux.HandleFunc("POST "+DevAvatarUploadPath, func(w http.ResponseWriter, r *http.Request) {
		if err := saveUpload(w, r, dir); err != nil {
			var ae *webapi.Error
			if !errors.As(err, &ae) {
				slog.Warn("portal dev: avatar upload refused", "err", err)
				err = &webapi.Error{Status: http.StatusBadRequest, Code: "invalid_upload"}
			}
			webapi.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/", next)
	return mux
}

func saveUpload(w http.ResponseWriter, r *http.Request, dir *avatar.Dir) error {
	r.Body = http.MaxBytesReader(w, r.Body, avatar.MaxBytes+64<<10)
	mr, err := r.MultipartReader()
	if err != nil {
		return err
	}
	var key string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return errors.New("no file part")
		}
		if err != nil {
			return err
		}
		switch p.FormName() {
		case "key":
			b, err := io.ReadAll(io.LimitReader(p, 512))
			if err != nil {
				return err
			}
			key = string(b)
		case "file":
			if key == "" {
				return errors.New("file before key")
			}
			return dir.Save(r.Context(), key, p)
		}
	}
}
