package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zzqDeco/teamie-werewolf/internal/auth"
	"github.com/zzqDeco/teamie-werewolf/internal/game"
	"github.com/zzqDeco/teamie-werewolf/internal/mcpserver"
)

func init() {
	_ = mime.AddExtensionType(".webp", "image/webp")
}

// Config configures the unified Werewolf HTTP + MCP + embedded Web UI server.
type Config struct {
	AppID         string
	SigningSecret string // Required when DevSimulator=true for minting local simulator tokens
	TeamieOrigin  string // Allowed parent origin for CSP frame-ancestors (e.g. https://120.25.210.202)
	Version       string
	GitSHA        string
	DevSimulator  bool
	StaticFS      fs.FS // Optional embedded Vite dist filesystem; falls back to built-in minimal HTML in unit tests
}

type webSubscriber struct {
	id     string
	claims auth.Claims
	wakeCh chan struct{}
}

// Server serves the Web UI SPA, REST API, Web SSE stream, and MCP Streamable HTTP endpoint.
type Server struct {
	cfg        Config
	verifier   *auth.Verifier
	gameMgr    *game.Manager
	mcpHandler *mcpserver.Handler
	mux        *http.ServeMux

	subMu sync.RWMutex
	subs  map[game.RoomKey]map[string]*webSubscriber
}

// NewServer wires the game state machine, MCP server, Web SSE hub, and HTTP routes.
func NewServer(cfg Config, verifier *auth.Verifier, gameMgr *game.Manager) *Server {
	if strings.TrimSpace(cfg.Version) == "" {
		cfg.Version = "0.1.0"
	}
	if strings.TrimSpace(cfg.GitSHA) == "" {
		cfg.GitSHA = "dev"
	}

	mcpH := mcpserver.NewHandler(cfg.Version, verifier, gameMgr)
	s := &Server{
		cfg:        cfg,
		verifier:   verifier,
		gameMgr:    gameMgr,
		mcpHandler: mcpH,
		mux:        http.NewServeMux(),
		subs:       make(map[game.RoomKey]map[string]*webSubscriber),
	}

	gameMgr.SetCallbacks(mcpH.EnqueueNotifications, s.NotifyRoomUpdated)
	s.registerRoutes()
	return s
}

// Close shuts down background MCP notification dispatchers and room timers.
func (s *Server) Close() {
	s.mcpHandler.Close()
	s.gameMgr.CloseAll()
}

// ServeHTTP applies framing/CORS headers and dispatches HTTP requests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.applyCommonHeaders(w, r)
	if r.Method == http.MethodOptions && !strings.HasPrefix(r.URL.Path, "/mcp") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// NotifyRoomUpdated wakes all Web UI SSE subscribers in (workspaceID, conversationID)
// so each stream pushes its freshly Fog-of-War-cropped PlayerView.
func (s *Server) NotifyRoomUpdated(workspaceID, conversationID string) {
	key := game.RoomKey{WorkspaceID: workspaceID, ConversationID: conversationID}
	s.subMu.RLock()
	roomSubs := s.subs[key]
	targets := make([]*webSubscriber, 0, len(roomSubs))
	for _, sub := range roomSubs {
		targets = append(targets, sub)
	}
	s.subMu.RUnlock()

	for _, sub := range targets {
		select {
		case sub.wakeCh <- struct{}{}:
		default:
		}
	}
}

func (s *Server) addSubscriber(claims auth.Claims) (*webSubscriber, func()) {
	key := game.RoomKey{WorkspaceID: claims.WorkspaceID, ConversationID: claims.ConversationID}
	sub := &webSubscriber{
		id:     uuid.NewString(),
		claims: claims,
		wakeCh: make(chan struct{}, 1),
	}
	s.subMu.Lock()
	m, ok := s.subs[key]
	if !ok {
		m = make(map[string]*webSubscriber)
		s.subs[key] = m
	}
	m[sub.id] = sub
	s.subMu.Unlock()

	remove := func() {
		s.subMu.Lock()
		defer s.subMu.Unlock()
		if roomSubs, exists := s.subs[key]; exists {
			delete(roomSubs, sub.id)
			if len(roomSubs) == 0 {
				delete(s.subs, key)
			}
		}
	}
	return sub, remove
}

func (s *Server) applyCommonHeaders(w http.ResponseWriter, r *http.Request) {
	// Never emit X-Frame-Options: DENY or SAMEORIGIN; control embedding strictly via CSP frame-ancestors.
	w.Header().Del("X-Frame-Options")

	origin := strings.TrimSpace(s.cfg.TeamieOrigin)
	if origin != "" {
		if s.cfg.DevSimulator {
			w.Header().Set("Content-Security-Policy", fmt.Sprintf("frame-ancestors 'self' %s", origin))
		} else {
			w.Header().Set("Content-Security-Policy", fmt.Sprintf("frame-ancestors %s", origin))
		}
	} else if s.cfg.DevSimulator {
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'self' *")
	}

	reqOrigin := strings.TrimSpace(r.Header.Get("Origin"))
	if reqOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", reqOrigin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, Mcp-Session-Id, MCP-Protocol-Version")
		w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id, MCP-Protocol-Version")
	}
}

func (s *Server) registerRoutes() {
	// Health & metadata probes
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	s.mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"name":    "teamie-werewolf",
			"slug":    "werewolf",
			"version": s.cfg.Version,
			"sha":     s.cfg.GitSHA,
		})
	})

	// MCP Streamable HTTP (2025-03-26)
	s.mux.Handle("/mcp", s.mcpHandler)
	s.mux.Handle("/mcp/", s.mcpHandler)

	// Authenticated Human Web UI API (uses identical symmetric HS256 Context Token)
	s.mux.HandleFunc("GET /api/view", s.withAuth(s.handleGetView))
	s.mux.HandleFunc("GET /api/stream", s.withAuth(s.handleStream))
	s.mux.HandleFunc("POST /api/action/join_lobby", s.withAuth(s.handleJoinLobby))
	s.mux.HandleFunc("POST /api/action/leave_lobby", s.withAuth(s.handleLeaveLobby))
	s.mux.HandleFunc("POST /api/action/configure_lobby", s.withAuth(s.handleConfigureLobby))
	s.mux.HandleFunc("POST /api/action/start_game", s.withAuth(s.handleStartGame))
	s.mux.HandleFunc("POST /api/action/wolf_chat", s.withAuth(s.handleWolfChat))
	s.mux.HandleFunc("POST /api/action/cast_night_action", s.withAuth(s.handleCastNightAction))
	s.mux.HandleFunc("POST /api/action/finish_speech", s.withAuth(s.handleFinishSpeech))
	s.mux.HandleFunc("POST /api/action/submit_vote", s.withAuth(s.handleSubmitVote))
	s.mux.HandleFunc("POST /api/action/hunter_shoot", s.withAuth(s.handleHunterShoot))

	// Local multi-player simulator endpoints (strictly disabled unless DEV_SIMULATOR=true)
	s.mux.HandleFunc("POST /api/dev/tokens", s.handleDevTokens)
	s.mux.HandleFunc("GET /dev", s.handleDevPage)
	s.mux.HandleFunc("GET /dev/", s.handleDevPage)

	// Embedded React SPA & static assets (GET / returns 200 OK text/html for Stage 2 web_ui probe)
	s.mux.HandleFunc("/", s.handleSPA)
}

func (s *Server) withAuth(next func(http.ResponseWriter, *http.Request, auth.Claims)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, err := s.verifier.VerifyAuthorizationHeader(r.Header.Get("Authorization"))
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error":   "unauthorized",
				"message": err.Error(),
			})
			return
		}
		next(w, r, *claims)
	}
}

func (s *Server) handleGetView(w http.ResponseWriter, _ *http.Request, claims auth.Claims) {
	view := s.gameMgr.GetView(claims)
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming_unsupported"})
		return
	}

	sub, remove := s.addSubscriber(claims)
	defer remove()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	sendSnapshot := func() error {
		view := s.gameMgr.GetView(claims)
		raw, err := json.Marshal(view)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: view\ndata: %s\n\n", raw); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	if err := sendSnapshot(); err != nil {
		return
	}

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.wakeCh:
			if err := sendSnapshot(); err != nil {
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleJoinLobby(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var body struct {
		Preset string `json:"preset"`
	}
	_ = decodeOptionalJSON(r, &body)
	room := s.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
	resp, err := room.JoinLobby(claims, body.Preset, idemKeyHeader(r))
	writeActionResult(w, resp, err)
}

func (s *Server) handleLeaveLobby(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	room := s.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
	resp, err := room.LeaveLobby(claims, idemKeyHeader(r))
	writeActionResult(w, resp, err)
}

func (s *Server) handleConfigureLobby(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var body struct {
		Preset          string `json:"preset"`
		PhaseTimeoutSec int    `json:"phaseTimeoutSec"`
	}
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": err.Error()})
		return
	}
	room := s.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
	resp, err := room.ConfigureLobby(claims, body.Preset, body.PhaseTimeoutSec, idemKeyHeader(r))
	writeActionResult(w, resp, err)
}

func (s *Server) handleStartGame(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var body struct {
		Preset          string `json:"preset"`
		PhaseTimeoutSec int    `json:"phaseTimeoutSec"`
	}
	_ = decodeOptionalJSON(r, &body)
	room := s.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
	resp, err := room.StartGame(claims, body.Preset, body.PhaseTimeoutSec, idemKeyHeader(r))
	writeActionResult(w, resp, err)
}

func (s *Server) handleWolfChat(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var body struct {
		Message string `json:"message"`
	}
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": err.Error()})
		return
	}
	room := s.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
	resp, err := room.WolfChat(claims, body.Message, idemKeyHeader(r))
	writeActionResult(w, resp, err)
}

func (s *Server) handleCastNightAction(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var body struct {
		ActionType string `json:"actionType"`
		TargetSeat int    `json:"targetSeat"`
	}
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": err.Error()})
		return
	}
	room := s.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
	resp, err := room.CastNightAction(claims, body.ActionType, body.TargetSeat, idemKeyHeader(r))
	writeActionResult(w, resp, err)
}

func (s *Server) handleFinishSpeech(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var body struct {
		Summary string `json:"summary"`
	}
	_ = decodeOptionalJSON(r, &body)
	room := s.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
	resp, err := room.FinishSpeech(claims, body.Summary, idemKeyHeader(r))
	writeActionResult(w, resp, err)
}

func (s *Server) handleSubmitVote(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var body struct {
		TargetSeat *int `json:"targetSeat"`
	}
	if err := decodeOptionalJSON(r, &body); err != nil || body.TargetSeat == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": "targetSeat is required"})
		return
	}
	room := s.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
	resp, err := room.SubmitVote(claims, *body.TargetSeat, idemKeyHeader(r))
	writeActionResult(w, resp, err)
}

func (s *Server) handleHunterShoot(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var body struct {
		TargetSeat *int `json:"targetSeat"`
	}
	if err := decodeOptionalJSON(r, &body); err != nil || body.TargetSeat == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": "targetSeat is required"})
		return
	}
	room := s.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
	resp, err := room.HunterShoot(claims, *body.TargetSeat, idemKeyHeader(r))
	writeActionResult(w, resp, err)
}

func (s *Server) handleDevPage(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.DevSimulator {
		http.NotFound(w, r)
		return
	}
	s.serveIndexHTML(w)
}

type devPlayerSession struct {
	Slot        int    `json:"slot"`
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	Token       string `json:"token"`
	ExpiresAt   string `json:"expiresAt"`
}

func (s *Server) handleDevTokens(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.DevSimulator {
		http.NotFound(w, r)
		return
	}
	var body struct {
		WorkspaceID    string `json:"workspaceId"`
		ConversationID string `json:"conversationId"`
		Preset         string `json:"preset"`
		QuickAction    string `json:"quickAction"` // "" | "seat_all" | "start_game"
	}
	_ = decodeOptionalJSON(r, &body)

	wsID := strings.TrimSpace(body.WorkspaceID)
	if wsID == "" {
		wsID = "00000000-0000-4000-8000-devworkspace01"
	}
	convID := strings.TrimSpace(body.ConversationID)
	if convID == "" {
		convID = "00000000-0000-4000-8000-devconvroom001"
	}
	preset := game.Preset6P
	if strings.EqualFold(strings.TrimSpace(body.Preset), "9p") {
		preset = game.Preset9P
	}
	count := game.PresetTargetCount(preset)

	now := time.Now().UTC()
	exp := now.Add(time.Hour)
	names := []string{
		"1号·阿狸", "2号·老白", "3号·Eino助手",
		"4号·林深", "5号·苏牧", "6号·知秋",
		"7号·星河", "8号·陆离", "9号·云舒",
	}

	sessions := make([]devPlayerSession, 0, count)
	claimsList := make([]auth.Claims, 0, count)
	for i := 0; i < count; i++ {
		uid := fmt.Sprintf("00000000-0000-4000-8000-devplayer%04d", i+1)
		cl := auth.Claims{
			AppID:          s.verifier.AppID(),
			Slug:           "werewolf",
			WorkspaceID:    wsID,
			ConversationID: convID,
			UserID:         uid,
			DisplayName:    names[i],
			IssuedAt:       now.Unix(),
			ExpiresAt:      exp.Unix(),
		}
		tok, err := auth.SignToken(s.cfg.SigningSecret, cl)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "sign_failed", "message": err.Error()})
			return
		}
		claimsList = append(claimsList, cl)
		sessions = append(sessions, devPlayerSession{
			Slot:        i + 1,
			UserID:      uid,
			DisplayName: names[i],
			Token:       tok,
			ExpiresAt:   exp.Format(time.RFC3339),
		})
	}

	qa := strings.ToLower(strings.TrimSpace(body.QuickAction))
	if qa == "seat_all" || qa == "start_game" {
		room := s.gameMgr.GetOrCreateRoom(wsID, convID)
		for i, cl := range claimsList {
			pArg := ""
			if i == 0 {
				pArg = string(preset)
			}
			_, _ = room.JoinLobby(cl, pArg, "")
		}
		if qa == "start_game" && len(claimsList) > 0 {
			_, _ = room.StartGame(claimsList[0], string(preset), 90, "")
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"appId":          s.verifier.AppID(),
		"workspaceId":    wsID,
		"conversationId": convID,
		"preset":         preset,
		"players":        sessions,
	})
}

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	cleanPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if cleanPath == "" || cleanPath == "." {
		s.serveIndexHTML(w)
		return
	}

	if s.cfg.StaticFS != nil {
		if f, err := s.cfg.StaticFS.Open(cleanPath); err == nil {
			defer f.Close()
			if stat, statErr := f.Stat(); statErr == nil && !stat.IsDir() {
				if ct := mime.TypeByExtension(path.Ext(cleanPath)); ct != "" {
					w.Header().Set("Content-Type", ct)
				}
				if strings.HasPrefix(cleanPath, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				data, readErr := io.ReadAll(f)
				if readErr == nil {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(data)
					return
				}
			}
		}
	}

	// Unknown API paths return JSON 404; non-asset paths fall back to SPA index.html
	if strings.HasPrefix(cleanPath, "api/") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if path.Ext(cleanPath) != "" {
		http.NotFound(w, r)
		return
	}
	s.serveIndexHTML(w)
}

func (s *Server) serveIndexHTML(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if s.cfg.StaticFS != nil {
		if data, err := fs.ReadFile(s.cfg.StaticFS, "index.html"); err == nil && len(data) > 0 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, fallbackHTML)
}

const fallbackHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>狼人杀裁判台 · Teamie</title>
</head>
<body>
  <div id="root">狼人杀裁判台加载中…</div>
</body>
</html>
`

func idemKeyHeader(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("Idempotency-Key"))
}

func decodeOptionalJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func writeActionResult(w http.ResponseWriter, resp game.ActionResponse, err error) {
	if err == nil {
		slog.Info("web_action_ok",
			"action", resp.Action,
			"convId", resp.View.ConversationID,
			"userId", resp.View.Me.UserID,
			"phase", resp.View.Phase,
			"round", resp.View.Round,
			"eventSeq", resp.View.EventSeq,
		)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	status := http.StatusBadRequest
	code := "invalid_action"
	switch {
	case errors.Is(err, game.ErrForbiddenRole):
		status = http.StatusForbidden
		code = "forbidden_role"
	case errors.Is(err, game.ErrWrongPhase):
		status = http.StatusConflict
		code = "wrong_phase"
	}
	specificCode := game.ErrorCodeOf(err)
	slog.Warn("web_action_rejected",
		"status", status,
		"error", code,
		"errorCode", specificCode,
		"message", err.Error(),
	)
	writeJSON(w, status, map[string]string{
		"error":     code,
		"errorCode": specificCode,
		"message":   err.Error(),
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
