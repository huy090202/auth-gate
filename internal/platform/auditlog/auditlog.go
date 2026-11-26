// Package auditlog is the request-side half of the audit log shared by the
// Console and the Portal: a note a request fills in while it runs, and a
// wrapper that holds the response back until the app has stored the entry. Item
// keys and attributes stay in each app.
package auditlog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// MaxTarget cuts a target, a detail value or a result.
const MaxTarget = 200

// Note is what a request tells the audit log about itself while it runs: who
// the session is (set by the session middleware), which scope it acted in
// (the Portal's workspace), what the action touched and a few facts about it.
// It never holds a secret; the request body and the response are not read for
// it, and each detail is set by the handler from a field it decoded.
type Note struct {
	ActorID, ActorName, Scope, Target string
	Detail                            map[string]string
	result                            string
}

// Entry is the finished record of one request, handed to the app to store.
type Entry struct {
	Action, Result string
	Note           Note
}

type noteKey struct{}

// NoteFrom returns the request's note, or nil outside Wrap.
func NoteFrom(ctx context.Context) *Note {
	n, _ := ctx.Value(noteKey{}).(*Note)
	return n
}

// SetActor records who the session is.
func SetActor(ctx context.Context, id, name string) {
	if n := NoteFrom(ctx); n != nil {
		n.ActorID, n.ActorName = id, name
	}
}

// SetScope names the scope whose log gets the entry.
func SetScope(ctx context.Context, scope string) {
	if n := NoteFrom(ctx); n != nil {
		n.Scope = scope
	}
}

// SetTarget names what the action touches.
func SetTarget(ctx context.Context, target string) {
	if n := NoteFrom(ctx); n != nil {
		n.Target = target
	}
}

// SetResult overrides the result Wrap derives from the status, for an action
// whose outcome the status cannot tell, such as "partial".
func SetResult(ctx context.Context, result string) {
	if n := NoteFrom(ctx); n != nil {
		n.result = result
	}
}

// SetDetail records one fact about the action. Keys are fixed names in the
// handler; the value comes from a decoded field, never from a raw body, an
// upstream error or a credential.
func SetDetail(ctx context.Context, k, v string) {
	if n := NoteFrom(ctx); n != nil {
		if n.Detail == nil {
			n.Detail = map[string]string{}
		}
		n.Detail[k] = v
	}
}

// bufWriter holds a response back until the audit entry is written, so a client
// that has its answer can read the log.
type bufWriter struct {
	h      http.Header
	status int
	body   bytes.Buffer
}

func (b *bufWriter) Header() http.Header { return b.h }
func (b *bufWriter) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}
func (b *bufWriter) Write(p []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	return b.body.Write(p)
}

// Wrap runs h with a fresh note, then calls finish with the outcome before the
// response goes out: Result is "success", or "failed:<code>" with the error code
// the handler answered, unless it set its own with SetResult. The note's target is already cut to MaxTarget. finish
// decides where the entry goes and never changes the answer.
func Wrap(action string, h http.Handler, finish func(r *http.Request, e Entry)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		note := &Note{}
		bw := &bufWriter{h: w.Header()}
		h.ServeHTTP(bw, r.WithContext(context.WithValue(r.Context(), noteKey{}, note)))
		if bw.status == 0 {
			bw.status = http.StatusOK
		}
		result := "success"
		if bw.status >= 400 {
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(bw.body.Bytes(), &e)
			if e.Error == "" {
				e.Error = fmt.Sprintf("http_%d", bw.status)
			}
			result = Truncate("failed:"+e.Error, MaxTarget)
		}
		if note.result != "" {
			result = Truncate(note.result, MaxTarget)
		}
		note.Target = Truncate(note.Target, MaxTarget)
		finish(r, Entry{Action: action, Result: result, Note: *note})
		w.WriteHeader(bw.status)
		_, _ = w.Write(bw.body.Bytes())
	})
}

// Truncate cuts v to n bytes without splitting a character.
func Truncate(v string, n int) string {
	if len(v) <= n {
		return v
	}
	return strings.ToValidUTF8(v[:n], "")
}
