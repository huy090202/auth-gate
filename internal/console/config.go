// Package console is the AuthGate Console backend: admin accounts, sessions and
// the HTTP surface the browser talks to. It reaches AuthGate only over HTTP and
// must not import internal/auth/service.
package console

import (
	"errors"
	"strings"
)

// Config is the environment of the Console binary.
type Config struct {
	Stage, Table, Region, AuthGateBaseURL, AuthGateClientID string
	AdminKeyParam, ClientKeyParam, OriginVerifyParam        string
	DynamoEndpoint, Port                                    string
	// PortalURL and PortalClientID reach the Portal's operator API; the key is
	// PortalOperatorKeyParam on a stage and PORTAL_OPERATOR_KEY in Dev.
	PortalURL, PortalClientID, PortalOperatorKeyParam string
	Dev                                               bool
}

// LoadConfig reads the environment and refuses to start with anything missing.
// With CONSOLE_DEV=1 the admin and client keys come from the ADMIN_KEY,
// CLIENT_KEY and PORTAL_OPERATOR_KEY variables, so the SSM parameter names are not needed.
func LoadConfig(getenv func(string) string) (Config, error) {
	c := Config{
		Stage:                  getenv("STAGE"),
		Table:                  getenv("TABLE_NAME"),
		Region:                 getenv("AWS_REGION"),
		AuthGateBaseURL:        getenv("AUTHGATE_BASE_URL"),
		AuthGateClientID:       getenv("AUTHGATE_CLIENT_ID"),
		AdminKeyParam:          getenv("ADMIN_KEY_PARAM"),
		ClientKeyParam:         getenv("CLIENT_KEY_PARAM"),
		OriginVerifyParam:      getenv("ORIGIN_VERIFY_PARAM"),
		PortalURL:              getenv("PORTAL_URL"),
		PortalClientID:         getenv("PORTAL_CLIENT_ID"),
		PortalOperatorKeyParam: getenv("PORTAL_OPERATOR_KEY_PARAM"),
		DynamoEndpoint:         getenv("DYNAMO_ENDPOINT"),
		Port:                   getenv("PORT"),
		Dev:                    getenv("CONSOLE_DEV") == "1",
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
	need("PORTAL_URL", c.PortalURL)
	need("PORTAL_CLIENT_ID", c.PortalClientID)
	if c.Dev {
		need("ADMIN_KEY", getenv("ADMIN_KEY"))
		need("CLIENT_KEY", getenv("CLIENT_KEY"))
		need("PORTAL_OPERATOR_KEY", getenv("PORTAL_OPERATOR_KEY"))
		if c.Port == "" {
			c.Port = "8788"
		}
	} else {
		need("ADMIN_KEY_PARAM", c.AdminKeyParam)
		need("CLIENT_KEY_PARAM", c.ClientKeyParam)
		need("ORIGIN_VERIFY_PARAM", c.OriginVerifyParam)
		need("PORTAL_OPERATOR_KEY_PARAM", c.PortalOperatorKeyParam)
	}
	if len(missing) > 0 {
		return Config{}, errors.New("config: console requires " + strings.Join(missing, ", "))
	}
	return c, nil
}
