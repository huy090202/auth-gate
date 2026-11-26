// Command console is the AuthGate Console backend. On AWS it is a Lambda behind
// CloudFront; with CONSOLE_DEV=1 it is a plain HTTP server on PORT.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/huy090202/auth-gate/internal/console"
	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/avatar"
	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/portalapi"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
	"github.com/huy090202/auth-gate/internal/platform/ssmx"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		if err := runAdmin(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	cfg, err := console.LoadConfig(os.Getenv)
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
	var awsCfg aws.Config
	var adminKey, clientKey, operatorKey string
	var avatars avatar.Store
	var devAvatars *avatar.Dir
	if cfg.Dev {
		if devAvatars, err = avatar.NewDir(cfg.AvatarDir, console.DevAvatarUploadPath); err != nil {
			log.Fatal(err)
		}
		avatars = devAvatars
		adminKey, clientKey, operatorKey = os.Getenv("ADMIN_KEY"), os.Getenv("CLIENT_KEY"), cfg.PortalOperatorKey
	} else {
		awsCfg, err = awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
		if err != nil {
			log.Fatalf("load aws config: %v", err)
		}
		adminKey = mustSecret(ctx, awsCfg, cfg.AdminKeyParam)
		clientKey = mustSecret(ctx, awsCfg, cfg.ClientKeyParam)
		operatorKey = mustSecret(ctx, awsCfg, cfg.PortalOperatorKeyParam)
		s3c := s3.NewFromConfig(awsCfg)
		avatars = avatar.NewS3(s3c, s3.NewPresignClient(s3c), cfg.AvatarBucket)
	}
	httpc := &http.Client{Timeout: authgate.Timeout}
	// authgate.Client and Verifier copy this client and refuse redirects themselves.
	h := console.New(console.Deps{
		Config:   cfg,
		DB:       db,
		Limiter:  &ratelimit.Limiter{DB: db, Table: cfg.Table, TTL: time.Hour},
		AuthGate: &authgate.Client{BaseURL: cfg.AuthGateBaseURL, ClientKey: clientKey, AdminKey: adminKey, HTTP: httpc},
		Tokens:   &authgate.Verifier{BaseURL: cfg.AuthGateBaseURL, Audience: cfg.AuthGateClientID, HTTP: httpc},
		Portal:   &portalapi.Client{BaseURL: cfg.PortalURL, OperatorKey: operatorKey, HTTP: httpc},
		Avatars:  avatars,
		Now:      time.Now,
	})

	if cfg.Dev {
		h = console.DevRoutes(h, cfg.AvatarDir, devAvatars)
		addr := ":" + cfg.Port
		log.Printf("console dev listening on %s", addr)
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

// runAdmin runs an operator command with the caller's AWS profile. It needs the
// table, and for clear-factors AuthGate's address, the Console's client id and
// the admin key (ADMIN_KEY with CONSOLE_DEV=1, otherwise the SSM parameter
// ADMIN_KEY_PARAM). The key is read only by clear-factors.
func runAdmin(args []string) error {
	ctx := context.Background()
	table := os.Getenv("TABLE_NAME")
	if table == "" {
		return errors.New("TABLE_NAME is required")
	}
	endpoint, dev := os.Getenv("DYNAMO_ENDPOINT"), os.Getenv("CONSOLE_DEV") == "1"
	db, err := dynamo.New(ctx, endpoint)
	if err != nil {
		return err
	}
	if dev && endpoint != "" {
		if err := dynamo.EnsureTable(ctx, db, table); err != nil {
			return err
		}
	}
	d := console.CLIDeps{
		Store:           &console.Store{DB: db, Table: table},
		ConsoleClientID: os.Getenv("AUTHGATE_CLIENT_ID"),
		Now:             time.Now,
		AuthGate: func(ctx context.Context) (*authgate.Client, error) {
			base := os.Getenv("AUTHGATE_BASE_URL")
			if base == "" || os.Getenv("AUTHGATE_CLIENT_ID") == "" {
				return nil, errors.New("AUTHGATE_BASE_URL and AUTHGATE_CLIENT_ID are required")
			}
			key := os.Getenv("ADMIN_KEY")
			if !dev {
				param := os.Getenv("ADMIN_KEY_PARAM")
				if param == "" {
					return nil, errors.New("ADMIN_KEY_PARAM is required")
				}
				awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(os.Getenv("AWS_REGION")))
				if err != nil {
					return nil, fmt.Errorf("load aws config: %w", err)
				}
				if key, err = ssmx.GetSecureString(ctx, awsCfg, param); err != nil {
					return nil, err
				}
			}
			if key == "" {
				return nil, errors.New("the admin key is empty")
			}
			return &authgate.Client{BaseURL: base, AdminKey: key, HTTP: &http.Client{Timeout: authgate.Timeout}}, nil
		},
	}
	return console.RunAdmin(ctx, args, os.Stdin, os.Stdout, d)
}
