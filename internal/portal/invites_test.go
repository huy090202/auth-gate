package portal

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// inviteMail finds the newest org-invite mail to addr and returns its token and the link's text.
func inviteMail(t *testing.T, e *authEnv, addr string) (token, text string) {
	t.Helper()
	ms := e.mails(t)
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].Kind == "org-invite" && ms[i].To == addr {
			j := strings.Index(ms[i].Text, "#token=")
			if j < 0 {
				t.Fatalf("no token in %q", ms[i].Text)
			}
			return strings.Fields(ms[i].Text[j+len("#token="):])[0], ms[i].Text
		}
	}
	t.Fatalf("no org-invite mail to %s: %s", addr, kinds(ms))
	return "", ""
}

// Why: the whole feature is this round trip; the token must travel only in the mail fragment and bodies,
// and the invitee's address must match whatever case they signed up with.
func TestInviteRoundOverHTTP(t *testing.T) {
	e := newAuthEnv(t)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	_ = o
	tid := ob.do("POST", "/api/tenants", map[string]string{"name": "Acme"}).str("tenantId")
	r := ob.do("POST", "/api/t/"+tid+"/invites", map[string]string{"email": "bob@x.com", "role": "member"})
	want(t, r, 201, "")
	if r.str("email") != "bob@x.com" || r.str("role") != "member" || r.str("inviteId") == "" || r.str("expiresAt") == "" {
		t.Fatalf("body = %s", r.Body)
	}
	token, text := inviteMail(t, e, "bob@x.com")
	if !strings.Contains(text, "https://portal.test/invite/"+tid+"#token=") {
		t.Fatalf("mail = %q", text)
	}

	anon := e.browser(t, "203.0.113.9")
	body := map[string]string{"token": token}
	lk := anon.do("POST", "/api/invites/"+tid+"/lookup", body)
	want(t, lk, 200, "")
	if lk.str("email") != "bob@x.com" || lk.str("orgName") != "Acme" || lk.str("role") != "member" {
		t.Fatalf("lookup = %s", lk.Body)
	}
	if _, has := lk.m()["emailMatches"]; has {
		t.Fatalf("signed-out lookup carries emailMatches: %s", lk.Body)
	}

	_, bb := e.owner(t, "Bob@X.com", "203.0.113.2")
	lk = bb.do("POST", "/api/invites/"+tid+"/lookup", body)
	want(t, lk, 200, "")
	if lk.m()["emailMatches"] != true {
		t.Fatalf("lookup = %s", lk.Body)
	}
	acc := bb.do("POST", "/api/invites/"+tid+"/accept", body)
	want(t, acc, 200, "")
	if acc.str("tenantId") != tid {
		t.Fatalf("accept = %s", acc.Body)
	}
	if got := bb.do("GET", "/api/tenants", nil); !strings.Contains(string(got.Body), `"name":"Acme"`) || !strings.Contains(string(got.Body), `"role":"member"`) {
		t.Fatalf("tenants = %s", got.Body)
	}
	want(t, bb.do("POST", "/api/invites/"+tid+"/accept", body), 404, "invite_not_found")
	es, _, err := e.a.Store.ListAudit(context.Background(), tid, "")
	if err != nil {
		t.Fatal(err)
	}
	var acts []string
	for _, x := range es {
		acts = append(acts, x.Action+":"+x.Result)
	}
	if got := strings.Join(acts, " "); !strings.Contains(got, "invite-create:success") || !strings.Contains(got, "invite-accept:success") {
		t.Fatalf("audit = %s", got)
	}
	want(t, anon.do("POST", "/api/invites/"+tid+"/lookup", body), 404, "invite_not_found")
}

// Why: an invite link forwarded to someone else must not let them in, and must not burn the real invitee's invite.
func TestInviteWrongEmail(t *testing.T) {
	e := newAuthEnv(t)
	_, ob := e.owner(t, "o@x.com", "203.0.113.1")
	tid := ob.do("POST", "/api/tenants", map[string]string{"name": "Acme"}).str("tenantId")
	want(t, ob.do("POST", "/api/t/"+tid+"/invites", map[string]string{"email": "bob@x.com", "role": "admin"}), 201, "")
	token, _ := inviteMail(t, e, "bob@x.com")
	_, cb := e.owner(t, "carol@x.com", "203.0.113.3")
	body := map[string]string{"token": token}
	lk := cb.do("POST", "/api/invites/"+tid+"/lookup", body)
	want(t, lk, 200, "")
	if lk.m()["emailMatches"] != false {
		t.Fatalf("lookup = %s", lk.Body)
	}
	want(t, cb.do("POST", "/api/invites/"+tid+"/accept", body), 403, "invite_wrong_email")
	want(t, cb.do("POST", "/api/invites/"+tid+"/decline", body), 403, "invite_wrong_email")
	list := ob.do("GET", "/api/t/"+tid+"/invites", nil)
	want(t, list, 200, "")
	if !strings.Contains(string(list.Body), "bob@x.com") {
		t.Fatalf("invite gone: %s", list.Body)
	}
}

// Why: an admin who could invite an owner could make themselves an owner through a second account.
func TestAdminCannotInviteOwner(t *testing.T) {
	e := newAuthEnv(t)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	a, ab := e.owner(t, "a@x.com", "203.0.113.2")
	m, mb := e.owner(t, "m@x.com", "203.0.113.3")
	tid := e.orgWith(t, ob, o, orgMember{a, RoleAdmin}, orgMember{m, RoleMember})
	path := "/api/t/" + tid + "/invites"
	want(t, ab.do("POST", path, map[string]string{"email": "n@x.com", "role": "owner"}), 403, "forbidden")
	want(t, ab.do("POST", path, map[string]string{"email": "n@x.com", "role": "member"}), 201, "")
	want(t, mb.do("POST", path, map[string]string{"email": "n2@x.com", "role": "member"}), 403, "forbidden")
	want(t, mb.do("GET", path, nil), 403, "forbidden")
	want(t, ob.do("POST", path, map[string]string{"email": "n3@x.com", "role": "boss"}), 400, "invalid_role")
	want(t, ob.do("POST", path, map[string]string{"email": "nope", "role": "member"}), 400, "invalid_email")
}

// Why: a resend mails a fresh owner link; an admin who could resend one could still hand out ownership.
func TestAdminCannotResendOwnerInvite(t *testing.T) {
	e := newAuthEnv(t)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	a, ab := e.owner(t, "a@x.com", "203.0.113.2")
	tid := e.orgWith(t, ob, o, orgMember{a, RoleAdmin})
	path := "/api/t/" + tid + "/invites"
	ownerInv := ob.do("POST", path, map[string]string{"email": "n@x.com", "role": "owner"})
	want(t, ownerInv, 201, "")
	memberInv := ob.do("POST", path, map[string]string{"email": "n2@x.com", "role": "member"})
	want(t, memberInv, 201, "")
	before := len(e.mails(t))
	want(t, ab.do("POST", path+"/"+ownerInv.str("inviteId")+"/resend", nil), 403, "forbidden")
	if len(e.mails(t)) != before {
		t.Fatal("a refused resend sent mail")
	}
	want(t, ab.do("POST", path+"/"+memberInv.str("inviteId")+"/resend", nil), 200, "")
	want(t, ob.do("POST", path+"/"+ownerInv.str("inviteId")+"/resend", nil), 200, "")
	want(t, ab.do("DELETE", path+"/"+ownerInv.str("inviteId"), nil), 204, "")
}

// Why: inviting a member would mail them a link that can only fail, and hint at who is in the organisation.
func TestInviteAlreadyMember(t *testing.T) {
	e := newAuthEnv(t)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	m, _ := e.owner(t, "m@x.com", "203.0.113.2")
	tid := e.orgWith(t, ob, o, orgMember{m, RoleMember})
	want(t, ob.do("POST", "/api/t/"+tid+"/invites", map[string]string{"email": " M@x.com", "role": "member"}), 409, "already_member")
}

// Why: invite mail is the one thing a member can make the service send to strangers; the day's cap must hold
// even when invites are revoked to free pending slots.
func TestInviteDailyLimit(t *testing.T) {
	e := newAuthEnv(t)
	_, ob := e.owner(t, "o@x.com", "203.0.113.1")
	tid := ob.do("POST", "/api/tenants", map[string]string{"name": "Acme"}).str("tenantId")
	path := "/api/t/" + tid + "/invites"
	for i := 0; i < 20; i++ {
		r := ob.do("POST", path, map[string]string{"email": fmt.Sprintf("u%d@x.com", i), "role": "member"})
		want(t, r, 201, "")
		if i%2 == 0 {
			want(t, ob.do("DELETE", path+"/"+r.str("inviteId"), nil), 204, "")
		}
	}
	want(t, ob.do("POST", path, map[string]string{"email": "u21@x.com", "role": "member"}), 429, "rate_limited")
}

// Why: a deleting organisation must not leak its name or accept new members through an old link.
func TestLookupDeletingTenant(t *testing.T) {
	e := newAuthEnv(t)
	_, ob := e.owner(t, "o@x.com", "203.0.113.1")
	tid := ob.do("POST", "/api/tenants", map[string]string{"name": "Acme"}).str("tenantId")
	want(t, ob.do("POST", "/api/t/"+tid+"/invites", map[string]string{"email": "bob@x.com", "role": "member"}), 201, "")
	token, _ := inviteMail(t, e, "bob@x.com")
	if _, err := e.a.Store.DB.UpdateItem(context.Background(), &dynamodb.UpdateItemInput{
		TableName: aws.String(e.a.Store.Table), Key: tenantKey(tid),
		UpdateExpression: aws.String("SET deletingAt = :d"), ExpressionAttributeValues: item{":d": s(ts(e.a.now()))},
	}); err != nil {
		t.Fatal(err)
	}
	anon := e.browser(t, "203.0.113.9")
	want(t, anon.do("POST", "/api/invites/"+tid+"/lookup", map[string]string{"token": token}), 404, "invite_not_found")
}
