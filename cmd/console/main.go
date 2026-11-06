// Command console is the AuthGate Console backend. On AWS it is a Lambda behind
// CloudFront; with CONSOLE_DEV=1 it is a plain HTTP server on PORT.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/huy090202/auth-gate/internal/console"
	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
	"github.com/huy090202/auth-gate/internal/platform/ssmx"
)

func main() {
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
	h := console.New(console.Deps{
		Config:  cfg,
		DB:      db,
		Limiter: &ratelimit.Limiter{DB: db, Table: cfg.Table, TTL: time.Hour},
		Now:     time.Now,
	})

	if cfg.Dev {
		addr := ":" + cfg.Port
		log.Printf("console dev listening on %s", addr)
		srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
		log.Fatal(srv.ListenAndServe())
		return
	}

	// Only requests that came through CloudFront carry the shared origin secret.
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		log.Fatalf("load aws config: %v", err)
	}
	verify, err := ssmx.GetSecureString(ctx, awsCfg, cfg.OriginVerifyParam)
	if err != nil {
		log.Fatal(err)
	}
	if verify == "" {
		log.Fatalf("ssm parameter %s is empty", cfg.OriginVerifyParam)
	}
	h = httpx.RequireKey("x-origin-verify", httpx.StaticKey(verify))(h)
	// A Function URL sends the API Gateway v2 (HTTP API) payload.
	lambda.Start(httpadapter.NewV2(h).ProxyWithContext)
}
