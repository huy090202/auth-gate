package console

import "net/http"

// NewWithPasswordCheck is New with the password comparison replaced, so a test
// can count how often sign-in runs it.
func NewWithPasswordCheck(d Deps, verify func(password, stored string) bool) http.Handler {
	a := newApp(d)
	a.verifyPassword = verify
	return a.handler()
}
