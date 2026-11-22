package mail

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
)

var wantLink = map[string]bool{"verify": true, "reset": true, "factor-reset-confirm": true, "email-change-confirm": true}

var wantNotYou = map[string]string{
	"en": "If this was not you",
	"vi": "Nếu đây không phải là bạn",
}

func TestRenderEveryKindInBothLanguages(t *testing.T) {
	const link = "https://portal.example/verify?t=abc"
	for _, kind := range Kinds {
		for _, lang := range []string{"en", "vi"} {
			subject, text, err := Render(Message{Lang: lang, Kind: kind, Vars: map[string]string{"link": link, "until": "2026-11-23 10:00 UTC"}})
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, lang, err)
			}
			if subject == "" || strings.ContainsAny(subject, "\r\n") {
				t.Errorf("%s/%s: bad subject %q", kind, lang, subject)
			}
			if !strings.Contains(text, link) {
				t.Errorf("%s/%s: body lacks the link", kind, lang)
			}
			if !strings.Contains(text, wantNotYou[lang]) {
				t.Errorf("%s/%s: body lacks the not-you line", kind, lang)
			}
		}
	}
	if len(Kinds) != 13 {
		t.Fatalf("expected 13 kinds, got %d", len(Kinds))
	}
}

func TestRenderRequiresLinkForLinkKinds(t *testing.T) {
	for _, kind := range Kinds {
		_, _, err := Render(Message{Lang: "en", Kind: kind})
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
		"unknown var":  {Lang: "en", Kind: "verify", Vars: map[string]string{"link": "x", "name": "Mallory"}},
		"unknown kind": {Lang: "en", Kind: "spam", Vars: map[string]string{"link": "x"}},
		"unknown lang": {Lang: "fr", Kind: "verify", Vars: map[string]string{"link": "x"}},
		"newline var":  {Lang: "en", Kind: "verify", Vars: map[string]string{"link": "x\nBcc: a@b.c"}},
	}
	for name, m := range cases {
		if _, _, err := Render(m); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestRenderRefusesUnknownVars(t *testing.T) {
	_, _, err := Render(Message{Lang: "en", Kind: "account-locked", Vars: map[string]string{"displayName": "x"}})
	if err == nil {
		t.Fatal("a Vars key other than link or until must be refused")
	}
}

func TestDirSenderWritesOneFilePerMessage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "box")
	s, err := NewDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := Message{To: "a@example.com", Lang: "vi", Kind: "verify", Vars: map[string]string{"link": "https://p/x"}}
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
	if got.To != m.To || got.Lang != "vi" || got.Kind != "verify" || got.Subject == "" || !strings.Contains(got.Text, "https://p/x") {
		t.Errorf("unexpected content %+v", got)
	}
}

type fakeSES struct{ in *sesv2.SendEmailInput }

func (f *fakeSES) SendEmail(_ context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	f.in = in
	return &sesv2.SendEmailOutput{}, nil
}

func TestSESSendUsesConfiguredFromAndMessageTo(t *testing.T) {
	f := &fakeSES{}
	s := NewSES(f, "Auth Gate <no-reply@example.com>")
	err := s.Send(context.Background(), Message{To: "u@example.com", Lang: "en", Kind: "verify", Vars: map[string]string{"link": "https://p/x"}})
	if err != nil {
		t.Fatal(err)
	}
	if *f.in.FromEmailAddress != "Auth Gate <no-reply@example.com>" || len(f.in.Destination.ToAddresses) != 1 || f.in.Destination.ToAddresses[0] != "u@example.com" {
		t.Errorf("wrong addresses: %+v", f.in)
	}
	if f.in.Content.Simple == nil || f.in.Content.Simple.Body.Text == nil || f.in.Content.Simple.Body.Html != nil {
		t.Errorf("expected a plain-text body only")
	}
	if err := s.Send(context.Background(), Message{To: "u@example.com", Lang: "en", Kind: "nope"}); err == nil {
		t.Error("an unrenderable message must not be sent")
	}
}
