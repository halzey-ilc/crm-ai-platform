// Command devtoken mints a short-lived gateway token for local development.
// In production the CRM backend mints tokens; this tool refuses to run when
// AI_ENV=production.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"crmai/internal/auth"
)

func main() {
	tenant := flag.String("tenant", "demo", "tenant slug the token is bound to")
	subject := flag.String("sub", "user:local-dev", "caller identity")
	scopes := flag.String("scopes", "ai.jobs.create ai.jobs.read ai.jobs.cancel", "space-separated scopes")
	ttl := flag.Duration("ttl", 10*time.Minute, "token lifetime")
	flag.Parse()

	if os.Getenv("AI_ENV") == "production" {
		fmt.Fprintln(os.Stderr, "devtoken: refusing to mint tokens with AI_ENV=production")
		os.Exit(2)
	}
	key := os.Getenv("AI_AUTH_HS256_KEY")
	if len(key) < 32 {
		fmt.Fprintln(os.Stderr, "devtoken: AI_AUTH_HS256_KEY must be set (>= 32 bytes)")
		os.Exit(2)
	}
	iss, aud := os.Getenv("AI_AUTH_ISSUER"), os.Getenv("AI_AUTH_AUDIENCE")
	if iss == "" {
		iss = "crm-platform"
	}
	if aud == "" {
		aud = "crm-ai-platform"
	}
	tok, err := auth.Mint(auth.MintRequest{
		Key: []byte(key), Issuer: iss, Audience: aud, Subject: *subject, Tenant: *tenant,
		Scopes: strings.Fields(*scopes), TTL: *ttl,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "devtoken:", err)
		os.Exit(1)
	}
	fmt.Println(tok)
}
