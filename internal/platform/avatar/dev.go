package avatar

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

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

// DevRoutes registers the two routes a dev server needs in place of S3 and
// CloudFront: GET /avatars/ serves root at the production path (confirmed keys
// are avatars/<userId>/<ulid>.<ext>; uploads waiting under pending/ are not
// served, as in production), and POST uploadPath takes the multipart form
// Dir hands out (the key field, then the file) and saves it with dir.Save.
// A deployed app must not call it.
func DevRoutes(mux *http.ServeMux, root string, dir *Dir, uploadPath string) {
	mux.Handle("GET /avatars/", http.FileServer(noListing{http.Dir(root)}))
	mux.HandleFunc("POST "+uploadPath, func(w http.ResponseWriter, r *http.Request) {
		if err := saveUpload(w, r, dir); err != nil {
			var ae *webapi.Error
			if !errors.As(err, &ae) {
				slog.Warn("avatar dev: upload refused", "err", err)
				err = &webapi.Error{Status: http.StatusBadRequest, Code: "invalid_upload"}
			}
			webapi.WriteErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func saveUpload(w http.ResponseWriter, r *http.Request, dir *Dir) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBytes+64<<10)
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
