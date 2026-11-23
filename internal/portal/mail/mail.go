// Package mail sends the Portal's fixed, plain-text emails: through SES in
// production and into a directory of JSON files for local runs and tests.
package mail

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// Message is one email to send. Vars holds only "link" and "until": nothing a
// user typed is ever interpolated into an email.
type Message struct {
	To   string
	Lang string // "en" or "vi"
	Kind string
	Vars map[string]string
}

// Sender delivers a Message.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// SESAPI is the part of the sesv2 client the sender uses.
type SESAPI interface {
	SendEmail(ctx context.Context, in *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

// SES sends through SES v2 from one configured bare address, shown as "AuthGate <address>".
type SES struct {
	api  SESAPI
	from string
}

func NewSES(api SESAPI, from string) *SES { return &SES{api: api, from: from} }

// Send renders and sends m. It never logs the body or the link.
func (s *SES) Send(ctx context.Context, m Message) error {
	subject, text, err := Render(m)
	if err != nil {
		return err
	}
	_, err = s.api.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String("AuthGate <" + s.from + ">"),
		Destination:      &types.Destination{ToAddresses: []string{m.To}},
		Content: &types.EmailContent{Simple: &types.Message{
			Subject: &types.Content{Data: aws.String(subject), Charset: aws.String("UTF-8")},
			Body:    &types.Body{Text: &types.Content{Data: aws.String(text), Charset: aws.String("UTF-8")}},
		}},
	})
	if err != nil {
		return fmt.Errorf("mail: ses send %s: %w", m.Kind, err)
	}
	return nil
}

// Dir writes one JSON file per message, so local runs and tests can read links.
type Dir struct{ dir string }

func NewDir(dir string) (*Dir, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mail: mailbox dir: %w", err)
	}
	return &Dir{dir: dir}, nil
}

// Send writes <dir>/<RFC3339Nano>-<kind>-<random>.json with mode 0600.
func (d *Dir) Send(_ context.Context, m Message) error {
	subject, text, err := Render(m)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(struct {
		To      string `json:"to"`
		Lang    string `json:"lang"`
		Kind    string `json:"kind"`
		Subject string `json:"subject"`
		Text    string `json:"text"`
	}{m.To, m.Lang, m.Kind, subject, text}, "", "  ")
	if err != nil {
		return err
	}
	var r [6]byte
	if _, err := rand.Read(r[:]); err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%s-%s.json", time.Now().UTC().Format(time.RFC3339Nano), m.Kind, hex.EncodeToString(r[:]))
	f, err := os.OpenFile(filepath.Join(d.dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // name is generated here, not user input
	if err != nil {
		return fmt.Errorf("mail: mailbox write: %w", err)
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("mail: mailbox write: %w", err)
	}
	return f.Close()
}

// Render builds the subject and plain-text body of m from the fixed templates.
func Render(m Message) (subject, text string, err error) {
	t, ok := templates[m.Kind]
	if !ok {
		return "", "", fmt.Errorf("mail: unknown kind %q", m.Kind)
	}
	l, ok := t[m.Lang]
	if !ok {
		return "", "", fmt.Errorf("mail: unknown lang %q", m.Lang)
	}
	for k, v := range m.Vars {
		if k != "link" && k != "until" {
			return "", "", fmt.Errorf("mail: unknown var %q", k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return "", "", fmt.Errorf("mail: var %q holds a line break", k)
		}
	}
	link, until := m.Vars["link"], m.Vars["until"]
	if linkKinds[m.Kind] && link == "" {
		return "", "", fmt.Errorf("mail: kind %q needs a link", m.Kind)
	}
	var b strings.Builder
	b.WriteString(l.body + "\n\n")
	if link != "" {
		b.WriteString(link + "\n\n")
	}
	if until != "" {
		b.WriteString(untilLabel[m.Lang] + until + "\n\n")
	}
	b.WriteString(notYou[m.Lang] + "\n")
	return l.subject, b.String(), nil
}
