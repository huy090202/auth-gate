// Package mail sends the Portal's fixed emails, each as a branded HTML part and
// a plain-text part: through SES in production and into a directory of files
// for local runs and tests.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed" // layout.html
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
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
	site Site
}

func NewSES(api SESAPI, from string, s Site) *SES { return &SES{api: api, from: from, site: s} }

// Send renders and sends m. It never logs the body or the link.
func (s *SES) Send(ctx context.Context, m Message) error {
	r, err := Render(m, s.site)
	if err != nil {
		return err
	}
	in := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String("AuthGate <" + s.from + ">"),
		Destination:      &types.Destination{ToAddresses: []string{m.To}},
		Content: &types.EmailContent{Simple: &types.Message{
			Subject: &types.Content{Data: aws.String(r.Subject), Charset: aws.String("UTF-8")},
			Body: &types.Body{
				Text: &types.Content{Data: aws.String(r.Text), Charset: aws.String("UTF-8")},
				Html: &types.Content{Data: aws.String(r.HTML), Charset: aws.String("UTF-8")},
			},
		}},
	}
	if s.site.Support != "" {
		in.ReplyToAddresses = []string{s.site.Support}
	}
	if _, err = s.api.SendEmail(ctx, in); err != nil {
		return fmt.Errorf("mail: ses send %s: %w", m.Kind, err)
	}
	return nil
}

// Dir writes one JSON file per message, with the html and text parts beside it,
// so local runs and tests can read links and look at the mail.
type Dir struct {
	dir  string
	site Site
}

func NewDir(dir string, s Site) (*Dir, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mail: mailbox dir: %w", err)
	}
	return &Dir{dir: dir, site: s}, nil
}

// Send writes <dir>/<RFC3339Nano>-<kind>-<random>.json, .html and .txt with mode 0600.
func (d *Dir) Send(_ context.Context, m Message) error {
	r, err := Render(m, d.site)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(struct {
		To      string `json:"to"`
		Lang    string `json:"lang"`
		Kind    string `json:"kind"`
		Subject string `json:"subject"`
		Text    string `json:"text"`
		HTML    string `json:"html"`
	}{m.To, m.Lang, m.Kind, r.Subject, r.Text, r.HTML}, "", "  ")
	if err != nil {
		return err
	}
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	base := fmt.Sprintf("%s-%s-%s", time.Now().UTC().Format(time.RFC3339Nano), m.Kind, hex.EncodeToString(rnd[:]))
	// The json goes last, so a reader that sees it also finds the other two.
	for _, f := range []struct {
		ext  string
		data []byte
	}{{".html", []byte(r.HTML)}, {".txt", []byte(r.Text)}, {".json", body}} {
		if err := d.write(base+f.ext, f.data); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dir) write(name string, data []byte) error {
	f, err := os.OpenFile(filepath.Join(d.dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // name is generated here, not user input
	if err != nil {
		return fmt.Errorf("mail: mailbox write: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("mail: mailbox write: %w", err)
	}
	return f.Close()
}

// Site says where the Portal lives and who answers questions.
type Site struct {
	BaseURL string // no trailing slash
	Support string // a bare address, or "" for none
}

// Rendered is one email, ready to send.
type Rendered struct{ Subject, Text, HTML string }

//go:embed layout.html
var layoutSrc string

var layout = template.Must(template.New("layout").Parse(layoutSrc))

// support is a line around an optional address: Pre, the address as a mailto link, Post.
type support struct{ Pre, Addr, Post string }

func (s support) plain() string { return s.Pre + s.Addr + s.Post }

func newSupport(format, addr string) support {
	pre, post, _ := strings.Cut(format, "%s")
	return support{pre, addr, post}
}

type button struct {
	Label, URL string
	Primary    bool
}

// view is everything layout.html needs; the template holds no per-kind logic.
type view struct {
	Lang, Subject, Preheader, Body, Until, Logo, BaseURL, Host string
	BodyGap, UntilGap                                          string
	Button                                                     *button
	Fallback, Link                                             string
	Footer, Support                                            support
	WhyPre, WhyPost                                            string
}

// preheader is body up to and including its first "." followed by a space or the end.
func preheader(body string) string {
	for i, r := range body {
		if r == '.' && (i+1 == len(body) || body[i+1] == ' ') {
			return body[:i+1]
		}
	}
	return body
}

// Render builds the subject, plain-text part and HTML part of m from the fixed templates.
func Render(m Message, s Site) (Rendered, error) {
	t, ok := templates[m.Kind]
	if !ok {
		return Rendered{}, fmt.Errorf("mail: unknown kind %q", m.Kind)
	}
	l, ok := t[m.Lang]
	if !ok {
		return Rendered{}, fmt.Errorf("mail: unknown lang %q", m.Lang)
	}
	if s.BaseURL == "" {
		return Rendered{}, fmt.Errorf("mail: site has no base URL")
	}
	for k, v := range m.Vars {
		if k != "link" && k != "until" {
			return Rendered{}, fmt.Errorf("mail: unknown var %q", k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return Rendered{}, fmt.Errorf("mail: var %q holds a line break", k)
		}
	}
	link, until := m.Vars["link"], m.Vars["until"]
	if linkKinds[m.Kind] && link == "" {
		return Rendered{}, fmt.Errorf("mail: kind %q needs a link", m.Kind)
	}
	if link != "" && !strings.HasPrefix(link, s.BaseURL+"/") {
		return Rendered{}, fmt.Errorf("mail: link for %q is not under the site", m.Kind)
	}

	body := strings.TrimSuffix(l.body, ":")
	if body != l.body {
		body += "."
	}
	v := view{
		Lang: m.Lang, Subject: l.subject, Preheader: preheader(body), Body: body,
		Logo: s.BaseURL + "/email-logo.png", BaseURL: s.BaseURL,
		Host: strings.TrimPrefix(strings.TrimPrefix(s.BaseURL, "https://"), "http://"),
	}
	if until != "" {
		v.Until = untilLabel[m.Lang] + until
	}
	v.Support = newSupport(supportLine[m.Lang], s.Support)
	v.WhyPre, v.WhyPost, _ = strings.Cut(fmt.Sprintf(whyLine[m.Lang], "\x00"), "\x00")

	var textButton string
	switch {
	case linkKinds[m.Kind]:
		v.Button = &button{Label: buttons[m.Kind][m.Lang], URL: link, Primary: true}
		v.Fallback, v.Link = fallbackLine[m.Lang], link
		v.Footer = support{Pre: linkFooter[m.Lang]}
		textButton = v.Button.Label + ": " + link
	default:
		v.Footer = noticeFooterSupport(m.Lang, s.Support)
		if m.Kind != "account-deleted" {
			signIn := s.BaseURL + "/sign-in"
			v.Button = &button{Label: signInLabel[m.Lang], URL: signIn}
			textButton = v.Button.Label + ": " + signIn
		}
	}
	switch {
	case v.Button == nil && v.Until == "":
		v.BodyGap = "0"
	case v.Until != "":
		v.BodyGap = "12px"
	default:
		v.BodyGap = "28px"
	}
	v.UntilGap = "0"
	if v.Button != nil {
		v.UntilGap = "28px"
	}

	var h bytes.Buffer
	if err := layout.Execute(&h, v); err != nil {
		return Rendered{}, fmt.Errorf("mail: render html: %w", err)
	}

	blocks := []string{body}
	if v.Until != "" {
		blocks = append(blocks, v.Until)
	}
	if textButton != "" {
		blocks = append(blocks, textButton)
	}
	blocks = append(blocks, v.Footer.plain())
	if s.Support != "" {
		blocks = append(blocks, v.Support.plain())
	}
	return Rendered{Subject: l.subject, Text: strings.Join(blocks, "\n\n") + "\n", HTML: h.String()}, nil
}

// noticeFooterSupport is the footer of a notice: it names the support address when there is one.
func noticeFooterSupport(lang, addr string) support {
	if addr == "" {
		return support{Pre: noticeFooterBare[lang]}
	}
	return newSupport(noticeFooter[lang], addr)
}
