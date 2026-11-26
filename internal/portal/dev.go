package portal

import (
	"net/http"

	"github.com/huy090202/auth-gate/internal/platform/avatar"
)

// DevAvatarUploadPath is where the dev server receives avatar uploads in place of S3.
const DevAvatarUploadPath = "/dev/avatars-upload"

// DevRoutes wraps the Portal handler with the two routes only the dev server
// has: GET /avatars/<userId>/<file> serves the local avatar directory at the production path, and POST
// /dev/avatars-upload takes the multipart form the dev avatar store hands out.
// See avatar.DevRoutes. They sit outside New's wrapping because an upload is
// not JSON. main registers them only when PORTAL_DEV=1, so a deployed Portal
// has neither.
func DevRoutes(next http.Handler, root string, dir *avatar.Dir) http.Handler {
	mux := http.NewServeMux()
	avatar.DevRoutes(mux, root, dir, DevAvatarUploadPath)
	mux.Handle("/", next)
	return mux
}
