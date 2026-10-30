package auth

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestVerifierStrictHS256Validation(t *testing.T) {
	const (
		appID  = "app-11111111-2222-3333-4444-555555555555"
		secret = "tmsec_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	)
	fixedNow := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	v, err := NewVerifier(appID, secret)
	if err != nil {
		t.Fatalf("NewVerifier failed: %v", err)
	}
	v.WithNow(func() time.Time { return fixedNow })

	validToken, err := SignToken(secret, Claims{
		UserID:         "user-001",
		DisplayName:    "Alice",
		WorkspaceID:    "ws-001",
		ConversationID: "conv-001",
		AppID:          appID,
		Slug:           "werewolf",
		IssuedAt:       fixedNow.Unix(),
		ExpiresAt:      fixedNow.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("SignToken failed: %v", err)
	}

	got, err := v.VerifyAuthorizationHeader("Bearer " + validToken)
	if err != nil {
		t.Fatalf("expected valid token to pass, got err=%v", err)
	}
	if got.UserID != "user-001" || got.WorkspaceID != "ws-001" || got.ConversationID != "conv-001" || got.DisplayName != "Alice" {
		t.Fatalf("unexpected verified claims: %+v", got)
	}

	// 1. Missing Authorization header
	if _, err := v.VerifyAuthorizationHeader(""); err == nil {
		t.Fatal("expected error on empty Authorization header")
	}

	// 2. Forged signature with wrong secret
	forgedToken, _ := SignToken("tmsec_wrong_secret", Claims{
		UserID:         "user-001",
		DisplayName:    "Forger",
		WorkspaceID:    "ws-001",
		ConversationID: "conv-001",
		AppID:          appID,
		Slug:           "werewolf",
		IssuedAt:       fixedNow.Unix(),
		ExpiresAt:      fixedNow.Add(time.Hour).Unix(),
	})
	if _, err := v.VerifyAuthorizationHeader("Bearer " + forgedToken); err == nil {
		t.Fatal("expected forged signature token to fail")
	}

	// 3. alg=none attack
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	bodyBytes, _ := json.Marshal(map[string]any{
		"iss":            "teamie",
		"aud":            appID,
		"sub":            "user-001",
		"workspaceId":    "ws-001",
		"conversationId": "conv-001",
		"exp":            fixedNow.Add(time.Hour).Unix(),
	})
	noneToken := hdr + "." + base64.RawURLEncoding.EncodeToString(bodyBytes) + "."
	if _, err := v.VerifyAuthorizationHeader("Bearer " + noneToken); err == nil {
		t.Fatal("expected alg=none token to fail")
	}

	// 4. HS384 algorithm confusion
	hs384Tok := jwt.NewWithClaims(jwt.SigningMethodHS384, jwt.MapClaims{
		"iss":            "teamie",
		"aud":            appID,
		"sub":            "user-001",
		"workspaceId":    "ws-001",
		"conversationId": "conv-001",
		"exp":            fixedNow.Add(time.Hour).Unix(),
	})
	hs384Signed, _ := hs384Tok.SignedString([]byte(secret))
	if _, err := v.VerifyAuthorizationHeader("Bearer " + hs384Signed); err == nil {
		t.Fatal("expected HS384 token to be rejected when only HS256 is permitted")
	}

	// 5. Wrong issuer
	wrongIssTok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss":            "other-platform",
		"aud":            appID,
		"sub":            "user-001",
		"workspaceId":    "ws-001",
		"conversationId": "conv-001",
		"exp":            fixedNow.Add(time.Hour).Unix(),
	})
	wrongIssSigned, _ := wrongIssTok.SignedString([]byte(secret))
	if _, err := v.VerifyAuthorizationHeader("Bearer " + wrongIssSigned); err == nil {
		t.Fatal("expected wrong issuer to fail")
	}

	// 6. Wrong audience
	wrongAudToken, _ := SignToken(secret, Claims{
		UserID:         "user-001",
		WorkspaceID:    "ws-001",
		ConversationID: "conv-001",
		AppID:          "other-app-id",
		IssuedAt:       fixedNow.Unix(),
		ExpiresAt:      fixedNow.Add(time.Hour).Unix(),
	})
	if _, err := v.VerifyAuthorizationHeader("Bearer " + wrongAudToken); err == nil {
		t.Fatal("expected wrong audience to fail")
	}

	// 7. Expired token
	expiredToken, _ := SignToken(secret, Claims{
		UserID:         "user-001",
		WorkspaceID:    "ws-001",
		ConversationID: "conv-001",
		AppID:          appID,
		IssuedAt:       fixedNow.Add(-2 * time.Hour).Unix(),
		ExpiresAt:      fixedNow.Add(-1 * time.Second).Unix(),
	})
	if _, err := v.VerifyAuthorizationHeader("Bearer " + expiredToken); err == nil {
		t.Fatal("expected expired token to fail")
	}
}
