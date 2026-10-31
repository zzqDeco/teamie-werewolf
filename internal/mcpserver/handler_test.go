package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/zzqDeco/teamie-werewolf/internal/auth"
	"github.com/zzqDeco/teamie-werewolf/internal/game"
)

const (
	testAppID  = "11111111-2222-4333-8444-555555555555"
	testSecret = "tmsec_unit_test_secret_key_for_werewolf_mcp"
	testWSID   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	testConvID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

type bearerRoundTripper struct {
	base    http.RoundTripper
	token   string
	idemKey string
}

func (rt *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if rt.token != "" && clone.Header.Get("Authorization") == "" {
		clone.Header.Set("Authorization", "Bearer "+rt.token)
	}
	if rt.idemKey != "" && clone.Header.Get("Idempotency-Key") == "" {
		clone.Header.Set("Idempotency-Key", rt.idemKey)
	}
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

func mintToken(t *testing.T, secret, userID, displayName string, expOffset time.Duration) string {
	t.Helper()
	now := time.Now()
	tok, err := auth.SignToken(secret, auth.Claims{
		AppID:          testAppID,
		Slug:           "werewolf",
		WorkspaceID:    testWSID,
		ConversationID: testConvID,
		UserID:         userID,
		DisplayName:    displayName,
		IssuedAt:       now.Unix(),
		ExpiresAt:      now.Add(expOffset).Unix(),
	})
	if err != nil {
		t.Fatalf("SignToken failed: %v", err)
	}
	return tok
}

func setupMCPTestServer(t *testing.T) (*httptest.Server, *game.Manager, func()) {
	t.Helper()
	verifier, err := auth.NewVerifier(testAppID, testSecret)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	mgr := game.NewManager(game.Timings{
		NightTimeout:       30 * time.Second,
		DayAnnounceDelay:   40 * time.Millisecond,
		HunterTimeout:      30 * time.Second,
		SpeakerTurnTimeout: 30 * time.Second,
		VoteTimeout:        30 * time.Second,
	})
	h := NewHandler("0.1.0-test", verifier, mgr)
	mgr.SetCallbacks(h.EnqueueNotifications, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", h)
	srv := httptest.NewServer(mux)
	cleanup := func() {
		srv.Close()
		h.Close()
		mgr.CloseAll()
	}
	return srv, mgr, cleanup
}

func TestMCPAuthAndFourStageHandshakeProbe(t *testing.T) {
	srv, mgr, cleanup := setupMCPTestServer(t)
	defer cleanup()

	initPayload := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"teamie-marketplace-verifier","version":"1.0.0"}}}`)

	// Stage 3a: Missing Authorization header -> MUST return HTTP 401 (never HTTP 200)
	reqNoAuth, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", bytes.NewReader(initPayload))
	reqNoAuth.Header.Set("Content-Type", "application/json")
	respNoAuth, err := http.DefaultClient.Do(reqNoAuth)
	if err != nil {
		t.Fatalf("no-auth request error: %v", err)
	}
	_ = respNoAuth.Body.Close()
	if respNoAuth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without Authorization header, got %d", respNoAuth.StatusCode)
	}

	// Stage 3b: Forged HS256 signature -> MUST return HTTP 401
	forgedTok := mintToken(t, "tmsec_wrong_forged_secret", "00000000-0000-4000-8000-000000000001", "Forged", 5*time.Minute)
	reqForged, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", bytes.NewReader(initPayload))
	reqForged.Header.Set("Content-Type", "application/json")
	reqForged.Header.Set("Authorization", "Bearer "+forgedTok)
	respForged, err := http.DefaultClient.Do(reqForged)
	if err != nil {
		t.Fatalf("forged request error: %v", err)
	}
	_ = respForged.Body.Close()
	if respForged.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for forged token, got %d", respForged.StatusCode)
	}

	// Stage 3c: Expired token -> MUST return HTTP 401
	expiredTok := mintToken(t, testSecret, "00000000-0000-4000-8000-000000000001", "Expired", -2*time.Minute)
	reqExp, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", bytes.NewReader(initPayload))
	reqExp.Header.Set("Content-Type", "application/json")
	reqExp.Header.Set("Authorization", "Bearer "+expiredTok)
	respExp, err := http.DefaultClient.Do(reqExp)
	if err != nil {
		t.Fatalf("expired request error: %v", err)
	}
	_ = respExp.Body.Close()
	if respExp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired token, got %d", respExp.StatusCode)
	}

	// Stage 4: Valid probe token handshake -> initialize -> notifications/initialized -> tools/list -> get_my_view -> DELETE
	probeTok := mintToken(t, testSecret, "00000000-0000-4000-8000-000000000001", "Teamie Protocol Verifier", 5*time.Minute)

	doRPC := func(method string, body string, sid string) *http.Response {
		t.Helper()
		var reader *bytes.Reader
		if body != "" {
			reader = bytes.NewReader([]byte(body))
		} else {
			reader = bytes.NewReader(nil)
		}
		req, err := http.NewRequest(method, srv.URL+"/mcp", reader)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+probeTok)
		if sid != "" {
			req.Header.Set("Mcp-Session-Id", sid)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("doRPC %s failed: %v", method, err)
		}
		return resp
	}

	initResp := doRPC(http.MethodPost, string(initPayload), "")
	defer initResp.Body.Close()
	if initResp.StatusCode != http.StatusOK {
		t.Fatalf("initialize expected 200, got %d", initResp.StatusCode)
	}
	sid := strings.TrimSpace(initResp.Header.Get("Mcp-Session-Id"))
	if sid == "" {
		t.Fatalf("expected non-empty Mcp-Session-Id header on initialize")
	}

	notifResp := doRPC(http.MethodPost, `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`, sid)
	_ = notifResp.Body.Close()
	if notifResp.StatusCode < 200 || notifResp.StatusCode >= 300 {
		t.Fatalf("notifications/initialized expected 2xx, got %d", notifResp.StatusCode)
	}

	listResp := doRPC(http.MethodPost, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, sid)
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("tools/list expected 200, got %d", listResp.StatusCode)
	}
	var listEnv struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listEnv); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	if len(listEnv.Result.Tools) != 9 {
		t.Fatalf("expected 9 orthogonal tools, got %d", len(listEnv.Result.Tools))
	}
	for _, tool := range listEnv.Result.Tools {
		mountedName := "mcp__werewolf__" + tool.Name
		if len(mountedName) > 64 {
			t.Fatalf("mounted tool name %q exceeds 64-char limit (%d)", mountedName, len(mountedName))
		}
		if len(tool.Name) > 32 {
			t.Fatalf("tool name %q exceeds 32-char limit", tool.Name)
		}
		if len(tool.InputSchema) > 4096 {
			t.Fatalf("tool %q inputSchema exceeds 4KB limit (%d bytes)", tool.Name, len(tool.InputSchema))
		}
	}

	// Read-only get_my_view MUST NOT allocate any room in memory
	viewResp := doRPC(http.MethodPost, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_my_view","arguments":{}}}`, sid)
	_ = viewResp.Body.Close()
	if viewResp.StatusCode != http.StatusOK {
		t.Fatalf("get_my_view expected 200, got %d", viewResp.StatusCode)
	}
	if mgr.RoomCount() != 0 {
		t.Fatalf("probe handshake + get_my_view created dirty room! RoomCount=%d", mgr.RoomCount())
	}

	// Cross-user Mcp-Session-Id hijack attempt -> MUST return 403
	attackerTok := mintToken(t, testSecret, "99999999-9999-4999-8999-999999999999", "Attacker", 5*time.Minute)
	hijackReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}`)))
	hijackReq.Header.Set("Content-Type", "application/json")
	hijackReq.Header.Set("Authorization", "Bearer "+attackerTok)
	hijackReq.Header.Set("Mcp-Session-Id", sid)
	hijackResp, err := http.DefaultClient.Do(hijackReq)
	if err != nil {
		t.Fatalf("hijack request: %v", err)
	}
	_ = hijackResp.Body.Close()
	if hijackResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 on cross-user Mcp-Session-Id hijack, got %d", hijackResp.StatusCode)
	}

	// DELETE session -> 200 OK
	delResp := doRPC(http.MethodDelete, "", sid)
	_ = delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE expected 200, got %d", delResp.StatusCode)
	}

	// Stale/unknown Mcp-Session-Id (after DELETE or server restart) -> MUST return HTTP 404 on both POST and GET
	stalePost := doRPC(http.MethodPost, `{"jsonrpc":"2.0","id":5,"method":"tools/list","params":{}}`, "ww-mcp-restarted-nonexistent-session")
	_ = stalePost.Body.Close()
	if stalePost.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on stale POST Mcp-Session-Id, got %d", stalePost.StatusCode)
	}

	staleGet := doRPC(http.MethodGet, "", "ww-mcp-restarted-nonexistent-session")
	_ = staleGet.Body.Close()
	if staleGet.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on stale GET Mcp-Session-Id, got %d", staleGet.StatusCode)
	}
}

func TestMCPContinuousSSEFogOfWarAndIdempotency(t *testing.T) {
	srv, mgr, cleanup := setupMCPTestServer(t)
	defer cleanup()

	// Deterministic 6p deck:
	// Seat 1: WEREWOLF, Seat 2: WEREWOLF, Seat 3: VILLAGER, Seat 4: VILLAGER, Seat 5: SEER, Seat 6: WITCH
	mgr.SetShuffleFunc(func(deck []game.Role) {
		fixed := []game.Role{
			game.RoleWerewolf,
			game.RoleWerewolf,
			game.RoleVillager,
			game.RoleVillager,
			game.RoleSeer,
			game.RoleWitch,
		}
		copy(deck, fixed)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type connectedPlayer struct {
		userID string
		name   string
		cli    *client.Client
		mu     sync.Mutex
		notifs []mcp.JSONRPCNotification
	}

	players := make([]*connectedPlayer, 6)
	for i := 0; i < 6; i++ {
		uid := "00000000-0000-4000-8000-00000000010" + string(rune('1'+i))
		name := "Agent-" + string(rune('1'+i))
		tok := mintToken(t, testSecret, uid, name, 30*time.Minute)

		httpCli := &http.Client{
			Transport: &bearerRoundTripper{token: tok},
		}
		tr, err := transport.NewStreamableHTTP(
			srv.URL+"/mcp",
			transport.WithHTTPBasicClient(httpCli),
			transport.WithContinuousListening(),
		)
		if err != nil {
			t.Fatalf("NewStreamableHTTP player %d: %v", i+1, err)
		}
		c := client.NewClient(tr)
		cp := &connectedPlayer{
			userID: uid,
			name:   name,
			cli:    c,
		}
		c.OnNotification(func(n mcp.JSONRPCNotification) {
			cp.mu.Lock()
			cp.notifs = append(cp.notifs, n)
			cp.mu.Unlock()
		})
		if err := c.Start(ctx); err != nil {
			t.Fatalf("Start client %d: %v", i+1, err)
		}
		if _, err := c.Initialize(ctx, mcp.InitializeRequest{
			Params: mcp.InitializeParams{
				ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
				ClientInfo:      mcp.Implementation{Name: "test-agent", Version: "1.0.0"},
			},
		}); err != nil {
			t.Fatalf("Initialize client %d: %v", i+1, err)
		}
		players[i] = cp
		defer func(cl *client.Client) { _ = cl.Close() }(c)
	}

	// Give GET SSE continuous listener goroutines a moment to register
	time.Sleep(40 * time.Millisecond)

	// Seat 1..6 join_lobby; verify forged userId/conversationId in arguments are ignored!
	for i, p := range players {
		res, err := p.cli.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name: "join_lobby",
				Arguments: map[string]any{
					"userId":         "spoofed-evil-user-id",
					"conversationId": "spoofed-evil-conv-id",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("player %d join_lobby failed: err=%v res=%+v", i+1, err, res)
		}
	}

	// Seat 1 starts game
	startRes, err := players[0].cli.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "start_game",
			Arguments: map[string]any{"preset": "6p"},
		},
	})
	if err != nil || startRes.IsError {
		t.Fatalf("start_game failed: err=%v res=%+v", err, startRes)
	}

	// Test Idempotency-Key on wolf_chat using raw HTTP with same session/token for Seat 1:
	wolf1Tok := mintToken(t, testSecret, players[0].userID, players[0].name, 30*time.Minute)
	idemHTTPCli := &http.Client{
		Transport: &bearerRoundTripper{token: wolf1Tok, idemKey: "idem-wolf-chat-001"},
	}
	idemTr, err := transport.NewStreamableHTTP(srv.URL+"/mcp", transport.WithHTTPBasicClient(idemHTTPCli))
	if err != nil {
		t.Fatalf("idem transport: %v", err)
	}
	idemCli := client.NewClient(idemTr)
	if err := idemCli.Start(ctx); err != nil {
		t.Fatalf("idem start: %v", err)
	}
	defer func() { _ = idemCli.Close() }()
	if _, err := idemCli.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "idem-agent", Version: "1.0.0"},
		},
	}); err != nil {
		t.Fatalf("idem init: %v", err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		chatRes, err := idemCli.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name:      "wolf_chat",
				Arguments: map[string]any{"message": "今晚集火刀 3 号平民，预言家留给白天抗推"},
			},
		})
		if err != nil || chatRes.IsError {
			t.Fatalf("wolf_chat attempt %d failed: err=%v res=%+v", attempt, err, chatRes)
		}
		var ar game.ActionResponse
		txt := chatRes.Content[0].(mcp.TextContent).Text
		if err := json.Unmarshal([]byte(txt), &ar); err != nil {
			t.Fatalf("unmarshal ActionResponse: %v", err)
		}
		if attempt == 2 && !ar.IdempotentReplay {
			t.Fatalf("expected attempt 2 with identical Idempotency-Key to set idempotentReplay=true")
		}
		if len(ar.View.Private.WolfChat) != 1 {
			t.Fatalf("expected exactly 1 wolf_chat entry after idempotent replay, got %d", len(ar.View.Private.WolfChat))
		}
	}

	// Wait for SSE notifications to arrive
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		players[1].mu.Lock()
		n2 := len(players[1].notifs)
		players[1].mu.Unlock()
		if n2 >= 2 { // night_started + wolf_chat
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Verify Seat 2 (Werewolf teammate) received both night_started and wolf_chat notifications (< 2KB each)
	players[1].mu.Lock()
	wolf2Notifs := append([]mcp.JSONRPCNotification(nil), players[1].notifs...)
	players[1].mu.Unlock()

	sawWolfChatOnWolf2 := false
	for _, n := range wolf2Notifs {
		raw, _ := json.Marshal(n)
		if len(raw) >= MaxNotificationBytes {
			t.Fatalf("SSE notification exceeded 2KB limit (%d bytes): %s", len(raw), string(raw))
		}
		if n.Params.AdditionalFields["eventType"] == "wolf_chat_message" {
			sawWolfChatOnWolf2 = true
		}
	}
	if !sawWolfChatOnWolf2 {
		t.Fatalf("expected Seat 2 (Werewolf teammate) to receive wolf_chat SSE notification, got %+v", wolf2Notifs)
	}

	// Anti-cheat assertion: Non-wolves (Seat 3 Villager, Seat 4 Villager, Seat 5 Seer, Seat 6 Witch)
	// MUST NEVER receive wolf_chat or any WEREWOLF role hint over MCP SSE OR get_my_view!
	for _, seatIdx := range []int{2, 3, 4, 5} {
		p := players[seatIdx]
		p.mu.Lock()
		ns := append([]mcp.JSONRPCNotification(nil), p.notifs...)
		p.mu.Unlock()
		for _, n := range ns {
			raw, _ := json.Marshal(n)
			s := string(raw)
			if strings.Contains(s, "wolf_chat_message") || strings.Contains(s, "集火刀") || strings.Contains(s, "WEREWOLF") {
				t.Fatalf("CRITICAL FOG LEAK on SSE to seat %d: %s", seatIdx+1, s)
			}
		}

		viewRes, err := p.cli.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: "get_my_view"},
		})
		if err != nil || viewRes.IsError {
			t.Fatalf("seat %d get_my_view failed: %v", seatIdx+1, err)
		}
		viewJSON := viewRes.Content[0].(mcp.TextContent).Text
		if strings.Contains(viewJSON, "集火刀") || strings.Contains(viewJSON, "wolfChat") || strings.Contains(viewJSON, "wolfTeammateSeats") {
			t.Fatalf("CRITICAL FOG LEAK in get_my_view for seat %d: %s", seatIdx+1, viewJSON)
		}
	}
}

func TestMCPBusinessRejectionsReturnStructuredJSONWithoutIsError(t *testing.T) {
	srv, mgr, cleanup := setupMCPTestServer(t)
	t.Cleanup(cleanup)

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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	makeClient := func(i int) *client.Client {
		t.Helper()
		uid := "00000000-0000-4000-8000-00000000030" + string(rune('1'+i))
		tok := mintToken(t, testSecret, uid, "Bot-"+string(rune('1'+i)), 30*time.Minute)
		tr, err := transport.NewStreamableHTTP(
			srv.URL+"/mcp",
			transport.WithHTTPBasicClient(&http.Client{Transport: &bearerRoundTripper{token: tok}}),
		)
		if err != nil {
			t.Fatalf("NewStreamableHTTP: %v", err)
		}
		c := client.NewClient(tr)
		if err := c.Start(ctx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if _, err := c.Initialize(ctx, mcp.InitializeRequest{
			Params: mcp.InitializeParams{
				ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
				ClientInfo:      mcp.Implementation{Name: "test-bot", Version: "1.0.0"},
			},
		}); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	bots := make([]*client.Client, 6)
	for i := 0; i < 6; i++ {
		bots[i] = makeClient(i)
		res, err := bots[i].CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: "join_lobby"},
		})
		if err != nil || res.IsError {
			t.Fatalf("join_lobby %d failed: %v %+v", i+1, err, res)
		}
	}

	// Bot 1 starts game -> NIGHT
	if res, err := bots[0].CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: "start_game"},
	}); err != nil || res.IsError {
		t.Fatalf("start_game failed: %v %+v", err, res)
	}

	assertStructuredRejection := func(cli *client.Client, tool string, args map[string]any, wantCode string) game.ActionResponse {
		t.Helper()
		res, err := cli.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: tool, Arguments: args},
		})
		if err != nil {
			t.Fatalf("%s returned transport err: %v", tool, err)
		}
		if res.IsError {
			t.Fatalf("%s set IsError=true (would be stripped into opaque mcp_tool_error by einomcp!): %+v", tool, res)
		}
		var ar game.ActionResponse
		txt := res.Content[0].(mcp.TextContent).Text
		if err := json.Unmarshal([]byte(txt), &ar); err != nil {
			t.Fatalf("%s invalid ActionResponse JSON: %v (%s)", tool, err, txt)
		}
		if ar.OK {
			t.Fatalf("%s expected ok=false, got ok=true: %+v", tool, ar)
		}
		if ar.ErrorCode != wantCode {
			t.Fatalf("%s expected errorCode=%q, got %q (msg=%q)", tool, wantCode, ar.ErrorCode, ar.Message)
		}
		return ar
	}

	// 1. Duplicate start_game by Bot 2 right after game started -> game_already_started + cropped Villager/Wolf view
	dupStart := assertStructuredRejection(bots[2], "start_game", nil, "game_already_started")
	if dupStart.View.Phase != game.PhaseNight || dupStart.View.Me.Role != game.RoleVillager {
		t.Fatalf("expected Villager NIGHT view on rejected start_game, got %+v", dupStart.View)
	}
	// Ensure no Fog-of-War leak inside error response view!
	rawDup, _ := json.Marshal(dupStart)
	if strings.Contains(string(rawDup), "WEREWOLF") || strings.Contains(string(rawDup), "wolfTeammateSeats") {
		t.Fatalf("CRITICAL FOG LEAK in rejected ActionResponse: %s", string(rawDup))
	}

	// 2. leave_lobby during NIGHT -> game_in_progress
	assertStructuredRejection(bots[2], "leave_lobby", nil, "game_in_progress")

	// 3. Villager calling cast_night_action -> forbidden_role
	assertStructuredRejection(bots[2], "cast_night_action", map[string]any{"actionType": "check", "targetSeat": 1}, "forbidden_role")

	// 4. Missing targetSeat on submit_vote -> missing_target_seat
	assertStructuredRejection(bots[2], "submit_vote", map[string]any{}, "missing_target_seat")
}
