package auth_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"crmai/internal/auth"
)

var (
	key     = []byte("0123456789abcdef0123456789abcdef-current")
	oldKey  = []byte("0123456789abcdef0123456789abcdef-previous")
	now     = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	baseReq = auth.MintRequest{Key: key, Issuer: "crm-platform", Audience: "crm-ai-platform",
		Subject: "user:42", Tenant: "acme", Scopes: []string{auth.ScopeJobsRead}, TTL: 5 * time.Minute, Now: now}
)

func verifier(t *testing.T, keys ...[]byte) *auth.Verifier {
	t.Helper()
	v, err := auth.NewVerifier(keys, "crm-platform", "crm-ai-platform", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return v.WithClock(func() time.Time { return now.Add(time.Minute) })
}

func mint(t *testing.T, mutate func(*auth.MintRequest)) string {
	t.Helper()
	r := baseReq
	if mutate != nil {
		mutate(&r)
	}
	tok, err := auth.Mint(r)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestValidToken(t *testing.T) {
	p, err := verifier(t, key).Verify(mint(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if p.Tenant != "acme" || p.Subject != "user:42" || !p.Has(auth.ScopeJobsRead) || p.Has(auth.ScopeJobsCreate) {
		t.Fatalf("unexpected principal %+v", p)
	}
}

func TestPreviousKeyAcceptedDuringRotation(t *testing.T) {
	tok := mint(t, func(r *auth.MintRequest) { r.Key = oldKey })
	if _, err := verifier(t, key).Verify(tok); err == nil {
		t.Fatal("old key must be rejected when not configured")
	}
	if _, err := verifier(t, key, oldKey).Verify(tok); err != nil {
		t.Fatalf("old key must be accepted during rotation: %v", err)
	}
}

func TestRejectedTokens(t *testing.T) {
	cases := map[string]string{
		"wrong key":       mint(t, func(r *auth.MintRequest) { r.Key = []byte(strings.Repeat("z", 40)) }),
		"wrong audience":  mint(t, func(r *auth.MintRequest) { r.Audience = "someone-else" }),
		"wrong issuer":    mint(t, func(r *auth.MintRequest) { r.Issuer = "evil" }),
		"expired":         mint(t, func(r *auth.MintRequest) { r.Now = now.Add(-time.Hour) }),
		"ttl too long":    mint(t, func(r *auth.MintRequest) { r.TTL = 2 * time.Hour }),
		"bad tenant":      mint(t, func(r *auth.MintRequest) { r.Tenant = "../other" }),
		"empty tenant":    mint(t, func(r *auth.MintRequest) { r.Tenant = "" }),
		"no subject":      mint(t, func(r *auth.MintRequest) { r.Subject = "" }),
		"garbage":         "not.a.token",
		"alg none":        noneToken(t),
		"tampered tenant": tamper(t, mint(t, nil)),
	}
	v := verifier(t, key)
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(tok); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("want ErrUnauthenticated, got %v", err)
			}
		})
	}
}

func noneToken(t *testing.T) string {
	c := auth.Claims{Tenant: "acme", RegisteredClaims: jwt.RegisteredClaims{
		Issuer: "crm-platform", Subject: "x", Audience: jwt.ClaimStrings{"crm-ai-platform"},
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute))}}
	s, err := jwt.NewWithClaims(jwt.SigningMethodNone, c).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// tamper swaps the payload for one naming another tenant, keeping the signature.
func tamper(t *testing.T, tok string) string {
	parts := strings.Split(tok, ".")
	other := mint(t, func(r *auth.MintRequest) { r.Tenant = "globex" })
	return parts[0] + "." + strings.Split(other, ".")[1] + "." + parts[2]
}

func TestShortKeyRejected(t *testing.T) {
	if _, err := auth.NewVerifier([][]byte{[]byte("short")}, "i", "a", time.Minute); err == nil {
		t.Fatal("short key must be rejected")
	}
}
