// Package portal is the AuthGate Portal backend: self-service accounts,
// sessions, workspaces and the HTTP surface the browser talks to. It reaches
// AuthGate only over HTTP and must not import internal/auth/service or
// internal/console.
package portal

import (
	"errors"
	"strings"
)

// Cloudflare's published Turnstile test keys, used in Dev and tests.
const (
	TurnstileTestSiteKey   = "1x00000000000000000000AA"
	TurnstileTestSecretKey = "1x0000000000000000000000000000000AA"
)

// Config is the environment of the Portal binary.
type Config struct {
	Stage, Table, Region                                 string
	AuthGateBaseURL, AuthGateClientID                    string
	PublicBaseURL, EmailFrom, AvatarBucket               string
	TurnstileSiteKey                                     string
	ClientKeyParam, PortalKeyParam, TurnstileSecretParam string
	OriginVerifyParam                                    string
	DynamoEndpoint, Port                                 string
	Dev                                                  bool

	// Dev only: the keys come from the environment instead of SSM, mail and
	// avatars go to local directories, Turnstile uses Cloudflare's test keys.
	ClientKey, PortalKey, TurnstileSecret string
	MailboxDir, AvatarDir                 string
}

// LoadConfig reads the environment and refuses to start with anything missing.
// With PORTAL_DEV=1 the keys come from CLIENT_KEY and PORTAL_KEY (default
// "dev-portal"), so the SSM parameter names, the sender and the bucket are not
// needed.
func LoadConfig(getenv func(string) string) (Config, error) {
	c := Config{
		Stage:                getenv("STAGE"),
		Table:                getenv("TABLE_NAME"),
		Region:               getenv("AWS_REGION"),
		AuthGateBaseURL:      getenv("AUTHGATE_BASE_URL"),
		AuthGateClientID:     getenv("AUTHGATE_CLIENT_ID"),
		PublicBaseURL:        strings.TrimRight(getenv("PUBLIC_BASE_URL"), "/"),
		EmailFrom:            getenv("EMAIL_FROM"),
		AvatarBucket:         getenv("AVATAR_BUCKET"),
		TurnstileSiteKey:     getenv("TURNSTILE_SITE_KEY"),
		ClientKeyParam:       getenv("CLIENT_KEY_PARAM"),
		PortalKeyParam:       getenv("PORTAL_KEY_PARAM"),
		TurnstileSecretParam: getenv("TURNSTILE_SECRET_PARAM"),
		OriginVerifyParam:    getenv("ORIGIN_VERIFY_PARAM"),
		DynamoEndpoint:       getenv("DYNAMO_ENDPOINT"),
		Port:                 getenv("PORT"),
		Dev:                  getenv("PORTAL_DEV") == "1",
	}
	var missing []string
	need := func(name, v string) {
		if v == "" {
			missing = append(missing, name)
		}
	}
	need("TABLE_NAME", c.Table)
	need("AUTHGATE_BASE_URL", c.AuthGateBaseURL)
	need("AUTHGATE_CLIENT_ID", c.AuthGateClientID)
	if c.Dev {
		c.ClientKey = getenv("CLIENT_KEY")
		need("CLIENT_KEY", c.ClientKey)
		c.PortalKey = orDefault(getenv("PORTAL_KEY"), "dev-portal")
		c.TurnstileSecret = TurnstileTestSecretKey
		c.TurnstileSiteKey = orDefault(c.TurnstileSiteKey, TurnstileTestSiteKey)
		c.Port = orDefault(c.Port, "8789")
		c.PublicBaseURL = orDefault(c.PublicBaseURL, "http://localhost:"+c.Port)
		c.MailboxDir = orDefault(getenv("MAILBOX_DIR"), ".portal-mailbox")
		c.AvatarDir = orDefault(getenv("AVATAR_DIR"), ".portal-avatars")
	} else {
		need("AWS_REGION", c.Region)
		need("PUBLIC_BASE_URL", c.PublicBaseURL)
		need("EMAIL_FROM", c.EmailFrom)
		need("AVATAR_BUCKET", c.AvatarBucket)
		need("TURNSTILE_SITE_KEY", c.TurnstileSiteKey)
		need("CLIENT_KEY_PARAM", c.ClientKeyParam)
		need("PORTAL_KEY_PARAM", c.PortalKeyParam)
		need("TURNSTILE_SECRET_PARAM", c.TurnstileSecretParam)
		need("ORIGIN_VERIFY_PARAM", c.OriginVerifyParam)
	}
	if len(missing) > 0 {
		return Config{}, errors.New("config: portal requires " + strings.Join(missing, ", "))
	}
	return c, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
