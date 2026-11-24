package mail

import (
	"context"
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
)

var site = Site{BaseURL: "https://portal.example", Support: "support@example.com"}

var anchorTags = regexp.MustCompile(`</?a( [^>]*)?>`)

const link = "https://portal.example/verify?t=abc"

var wantLink = map[string]bool{"verify": true, "reset": true, "factor-reset-confirm": true, "email-change-confirm": true, "org-invite": true}

// varsFor gives a kind the vars it needs: a link for every kind (unused ones are
// ignored), and the typed values for the organisation kinds.
func varsFor(kind string) map[string]string {
	v := map[string]string{"link": link}
	if strings.HasPrefix(kind, "org-") {
		v["org"] = "Acme"
		v["inviter"] = "Ann"
		v["role"] = "admin"
	}
	return v
}

var wantButton = map[string]map[string]string{
	"verify":               {"en": "Confirm email address", "vi": "Xác nhận email"},
	"reset":                {"en": "Choose a new password", "vi": "Chọn mật khẩu mới"},
	"factor-reset-confirm": {"en": "Confirm the reset", "vi": "Xác nhận đặt lại"},
	"email-change-confirm": {"en": "Confirm new address", "vi": "Xác nhận địa chỉ mới"},
	"org-invite":           {"en": "View the invite", "vi": "Xem lời mời"},
}

var wantOrgFooter = map[string]string{
	"en": "If you think this is a mistake, ask an owner of the organisation.",
	"vi": "Nếu bạn cho rằng đây là nhầm lẫn, hãy hỏi chủ sở hữu của tổ chức.",
}

var wantInviteFooter = map[string]string{
	"en": "If you do not expect this invite, ignore this email; nothing changes unless you accept it.",
	"vi": "Nếu bạn không mong đợi lời mời này, hãy bỏ qua thư này; không có gì thay đổi trừ khi bạn chấp nhận.",
}

var wantSignIn = map[string]string{"en": "Sign in", "vi": "Đăng nhập"}

var wantLinkFooter = map[string]string{
	"en": "If you did not ask for this, ignore this email; nothing changes until the link is opened.",
	"vi": "Nếu bạn không yêu cầu, hãy bỏ qua thư này; không có gì thay đổi cho tới khi liên kết được mở.",
}

var wantNoticeFooter = map[string]string{
	"en": "If this was not you, change your password now. If you cannot sign in, write to support@example.com.",
	"vi": "Nếu không phải bạn, hãy đổi mật khẩu ngay. Nếu không đăng nhập được, hãy viết cho support@example.com.",
}

func TestRenderEveryKindInBothLanguages(t *testing.T) {
	for _, kind := range Kinds {
		for _, lang := range []string{"en", "vi"} {
			id := kind + "/" + lang
			r, err := Render(Message{Lang: lang, Kind: kind, Vars: varsFor(kind)}, site)
			if err != nil {
				t.Fatalf("%s: %v", id, err)
			}
			if r.Subject == "" || strings.ContainsAny(r.Subject, "\r\n") {
				t.Errorf("%s: bad subject %q", id, r.Subject)
			}
			for _, want := range []string{`<meta charset="utf-8">`, `alt="AuthGate"`, "AuthGate Portal", "<h1"} {
				if !strings.Contains(r.HTML, want) {
					t.Errorf("%s: html lacks %q", id, want)
				}
			}
			if !strings.Contains(r.HTML, ">"+html.EscapeString(r.Subject)+"</h1>") {
				t.Errorf("%s: subject not inside h1", id)
			}
			for name, part := range map[string]string{"html": r.HTML, "text": r.Text} {
				if !strings.Contains(part, "support@example.com") {
					t.Errorf("%s %s: lacks support address", id, name)
				}
				if strings.Contains(part, "link above") || strings.Contains(part, "liên kết ở trên") {
					t.Errorf("%s %s: still points at a link above", id, name)
				}
			}
			for name, part := range map[string]string{"html": r.HTML, "text": r.Text} {
				if wantLink[kind] {
					if strings.Contains(part, "/sign-in") {
						t.Errorf("%s %s: a link kind must not offer sign in", id, name)
					}
					continue
				}
				for _, bad := range []string{fallbackLine[lang], link, linkFooter[lang]} {
					if strings.Contains(part, bad) {
						t.Errorf("%s %s: notice carries %q", id, name, bad)
					}
				}
			}
			if !wantLink[kind] && strings.Contains(r.HTML, "word-break:break-all") {
				t.Errorf("%s: notice carries the link block", id)
			}
			wantLF := wantLinkFooter[lang]
			if kind == "org-invite" {
				wantLF = wantInviteFooter[lang]
			}
			switch {
			case wantLink[kind]:
				label := wantButton[kind][lang]
				for _, want := range []string{label, `href="` + link + `"`, "word-break:break-all", wantLF} {
					if !strings.Contains(r.HTML, want) {
						t.Errorf("%s: html lacks %q", id, want)
					}
				}
				for _, want := range []string{label + ": " + link, wantLF} {
					if !strings.Contains(r.Text, want) {
						t.Errorf("%s: text lacks %q", id, want)
					}
				}
			case kind == "account-deleted":
				if strings.Contains(r.HTML, "/sign-in") || strings.Contains(r.Text, "/sign-in") {
					t.Errorf("%s: must not offer sign in", id)
				}
				if !strings.Contains(anchorTags.ReplaceAllString(r.HTML, ""), wantNoticeFooter[lang]) || !strings.Contains(r.Text, wantNoticeFooter[lang]) {
					t.Errorf("%s: lacks notice footer", id)
				}
			default:
				for name, part := range map[string]string{"html": r.HTML, "text": r.Text} {
					plain := anchorTags.ReplaceAllString(part, "") // the address is a mailto link in the html
					wantNF := wantNoticeFooter[lang]
					if strings.HasPrefix(kind, "org-") {
						wantNF = wantOrgFooter[lang]
					}
					for _, want := range []string{"https://portal.example/sign-in", wantSignIn[lang], wantNF} {
						found := strings.Contains(part, want) || (want == wantNF && strings.Contains(plain, want))
						if !found {
							t.Errorf("%s %s: lacks %q", id, name, want)
						}
					}
				}
			}
		}
	}
	if len(Kinds) != 17 {
		t.Fatalf("expected 17 kinds, got %d", len(Kinds))
	}
}

func TestRenderWithoutSupport(t *testing.T) {
	bare := Site{BaseURL: "https://portal.example"}
	for _, kind := range Kinds {
		r, err := Render(Message{Lang: "en", Kind: kind, Vars: varsFor(kind)}, bare)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(r.HTML, "mailto:") || strings.Contains(r.Text, "mailto:") || strings.Contains(r.HTML, "Questions?") || strings.Contains(r.Text, "Questions?") {
			t.Errorf("%s: support line without a support address", kind)
		}
	}
	r, _ := Render(Message{Lang: "en", Kind: "account-locked"}, bare)
	if !strings.Contains(r.Text, "contact the operator who runs this service") {
		t.Errorf("fallback footer missing: %q", r.Text)
	}
}

func TestRenderPreheader(t *testing.T) {
	r, err := Render(Message{Lang: "en", Kind: "verify", Vars: map[string]string{"link": link}}, site)
	if err != nil {
		t.Fatal(err)
	}
	const want = "Open this link within 24 hours to confirm your email address and finish creating your account."
	if !strings.Contains(r.HTML, want) {
		t.Errorf("html lacks the preheader sentence")
	}
	if i := strings.Index(r.HTML, "display:none"); i < 0 || !strings.Contains(r.HTML[i:], want) {
		t.Errorf("no hidden preheader holding the sentence")
	}
}

func TestRenderUntil(t *testing.T) {
	r, err := Render(Message{Lang: "en", Kind: "account-locked", Vars: map[string]string{"until": "2026-11-23 10:00 UTC"}}, site)
	if err != nil {
		t.Fatal(err)
	}
	for name, part := range map[string]string{"html": r.HTML, "text": r.Text} {
		if !strings.Contains(part, "Until: 2026-11-23 10:00 UTC") {
			t.Errorf("%s lacks the until line", name)
		}
	}
}

func TestRenderEscapesTheLink(t *testing.T) {
	r, err := Render(Message{Lang: "en", Kind: "verify", Vars: map[string]string{"link": `https://portal.example/x?a="<b>&c=1`}}, site)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.HTML, "<b>") || strings.Contains(r.HTML, `"<b`) {
		t.Errorf("link not escaped in html")
	}
}

func TestRenderRefusesOffSiteLinks(t *testing.T) {
	for _, l := range []string{"https://evil.example/x", "https://portal.example.evil/x", "http://portal.example/x"} {
		if _, err := Render(Message{Lang: "en", Kind: "verify", Vars: map[string]string{"link": l}}, site); err == nil {
			t.Errorf("%s was accepted", l)
		}
	}
}

func TestRenderRequiresLinkForLinkKinds(t *testing.T) {
	for _, kind := range Kinds {
		vars := varsFor(kind)
		delete(vars, "link")
		_, err := Render(Message{Lang: "en", Kind: kind, Vars: vars}, site)
		if wantLink[kind] && err == nil {
			t.Errorf("%s rendered without a link", kind)
		}
		if !wantLink[kind] && err != nil {
			t.Errorf("%s needs no link but failed: %v", kind, err)
		}
	}
}

func TestRenderRefusesUnknownInput(t *testing.T) {
	cases := map[string]Message{
		"unknown var":  {Lang: "en", Kind: "verify", Vars: map[string]string{"link": link, "name": "Mallory"}},
		"unknown kind": {Lang: "en", Kind: "spam", Vars: map[string]string{"link": link}},
		"unknown lang": {Lang: "fr", Kind: "verify", Vars: map[string]string{"link": link}},
		"newline var":  {Lang: "en", Kind: "verify", Vars: map[string]string{"link": link + "\nBcc: a@b.c"}},
	}
	for name, m := range cases {
		if _, err := Render(m, site); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestRenderRefusesUnknownVars(t *testing.T) {
	_, err := Render(Message{Lang: "en", Kind: "account-locked", Vars: map[string]string{"displayName": "x"}}, site)
	if err == nil {
		t.Fatal("a Vars key other than link or until must be refused")
	}
}

func TestDirSenderWritesOneFilePerMessage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "box")
	s, err := NewDir(dir, site)
	if err != nil {
		t.Fatal(err)
	}
	m := Message{To: "a@example.com", Lang: "vi", Kind: "verify", Vars: map[string]string{"link": link}}
	for range 2 {
		if err := s.Send(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*-verify-*.json"))
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %v", files)
	}
	info, _ := os.Stat(files[0])
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(files[0])
	var got struct{ To, Lang, Kind, Subject, Text string }
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.To != m.To || got.Lang != "vi" || got.Kind != "verify" || got.Subject == "" || !strings.Contains(got.Text, link) {
		t.Errorf("unexpected content %+v", got)
	}
}

func TestDirWritesHTMLAndText(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "box")
	s, err := NewDir(dir, site)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), Message{To: "a@example.com", Lang: "en", Kind: "verify", Vars: map[string]string{"link": link}}); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*-verify-*.json"))
	if len(files) != 1 {
		t.Fatalf("want 1 json, got %v", files)
	}
	raw, _ := os.ReadFile(files[0])
	var got struct{ HTML, Text string }
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.HTML == "" || got.Text == "" {
		t.Errorf("json lacks html or text")
	}
	base := strings.TrimSuffix(files[0], ".json")
	for ext, want := range map[string]string{".html": got.HTML, ".txt": got.Text} {
		info, err := os.Stat(base + ext)
		if err != nil {
			t.Fatalf("%s: %v", ext, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", ext, info.Mode().Perm())
		}
		if b, _ := os.ReadFile(base + ext); string(b) != want { //nolint:gosec // path built from t.TempDir()
			t.Errorf("%s differs from the json field", ext)
		}
	}
}

type fakeSES struct{ in *sesv2.SendEmailInput }

func (f *fakeSES) SendEmail(_ context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	f.in = in
	return &sesv2.SendEmailOutput{}, nil
}

func TestSESSendsBothPartsAndReplyTo(t *testing.T) {
	f := &fakeSES{}
	s := NewSES(f, "no-reply@example.com", site)
	err := s.Send(context.Background(), Message{To: "u@example.com", Lang: "en", Kind: "verify", Vars: map[string]string{"link": link}})
	if err != nil {
		t.Fatal(err)
	}
	if *f.in.FromEmailAddress != "AuthGate <no-reply@example.com>" || len(f.in.Destination.ToAddresses) != 1 || f.in.Destination.ToAddresses[0] != "u@example.com" {
		t.Errorf("wrong addresses: %+v", f.in)
	}
	b := f.in.Content.Simple.Body
	if b.Text == nil || b.Html == nil || *b.Text.Charset != "UTF-8" || *b.Html.Charset != "UTF-8" {
		t.Errorf("expected text and html parts, both UTF-8: %+v", b)
	}
	if got := f.in.ReplyToAddresses; len(got) != 1 || got[0] != "support@example.com" {
		t.Errorf("reply-to %v", got)
	}
	if err := s.Send(context.Background(), Message{To: "u@example.com", Lang: "en", Kind: "nope"}); err == nil {
		t.Error("an unrenderable message must not be sent")
	}

	f2 := &fakeSES{}
	bare := NewSES(f2, "no-reply@example.com", Site{BaseURL: "https://portal.example"})
	if err := bare.Send(context.Background(), Message{To: "u@example.com", Lang: "en", Kind: "verify", Vars: map[string]string{"link": link}}); err != nil {
		t.Fatal(err)
	}
	if len(f2.in.ReplyToAddresses) != 0 {
		t.Errorf("reply-to without support: %v", f2.in.ReplyToAddresses)
	}
}

func orgMsg(kind, lang string, vars map[string]string) Message {
	return Message{To: "u@example.com", Lang: lang, Kind: kind, Vars: vars}
}

func TestRenderOrgKinds(t *testing.T) {
	want := map[string]map[string]string{
		"org-invite":       {"en": "Ann invited you to join Acme on the AuthGate Portal as admin.", "vi": "quản trị viên"},
		"org-removed":      {"en": "You are no longer a member of Acme.", "vi": "Acme"},
		"org-role-changed": {"en": "Your role in Acme is now admin.", "vi": "quản trị viên"},
		"org-deleted":      {"en": "The organisation Acme and its clients were deleted.", "vi": "Acme"},
	}
	for kind, byLang := range want {
		for lang, frag := range byLang {
			r, err := Render(orgMsg(kind, lang, varsFor(kind)), site)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, lang, err)
			}
			if !strings.Contains(r.Subject, "Acme") {
				t.Errorf("%s/%s: subject %q lacks the org", kind, lang, r.Subject)
			}
			for name, part := range map[string]string{"html": r.HTML, "text": r.Text} {
				if !strings.Contains(part, "Acme") || !strings.Contains(part, frag) {
					t.Errorf("%s/%s %s: lacks %q", kind, lang, name, frag)
				}
				if strings.ContainsAny(part, "{}") && !strings.Contains(part, "font-family") {
					t.Errorf("%s/%s %s: unsubstituted placeholder", kind, lang, name)
				}
			}
			if strings.Contains(r.Text, "{") {
				t.Errorf("%s/%s: unsubstituted placeholder in text: %q", kind, lang, r.Text)
			}
		}
	}
}

// Why: a display name saved before names were checked may hold a URL; the caller
// passes no inviter then, and the mail must still read well (Review Focus 5).
func TestRenderInviteWithoutInviter(t *testing.T) {
	for lang, want := range map[string]string{"en": "A member of Acme invited you", "vi": "Một thành viên của Acme"} {
		r, err := Render(orgMsg("org-invite", lang, map[string]string{"link": link, "org": "Acme", "role": "member"}), site)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(r.Text, want) {
			t.Errorf("%s: %q lacks %q", lang, r.Text, want)
		}
	}
}

// Why: typed values must never carry a link into an email, and only the org kinds take them.
func TestRenderRefusesBadOrgVars(t *testing.T) {
	long := strings.Repeat("a", 65)
	cases := map[string]Message{
		"url inviter":     orgMsg("org-invite", "en", map[string]string{"link": link, "org": "Acme", "inviter": "evil.com", "role": "member"}),
		"url org":         orgMsg("org-deleted", "en", map[string]string{"org": "see acme.io/x"}),
		"org on verify":   {Lang: "en", Kind: "verify", Vars: map[string]string{"link": link, "org": "Acme"}},
		"role root":       orgMsg("org-role-changed", "en", map[string]string{"org": "Acme", "role": "root"}),
		"missing org":     orgMsg("org-removed", "en", map[string]string{}),
		"missing role":    orgMsg("org-role-changed", "en", map[string]string{"org": "Acme"}),
		"org too long":    orgMsg("org-removed", "en", map[string]string{"org": long}),
		"control in org":  orgMsg("org-removed", "en", map[string]string{"org": "Ac\tme"}),
		"unknown org var": orgMsg("org-removed", "en", map[string]string{"org": "Acme", "name": "x"}),
	}
	for name, m := range cases {
		if _, err := Render(m, site); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestRenderEscapesOrgName(t *testing.T) {
	r, err := Render(orgMsg("org-removed", "en", map[string]string{"org": "<b>Acme</b>"}), site)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.HTML, "<b>Acme") || !strings.Contains(r.HTML, "&lt;b&gt;Acme&lt;/b&gt;") {
		t.Errorf("org name not escaped in html")
	}
}
