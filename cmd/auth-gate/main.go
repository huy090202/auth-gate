// Command auth-gate is one binary for three Lambda roles (verify, admin, jwks),
// selected by ROLE. ROLE=dev serves all three locally on one port.
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
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/huy090202/auth-gate/internal/auth/challenge"
	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/auth/factor"
	"github.com/huy090202/auth-gate/internal/auth/handler"
	"github.com/huy090202/auth-gate/internal/auth/service"
	"github.com/huy090202/auth-gate/internal/auth/token"
	"github.com/huy090202/auth-gate/internal/platform/config"
	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/kmsx"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
	"github.com/huy090202/auth-gate/internal/platform/ssmx"
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	if cfg.Role == config.RoleDev {
		runDev(ctx, cfg)
		return
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		log.Fatalf("load aws config: %v", err)
	}
	var h http.Handler
	switch cfg.Role {
	case config.RoleVerify:
		var signer token.Signer
		if cfg.LocalSigner {
			signer = mustLocalSigner()
		} else {
			signer = token.NewKMSSigner(kmsx.New(awsCfg), cfg.KMSKeyID)
		}
		db := mustDynamo(ctx, cfg)
		svc := newService(db, cfg, signer)
		h = handler.NewVerify(svc, svc.Clients, &ratelimit.Limiter{DB: db, Table: cfg.Table})
	case config.RoleAdmin:
		svc := newService(mustDynamo(ctx, cfg), cfg, nil)
		h = handler.NewAdmin(svc, mustSecret(ctx, awsCfg, cfg.AdminKeyParam))
	case config.RoleJWKS:
		var signers []token.Signer
		if cfg.LocalSigner {
			signers = append(signers, mustLocalSigner())
		} else {
			kms := kmsx.New(awsCfg)
			for _, id := range cfg.KMSKeyIDs {
				signers = append(signers, token.NewKMSSigner(kms, id))
			}
		}
		h = handler.NewJWKS(signers...)
	}
	// jwks is public; verify and admin only answer requests that came through CloudFront.
	if cfg.Role != config.RoleJWKS {
		h = httpx.RequireKey("x-origin-verify", httpx.StaticKey(mustSecret(ctx, awsCfg, cfg.OriginVerifyParam)))(h)
	}
	// A Function URL sends the API Gateway v2 (HTTP API) payload.
	lambda.Start(httpadapter.NewV2(h).ProxyWithContext)
}

// runDev serves verify, admin and jwks under /verify, /admin and /jwks on
// :PORT with a local signer and no origin check.
func runDev(ctx context.Context, cfg config.Config) {
	adminKey := os.Getenv("ADMIN_KEY")
	if adminKey == "" {
		log.Fatal("ROLE=dev requires ADMIN_KEY")
	}
	// One signer instance, so tokens from /verify check against /jwks.
	signer := mustLocalSigner()
	db := mustDynamo(ctx, cfg)
	svc := newService(db, cfg, signer)
	limiter := &ratelimit.Limiter{DB: db, Table: cfg.Table}
	mux := http.NewServeMux()
	mux.Handle("/verify/", http.StripPrefix("/verify", handler.NewVerify(svc, svc.Clients, limiter)))
	mux.Handle("/admin/", http.StripPrefix("/admin", handler.NewAdmin(svc, adminKey)))
	mux.Handle("/jwks/", http.StripPrefix("/jwks", handler.NewJWKS(signer)))
	addr := ":" + cfg.Port
	log.Printf("auth-gate dev listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// mustDynamo builds the DynamoDB client; against a local endpoint it also creates the table.
func mustDynamo(ctx context.Context, cfg config.Config) *dynamodb.Client {
	db, err := dynamo.New(ctx, cfg.DynamoEndpoint)
	if err != nil {
		log.Fatal(err)
	}
	if cfg.DynamoEndpoint != "" {
		if err := dynamo.EnsureTable(ctx, db, cfg.Table); err != nil {
			log.Fatal(err)
		}
	}
	return db
}

func newService(db *dynamodb.Client, cfg config.Config, signer token.Signer) *service.Service {
	return &service.Service{
		Clients:    &client.Service{Store: client.NewDynamoStore(db, cfg.Table)},
		Factors:    factor.NewDynamoStore(db, cfg.Table),
		Challenges: challenge.NewDynamoStore(db, cfg.Table),
		Signer:     signer,
		Issuer:     cfg.BaseURL,
	}
}

func mustLocalSigner() *token.LocalSigner {
	s, err := token.NewLocalSigner("local")
	if err != nil {
		log.Fatal(err)
	}
	return s
}

// mustSecret reads an SSM parameter (SecureString or String) at cold start; empty is fatal.
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
