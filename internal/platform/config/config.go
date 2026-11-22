// Package config reads the role and its wiring from the environment.
package config

import (
	"errors"
	"fmt"
	"strings"
)

// Roles selectable with ROLE.
const (
	RoleVerify = "verify"
	RoleAdmin  = "admin"
	RoleJWKS   = "jwks"
	RoleDev    = "dev"
)

// Config is the environment of one function (or the local dev server).
type Config struct {
	Role, Stage, Table, Region, KMSKeyID string
	KMSKeyIDs                            []string
	AdminKeyParam, OriginVerifyParam     string
	PortalKeyParam                       string
	BaseURL, DynamoEndpoint              string
	LocalSigner                          bool
	Port                                 string
}

// Load reads the configuration and refuses a role that is missing what it needs.
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Role:              getenv("ROLE"),
		Stage:             getenv("STAGE"),
		Table:             getenv("TABLE_NAME"),
		Region:            getenv("AWS_REGION"),
		KMSKeyID:          getenv("KMS_KEY_ID"),
		KMSKeyIDs:         splitList(getenv("KMS_KEY_IDS")),
		AdminKeyParam:     getenv("ADMIN_KEY_PARAM"),
		PortalKeyParam:    getenv("PORTAL_KEY_PARAM"),
		OriginVerifyParam: getenv("ORIGIN_VERIFY_PARAM"),
		BaseURL:           getenv("BASE_URL"),
		DynamoEndpoint:    getenv("DYNAMO_ENDPOINT"),
		LocalSigner:       getenv("SIGNER") == "local",
		Port:              getenv("PORT"),
	}
	var missing []string
	need := func(name, v string) {
		if v == "" {
			missing = append(missing, name)
		}
	}
	switch c.Role {
	case RoleVerify:
		need("TABLE_NAME", c.Table)
		need("BASE_URL", c.BaseURL)
		need("ORIGIN_VERIFY_PARAM", c.OriginVerifyParam)
		if c.KMSKeyID == "" && !c.LocalSigner {
			missing = append(missing, "KMS_KEY_ID")
		}
	case RoleAdmin:
		need("TABLE_NAME", c.Table)
		need("ADMIN_KEY_PARAM", c.AdminKeyParam)
		need("PORTAL_KEY_PARAM", c.PortalKeyParam)
		need("ORIGIN_VERIFY_PARAM", c.OriginVerifyParam)
	case RoleJWKS:
		if len(c.KMSKeyIDs) == 0 && !c.LocalSigner {
			missing = append(missing, "KMS_KEY_IDS")
		}
	case RoleDev:
		need("TABLE_NAME", c.Table)
		if c.Port == "" {
			c.Port = "8080"
		}
	default:
		return Config{}, fmt.Errorf("config: ROLE %q must be one of verify, admin, jwks, dev", c.Role)
	}
	if len(missing) > 0 {
		return Config{}, errors.New("config: role " + c.Role + " requires " + strings.Join(missing, ", "))
	}
	return c, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
