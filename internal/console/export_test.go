package console

import (
	"net/http"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

// NewWithPasswordCheck is New with the password comparison replaced, so a test
// can count how often sign-in runs it.
func NewWithPasswordCheck(d Deps, verify func(password, stored string) bool) http.Handler {
	a := newApp(d)
	a.verifyPassword = verify
	return a.handler()
}

// NewWithBeforeAdminWrite is New with a hook that runs after an /admins action
// has spent its token and just before it writes, so a test can change the world
// in that gap.
func NewWithBeforeAdminWrite(d Deps, hook func()) http.Handler {
	a := newApp(d)
	a.beforeAdminWrite = hook
	return a.handler()
}

// AdminWriteError exposes how a failed /admins write is answered.
func AdminWriteError(err error) (status int, code string, retryAfter int) {
	ae := adminWriteError(err).(*webapi.Error)
	return ae.Status, ae.Code, ae.RetryAfter
}

// FirstFactorSendWindow is how long after the lease claim the first-factor write may be sent.
const FirstFactorSendWindow time.Duration = firstFactorSendWindow
