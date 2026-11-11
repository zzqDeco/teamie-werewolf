package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zzqDeco/teamie-werewolf/internal/auth"
	"github.com/zzqDeco/teamie-werewolf/internal/game"
)

const (
	httpTestAppID  = "22222222-3333-4444-8555-666666666666"
	httpTestSecret = "tmsec_http_integration_test_secret_key"
	httpTestWSID   = "10000000-0000-4000-8000-000000000001"
	httpTestConvID = "20000000-0000-4000-8000-000000000002"
)

func makeTestToken(t *testing.T, secret, userID, name string) string {
	t.Helper()
	now := time.Now()
	tok, err := auth.SignToken(secret, auth.Claims{
		AppID:          httpTestAppID,
		Slug:           "werewolf",
		WorkspaceID:    httpTestWSID,
		ConversationID: httpTestConvID,
		UserID:         userID,
		DisplayName:    name,
		IssuedAt:       now.Unix(),
		ExpiresAt:      now.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	return tok
}

func TestWebUIProbeAndProductionDevGate(t *testing.T) {
	verifier, err := auth.NewVerifier(httpTestAppID, httpTestSecret)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	mgr := game.NewManager(game.DefaultTimings())
	srv := NewServer(Config{
		AppID:         httpTestAppID,
		SigningSecret: httpTestSecret,
		TeamieOrigin:  "https://120.25.210.202",
		Version:       "0.1.0",
		GitSHA:        "abc1234",
		DevSimulator:  false,
		StaticFS: fstest.MapFS{
			"index.html":                  &fstest.MapFile{Data: []byte("<!doctype html><html><body>ok</body></html>")},
			"assets/tarot-werewolf.webp": &fstest.MapFile{Data: []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")},
		},
	}, verifier, mgr)
	defer srv.Close()

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Stage 2: GET / must return 200 OK text/html without X-Frame-Options blocking and with CSP frame-ancestors
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / expected 200, got %d", resp.StatusCode)
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("GET / expected text/html, got %q", ct)
	}
	if xfo := resp.Header.Get("X-Frame-Options"); xfo != "" {
		t.Fatalf("GET / must not return X-Frame-Options, got %q", xfo)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors https://120.25.210.202") {
		t.Fatalf("expected CSP frame-ancestors https://120.25.210.202, got %q", csp)
	}
	if !strings.Contains(string(body), "<html") {
		t.Fatalf("expected HTML document on GET /, got %q", string(body))
	}

	// Verify .webp tarot artwork assets are served with Content-Type: image/webp (safe under nosniff)
	webpResp, err := http.Get(ts.URL + "/assets/tarot-werewolf.webp")
	if err != nil {
		t.Fatalf("GET .webp asset: %v", err)
	}
	_ = webpResp.Body.Close()
	if webpResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on .webp asset, got %d", webpResp.StatusCode)
	}
	if webpCT := strings.ToLower(webpResp.Header.Get("Content-Type")); !strings.Contains(webpCT, "image/webp") {
		t.Fatalf("expected image/webp Content-Type on .webp asset, got %q", webpCT)
	}

	// Health & version checks
	hResp, err := http.Get(ts.URL + "/healthz")
	if err != nil || hResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz failed: err=%v resp=%v", err, hResp)
	}
	_ = hResp.Body.Close()

	// DevSimulator=false MUST block /dev and /api/dev/tokens with 404
	devResp, _ := http.Get(ts.URL + "/dev")
	_ = devResp.Body.Close()
	if devResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on /dev when DevSimulator=false, got %d", devResp.StatusCode)
	}

	devTokResp, _ := http.Post(ts.URL+"/api/dev/tokens", "application/json", strings.NewReader(`{}`))
	_ = devTokResp.Body.Close()
	if devTokResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on /api/dev/tokens when DevSimulator=false, got %d", devTokResp.StatusCode)
	}

	// Missing / forged auth on /api/view -> 401
	unauthView, _ := http.Get(ts.URL + "/api/view")
	_ = unauthView.Body.Close()
	if unauthView.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unauthenticated /api/view, got %d", unauthView.StatusCode)
	}

	forgedTok := makeTestToken(t, "tmsec_forged_wrong_key", "u1", "Forged")
	reqForged, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/view", nil)
	reqForged.Header.Set("Authorization", "Bearer "+forgedTok)
	forgedResp, _ := http.DefaultClient.Do(reqForged)
	_ = forgedResp.Body.Close()
	if forgedResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 on forged /api/view, got %d", forgedResp.StatusCode)
	}
}

func TestHTTPAndWebSSEAntiCheatIntegration(t *testing.T) {
	verifier, err := auth.NewVerifier(httpTestAppID, httpTestSecret)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	mgr := game.NewManager(game.Timings{
		NightTimeout:       30 * time.Second,
		DayAnnounceDelay:   30 * time.Millisecond,
		HunterTimeout:      30 * time.Second,
		SpeakerTurnTimeout: 30 * time.Second,
		VoteTimeout:        30 * time.Second,
	})
	// Fixed 6p deck:
	// 1: WEREWOLF, 2: WEREWOLF, 3: VILLAGER, 4: VILLAGER, 5: SEER, 6: WITCH
	mgr.SetShuffleFunc(func(deck []game.Role) {
		copy(deck, []game.Role{
			game.RoleWerewolf,
			game.RoleWerewolf,
			game.RoleVillager,
			game.RoleVillager,
			game.RoleSeer,
			game.RoleWitch,
		})
	})

	srv := NewServer(Config{
		AppID:         httpTestAppID,
		SigningSecret: httpTestSecret,
		TeamieOrigin:  "https://120.25.210.202",
		DevSimulator:  true,
	}, verifier, mgr)
	defer srv.Close()

	ts := httptest.NewServer(srv)
	defer ts.Close()

	tokens := make([]string, 6)
	for i := 0; i < 6; i++ {
		uid := fmt.Sprintf("00000000-0000-4000-8000-00000000020%d", i+1)
		tokens[i] = makeTestToken(t, httpTestSecret, uid, fmt.Sprintf("Player-%d", i+1))
	}

	postAction := func(tok, path, body, idemKey string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		if idemKey != "" {
			req.Header.Set("Idempotency-Key", idemKey)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, string(raw)
	}

	getViewRaw := func(tok string) (game.PlayerView, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/view", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /api/view: %v", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/view status=%d body=%s", resp.StatusCode, string(raw))
		}
		var v game.PlayerView
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("decode PlayerView: %v", err)
		}
		return v, string(raw)
	}

	// Subscribe Seat 3 (Villager) to Web SSE /api/stream and record every pushed frame
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()

	sseReq, _ := http.NewRequestWithContext(sseCtx, http.MethodGet, ts.URL+"/api/stream", nil)
	sseReq.Header.Set("Authorization", "Bearer "+tokens[2]) // Seat 3 Villager
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("GET /api/stream: %v", err)
	}
	defer sseResp.Body.Close()

	villagerSSEFrames := make(chan string, 64)
	go func() {
		scanner := bufio.NewScanner(sseResp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				villagerSSEFrames <- strings.TrimSpace(after)
			}
		}
	}()

	// All 6 join and Seat 1 starts game
	for i := 0; i < 6; i++ {
		r, b := postAction(tokens[i], "/api/action/join_lobby", `{"preset":"6p"}`, "")
		if r.StatusCode != http.StatusOK {
			t.Fatalf("seat %d join failed: %d %s", i+1, r.StatusCode, b)
		}
	}
	rStart, bStart := postAction(tokens[0], "/api/action/start_game", `{"preset":"6p"}`, "")
	if rStart.StatusCode != http.StatusOK {
		t.Fatalf("start_game failed: %d %s", rStart.StatusCode, bStart)
	}

	// Wolf 1 sends secret wolf_chat (with Idempotency-Key replay check)
	rChat1, _ := postAction(tokens[0], "/api/action/wolf_chat", `{"message":"暗语测试：先刀3号平民"}`, "http-idem-1")
	rChat2, bChat2 := postAction(tokens[0], "/api/action/wolf_chat", `{"message":"暗语测试：先刀3号平民"}`, "http-idem-1")
	if rChat1.StatusCode != http.StatusOK || rChat2.StatusCode != http.StatusOK || !strings.Contains(bChat2, `"idempotentReplay":true`) {
		t.Fatalf("expected HTTP idempotent replay on wolf_chat, got %s", bChat2)
	}

	// Villager attempts wolf_chat -> MUST return 403 Forbidden
	rForbidden, _ := postAction(tokens[2], "/api/action/wolf_chat", `{"message":"我是平民偷看"}`, "")
	if rForbidden.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 when villager calls wolf_chat, got %d", rForbidden.StatusCode)
	}

	// Wolves kill Seat 3, Seer checks Seat 1, Witch saves Seat 3
	postAction(tokens[0], "/api/action/cast_night_action", `{"actionType":"kill","targetSeat":3}`, "")
	postAction(tokens[1], "/api/action/cast_night_action", `{"actionType":"kill","targetSeat":3}`, "")
	postAction(tokens[4], "/api/action/cast_night_action", `{"actionType":"check","targetSeat":1}`, "")

	// Before Witch acts, inspect Villager (Seat 3) & Seer (Seat 5) HTTP views:
	// Neither can see wolfChat, wolfTeammateSeats, witchState, antidoteLeft, poisonLeft!
	for _, idx := range []int{2, 4} {
		_, rawJSON := getViewRaw(tokens[idx])
		for _, forbidden := range []string{"暗语测试", "wolfChat", "wolfTeammateSeats", "witchState", "antidoteLeft", "poisonLeft"} {
			if strings.Contains(rawJSON, forbidden) {
				t.Fatalf("FOG LEAK in /api/view for player %d (matched %q): %s", idx+1, forbidden, rawJSON)
			}
		}
	}

	// Witch saves Seat 3 -> transitions to DAY_ANNOUNCE -> auto-transitions (30ms) to DAY_SPEECH
	rWitch, bWitch := postAction(tokens[5], "/api/action/cast_night_action", `{"actionType":"save"}`, "")
	if rWitch.StatusCode != http.StatusOK {
		t.Fatalf("witch save failed: %d %s", rWitch.StatusCode, bWitch)
	}

	// Wait for DAY_SPEECH
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		v, _ := getViewRaw(tokens[0])
		if v.Phase == game.PhaseDaySpeech {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	// Complete all 6 speeches in strict single-speaker order
	for step := 0; step < 6; step++ {
		v0, _ := getViewRaw(tokens[0])
		if v0.Phase != game.PhaseDaySpeech || v0.Speech == nil {
			t.Fatalf("step %d expected DAY_SPEECH, got %s", step, v0.Phase)
		}
		curSeat := v0.Speech.CurrentSpeakerSeat
		rSpk, bSpk := postAction(tokens[curSeat-1], "/api/action/finish_speech", `{"summary":"过麦"}`, "")
		if rSpk.StatusCode != http.StatusOK {
			t.Fatalf("seat %d finish_speech failed: %d %s", curSeat, rSpk.StatusCode, bSpk)
		}
	}

	// Now in DAY_VOTE: Seat 1 votes Seat 5, Seat 2 votes Seat 5.
	// Before all 6 vote, Seat 3 (Villager) MUST NOT see who Seat 1 or Seat 2 voted for!
	postAction(tokens[0], "/api/action/submit_vote", `{"targetSeat":5}`, "")
	postAction(tokens[1], "/api/action/submit_vote", `{"targetSeat":5}`, "")

	vVillagerVote, rawVillagerVote := getViewRaw(tokens[2])
	if vVillagerVote.Phase != game.PhaseDayVote {
		t.Fatalf("expected DAY_VOTE, got %s", vVillagerVote.Phase)
	}
	if len(vVillagerVote.VoteHistory) != 0 || strings.Contains(rawVillagerVote, "votesBySeat") {
		t.Fatalf("SECRET BALLOT LEAK before tally: %s", rawVillagerVote)
	}

	// Drain all Villager Web SSE frames accumulated so far and assert zero leaks!
	sseCancel()
	time.Sleep(20 * time.Millisecond)
drainLoop:
	for {
		select {
		case frame := <-villagerSSEFrames:
			for _, secretField := range []string{
				"暗语测试", "wolfChat", "wolfTeammateSeats",
				"seerChecks", "witchState", "antidoteLeft", "poisonLeft",
				`"knownRole":"WEREWOLF"`, `"knownRole":"SEER"`, `"knownRole":"WITCH"`,
				"votesBySeat",
			} {
				if strings.Contains(frame, secretField) {
					t.Fatalf("CRITICAL FOG LEAK on Web SSE frame for Villager (matched %q): %s", secretField, frame)
				}
			}
		default:
			break drainLoop
		}
	}

	// Also verify DevSimulator /api/dev/tokens works when DevSimulator=true
	devResp, devBody := postAction("", "/api/dev/tokens", `{"preset":"6p","quickAction":"start_game","conversationId":"dev-conv-2"}`, "")
	if devResp.StatusCode != http.StatusOK || !strings.Contains(devBody, `"players"`) {
		t.Fatalf("expected /api/dev/tokens 200 OK in DevSimulator=true, got %d %s", devResp.StatusCode, devBody)
	}
}
