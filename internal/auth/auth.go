// Package auth verifies the short-lived service tokens callers present to the
// gateway.
//
// A token is an HS256 JWT minted by a trusted caller (the CRM backend, after
// it has authenticated the user and checked their membership). It names
// exactly one tenant and a set of scopes. The gateway never accepts a tenant
// from the request body: the tenant is whatever the verified token says.
package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"crmai/internal/jobs"
)

// Scopes understood by the gateway.
const (
	ScopeJobsCreate = "ai.jobs.create"
	ScopeJobsRead   = "ai.jobs.read"
	ScopeJobsCancel = "ai.jobs.cancel"
	// Put or delete company documents for the Knowledge Assistant.
	ScopeKnowledgeWrite = "ai.knowledge.write"
)

// Principal is the verified caller.
type Principal struct {
	Subject string
	Tenant  string
	Scopes  map[string]bool
	TokenID string
}

// Has reports whether the principal was granted scope.
func (p Principal) Has(scope string) bool { return p.Scopes[scope] }

// ErrUnauthenticated is returned for any token problem. The reason is kept
// for server logs only; clients get a uniform 401.
var ErrUnauthenticated = errors.New("unauthenticated")

// Claims is the token payload.
type Claims struct {
	Tenant string `json:"tenant"`
	Scope  string `json:"scope"`
	jwt.RegisteredClaims
}

// Verifier checks tokens against the current and (during rotation) previous
// signing keys.
type Verifier struct {
	keys     [][]byte
	issuer   string
	audience string
	maxTTL   time.Duration
	leeway   time.Duration
	now      func() time.Time
}

// NewVerifier requires at least one key of 32+ bytes.
func NewVerifier(keys [][]byte, issuer, audience string, maxTTL time.Duration) (*Verifier, error) {
	if len(keys) == 0 {
		return nil, errors.New("auth: no signing key")
	}
	for _, k := range keys {
		if len(k) < 32 {
			return nil, errors.New("auth: signing key shorter than 32 bytes")
		}
	}
	return &Verifier{keys: keys, issuer: issuer, audience: audience, maxTTL: maxTTL,
		leeway: 30 * time.Second, now: time.Now}, nil
}

// WithClock replaces the clock (tests).
func (v *Verifier) WithClock(now func() time.Time) *Verifier {
	c := *v
	c.now = now
	return &c
}

// Verify parses and validates a raw bearer token.
func (v *Verifier) Verify(raw string) (Principal, error) {
	var lastErr error
	for _, key := range v.keys {
		claims := &Claims{}
		_, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return key, nil },
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
			jwt.WithIssuer(v.issuer),
			jwt.WithAudience(v.audience),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
			jwt.WithLeeway(v.leeway),
			jwt.WithTimeFunc(v.now),
		)
		if err != nil {
			lastErr = err
			if errors.Is(err, jwt.ErrTokenSignatureInvalid) {
				continue // maybe signed with the other key
			}
			break
		}
		return v.principal(claims)
	}
	return Principal{}, fmt.Errorf("%w: %v", ErrUnauthenticated, lastErr)
}

func (v *Verifier) principal(c *Claims) (Principal, error) {
	if c.IssuedAt == nil || c.ExpiresAt == nil || c.ExpiresAt.Sub(c.IssuedAt.Time) > v.maxTTL {
		return Principal{}, fmt.Errorf("%w: token lifetime exceeds %s", ErrUnauthenticated, v.maxTTL)
	}
	if c.Subject == "" || len(c.Subject) > 128 {
		return Principal{}, fmt.Errorf("%w: bad subject", ErrUnauthenticated)
	}
	if !jobs.ValidTenantID(c.Tenant) {
		return Principal{}, fmt.Errorf("%w: bad tenant claim", ErrUnauthenticated)
	}
	scopes := map[string]bool{}
	for _, s := range strings.Fields(c.Scope) {
		scopes[s] = true
	}
	return Principal{Subject: c.Subject, Tenant: c.Tenant, Scopes: scopes, TokenID: c.ID}, nil
}

// MintRequest describes a token to mint (local development and tests; in
// production the CRM backend mints tokens).
type MintRequest struct {
	Key      []byte
	Issuer   string
	Audience string
	Subject  string
	Tenant   string
	Scopes   []string
	TTL      time.Duration
	Now      time.Time
	ID       string
}

// Mint signs a token.
func Mint(r MintRequest) (string, error) {
	now := r.Now
	if now.IsZero() {
		now = time.Now()
	}
	c := Claims{
		Tenant: r.Tenant,
		Scope:  strings.Join(r.Scopes, " "),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    r.Issuer,
			Subject:   r.Subject,
			Audience:  jwt.ClaimStrings{r.Audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(r.TTL)),
			ID:        r.ID,
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(r.Key)
}
