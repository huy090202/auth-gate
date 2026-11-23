// Command portal is the AuthGate Portal backend. On AWS it is a Lambda behind
// CloudFront; with PORTAL_DEV=1 it is a plain HTTP server on PORT.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
	"github.com/huy090202/auth-gate/internal/platform/ssmx"
	"github.com/huy090202/auth-gate/internal/portal"
	"github.com/huy090202/auth-gate/internal/portal/avatar"
	"github.com/huy090202/auth-gate/internal/portal/hibp"
	"github.com/huy090202/auth-gate/internal/portal/mail"
	"github.com/huy090202/auth-gate/internal/portal/turnstile"
)

func main() {
	cfg, err := portal.LoadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	db, err := dynamo.New(ctx, cfg.DynamoEndpoint)
	if err != nil {
		log.Fatal(err)
	}
	if cfg.Dev && cfg.DynamoEndpoint != "" {
		if err := dynamo.EnsureTable(ctx, db, cfg.Table); err != nil {
			log.Fatal(err)
		}
	}

	httpc := &http.Client{Timeout: authgate.Timeout}
	deps := portal.Deps{
		Config:    cfg,
		DB:        db,
		Limiter:   &ratelimit.Limiter{DB: db, Table: cfg.Table, TTL: time.Hour},
		Tokens:    &authgate.Verifier{BaseURL: cfg.AuthGateBaseURL, Audience: cfg.AuthGateClientID, HTTP: httpc},
		Passwords: hibp.NewClient(),
		Now:       time.Now,
	}
	site := mail.Site{BaseURL: cfg.PublicBaseURL}
	var awsCfg aws.Config
	var devAvatars *avatar.Dir
	if cfg.Dev {
		deps.AuthGate = &authgate.Client{BaseURL: cfg.AuthGateBaseURL, ClientKey: cfg.ClientKey, PortalKey: cfg.PortalKey, HTTP: httpc}
		deps.Turnstile = turnstile.NewClient(cfg.TurnstileSecret)
		if deps.Mail, err = mail.NewDir(cfg.MailboxDir, site); err != nil {
			log.Fatal(err)
		}
		if devAvatars, err = avatar.NewDir(cfg.AvatarDir, cfg.PublicBaseURL+portal.DevAvatarUploadPath); err != nil {
			log.Fatal(err)
		}
		deps.Avatars = devAvatars
	} else {
		awsCfg, err = awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
		if err != nil {
			log.Fatalf("load aws config: %v", err)
		}
		deps.AuthGate = &authgate.Client{
			BaseURL: cfg.AuthGateBaseURL, ClientKey: mustSecret(ctx, awsCfg, cfg.ClientKeyParam),
			PortalKey: mustSecret(ctx, awsCfg, cfg.PortalKeyParam), HTTP: httpc,
		}
		deps.Turnstile = turnstile.NewClient(mustSecret(ctx, awsCfg, cfg.TurnstileSecretParam))
		deps.Mail = mail.NewSES(sesv2.NewFromConfig(awsCfg), cfg.EmailFrom, site)
		s3c := s3.NewFromConfig(awsCfg)
		deps.Avatars = avatar.NewS3(s3c, s3.NewPresignClient(s3c), cfg.AvatarBucket)
	}
	h := portal.New(deps)

	if cfg.Dev {
		h = portal.DevRoutes(h, cfg.AvatarDir, devAvatars)
		addr := ":" + cfg.Port
		log.Printf("portal dev listening on %s", addr)
		srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
		log.Fatal(srv.ListenAndServe())
		return
	}

	// Only requests that came through CloudFront carry the shared origin secret.
	verify := mustSecret(ctx, awsCfg, cfg.OriginVerifyParam)
	h = httpx.RequireKey("x-origin-verify", httpx.StaticKey(verify))(h)
	// A Function URL sends the API Gateway v2 (HTTP API) payload.
	lambda.Start(httpadapter.NewV2(h).ProxyWithContext)
}

// mustSecret reads an SSM parameter at cold start; an empty value is fatal.
func mustSecret(ctx context.Context, awsCfg aws.Config, name string) string {
	v, err := ssmx.GetSecureString(ctx, awsCfg, name)
	if err != nil {
		log.Fatal(err)
	}
	if v == "" {
		log.Fatalf("ssm parameter %s is empty", name)
	}
	return v
}
