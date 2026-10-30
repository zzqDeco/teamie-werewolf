package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrMissingBearerToken = errors.New("missing bearer token")
	ErrInvalidToken       = errors.New("invalid or expired context token")
)

// Claims represents the verified symmetric HS256 Teamie Marketplace Context Token payload.
// Room isolation primary key: (WorkspaceID, ConversationID).
// Player identity primary key: UserID (extracted strictly from JWT "sub").
type Claims struct {
	UserID         string `json:"userId"`
	DisplayName    string `json:"displayName"`
	WorkspaceID    string `json:"workspaceId"`
	ConversationID string `json:"conversationId"`
	AppID          string `json:"appId"`
	Slug           string `json:"slug"`
	IssuedAt       int64  `json:"iat"`
	ExpiresAt      int64  `json:"exp"`
}

type rawTeamieClaims struct {
	AppID          string `json:"appId"`
	Slug           string `json:"slug"`
	WorkspaceID    string `json:"workspaceId"`
	ConversationID string `json:"conversationId"`
	UserID         string `json:"userId"`
	DisplayName    string `json:"displayName"`
	jwt.RegisteredClaims
}

// Verifier strictly validates Teamie symmetric HS256 Context Tokens.
type Verifier struct {
	appID         string
	signingSecret []byte
	nowFunc       func() time.Time
}

// NewVerifier constructs a strict HS256 Context Token verifier.
func NewVerifier(appID, signingSecret string) (*Verifier, error) {
	appID = strings.TrimSpace(appID)
	signingSecret = strings.TrimSpace(signingSecret)
	if appID == "" {
		return nil, errors.New("TEAMIE_APP_ID is required")
	}
	if signingSecret == "" {
		return nil, errors.New("TEAMIE_APP_SIGNING_SECRET is required")
	}
	return &Verifier{
		appID:         appID,
		signingSecret: []byte(signingSecret),
		nowFunc:       time.Now,
	}, nil
}

// WithNow overrides the clock for deterministic testing.
func (v *Verifier) WithNow(fn func() time.Time) *Verifier {
	v.nowFunc = fn
	return v
}

// AppID returns the configured Teamie App ID.
func (v *Verifier) AppID() string {
	return v.appID
}

// VerifyAuthorizationHeader extracts and validates "Authorization: Bearer <token>".
func (v *Verifier) VerifyAuthorizationHeader(authHeader string) (*Claims, error) {
	trimmed := strings.TrimSpace(authHeader)
	if len(trimmed) < 7 || !strings.EqualFold(trimmed[:7], "Bearer ") {
		return nil, ErrMissingBearerToken
	}
	rawToken := strings.TrimSpace(trimmed[7:])
	if rawToken == "" {
		return nil, ErrMissingBearerToken
	}
	return v.VerifyToken(rawToken)
}

// VerifyToken validates a raw JWT string using strict HS256, iss="teamie", aud=TEAMIE_APP_ID, and exp checks.
func (v *Verifier) VerifyToken(rawToken string) (*Claims, error) {
	if v == nil || len(v.signingSecret) == 0 || v.appID == "" {
		return nil, ErrInvalidToken
	}
	rc := &rawTeamieClaims{}
	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("teamie"),
		jwt.WithAudience(v.appID),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(v.nowFunc),
	}
	tok, err := jwt.ParseWithClaims(rawToken, rc, func(t *jwt.Token) (any, error) {
		if t.Method == nil || t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return v.signingSecret, nil
	}, parserOpts...)
	if err != nil || tok == nil || !tok.Valid {
		return nil, ErrInvalidToken
	}

	sub := strings.TrimSpace(rc.Subject)
	wsID := strings.TrimSpace(rc.WorkspaceID)
	convID := strings.TrimSpace(rc.ConversationID)
	if sub == "" || wsID == "" || convID == "" {
		return nil, ErrInvalidToken
	}

	displayName := strings.TrimSpace(rc.DisplayName)
	if displayName == "" {
		if len(sub) >= 8 {
			displayName = "玩家-" + sub[:6]
		} else {
			displayName = "玩家-" + sub
		}
	}

	var iat, exp int64
	if rc.IssuedAt != nil {
		iat = rc.IssuedAt.Unix()
	}
	if rc.ExpiresAt != nil {
		exp = rc.ExpiresAt.Unix()
	}

	appID := strings.TrimSpace(rc.AppID)
	if appID == "" {
		appID = v.appID
	}
	slug := strings.TrimSpace(rc.Slug)
	if slug == "" {
		slug = "werewolf"
	}

	return &Claims{
		UserID:         sub,
		DisplayName:    displayName,
		WorkspaceID:    wsID,
		ConversationID: convID,
		AppID:          appID,
		Slug:           slug,
		IssuedAt:       iat,
		ExpiresAt:      exp,
	}, nil
}

// SignToken mints a compliant Teamie HS256 Context Token (used only by tests and DEV_SIMULATOR=true).
func SignToken(secret string, claims Claims) (string, error) {
	rc := rawTeamieClaims{
		AppID:          claims.AppID,
		Slug:           claims.Slug,
		WorkspaceID:    claims.WorkspaceID,
		ConversationID: claims.ConversationID,
		UserID:         claims.UserID,
		DisplayName:    claims.DisplayName,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "teamie",
			Audience:  jwt.ClaimStrings{claims.AppID},
			Subject:   claims.UserID,
			IssuedAt:  jwt.NewNumericDate(time.Unix(claims.IssuedAt, 0)),
			ExpiresAt: jwt.NewNumericDate(time.Unix(claims.ExpiresAt, 0)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, rc)
	return tok.SignedString([]byte(secret))
}
