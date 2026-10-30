package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/zzqDeco/teamie-werewolf/internal/auth"
	"github.com/zzqDeco/teamie-werewolf/internal/game"
)

const (
	// ServerName is reported in MCP initialize responses.
	ServerName = "teamie-werewolf"
	// NotificationMethod is the JSON-RPC notification method pushed over MCP SSE.
	// Teamie agent-runtime wraps this into TEAMIE_APP_NOTIFICATION envelopes.
	NotificationMethod = "notifications/werewolf_event"
	// MaxNotificationBytes enforces the < 2 KB Fog-of-War notification budget.
	MaxNotificationBytes = 2048
)

type ctxClaimsKey struct{}
type ctxIdemKey struct{}

type sessionMeta struct {
	SessionID      string
	WorkspaceID    string
	ConversationID string
	UserID         string
	CreatedAt      time.Time
}

type sessionRegistry struct {
	mu       sync.RWMutex
	byID     map[string]sessionMeta
	byPlayer map[playerKey]map[string]struct{}
}

type playerKey struct {
	WorkspaceID    string
	ConversationID string
	UserID         string
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{
		byID:     make(map[string]sessionMeta),
		byPlayer: make(map[playerKey]map[string]struct{}),
	}
}

func (r *sessionRegistry) register(claims auth.Claims) string {
	sid := "ww-mcp-" + uuid.NewString()
	meta := sessionMeta{
		SessionID:      sid,
		WorkspaceID:    claims.WorkspaceID,
		ConversationID: claims.ConversationID,
		UserID:         claims.UserID,
		CreatedAt:      time.Now().UTC(),
	}
	pk := playerKey{
		WorkspaceID:    claims.WorkspaceID,
		ConversationID: claims.ConversationID,
		UserID:         claims.UserID,
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[sid] = meta
	set, ok := r.byPlayer[pk]
	if !ok {
		set = make(map[string]struct{})
		r.byPlayer[pk] = set
	}
	set[sid] = struct{}{}
	return sid
}

func (r *sessionRegistry) get(sid string) (sessionMeta, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	meta, ok := r.byID[sid]
	return meta, ok
}

func (r *sessionRegistry) delete(sid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	meta, ok := r.byID[sid]
	if !ok {
		return
	}
	delete(r.byID, sid)
	pk := playerKey{
		WorkspaceID:    meta.WorkspaceID,
		ConversationID: meta.ConversationID,
		UserID:         meta.UserID,
	}
	if set, exists := r.byPlayer[pk]; exists {
		delete(set, sid)
		if len(set) == 0 {
			delete(r.byPlayer, pk)
		}
	}
}

func (r *sessionRegistry) sessionsForPlayer(workspaceID, conversationID, userID string) []string {
	pk := playerKey{
		WorkspaceID:    workspaceID,
		ConversationID: conversationID,
		UserID:         userID,
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	set := r.byPlayer[pk]
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for sid := range set {
		out = append(out, sid)
	}
	return out
}

type requestScopedSessionManager struct {
	registry *sessionRegistry
	mcpSrv   *server.MCPServer
	claims   auth.Claims
}

func (m *requestScopedSessionManager) Generate() string {
	return m.registry.register(m.claims)
}

func (m *requestScopedSessionManager) Validate(sessionID string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, errors.New("missing Mcp-Session-Id")
	}
	if _, ok := m.registry.get(sessionID); !ok {
		// Return isTerminated=true so StreamableHTTPServer responds with HTTP 404 Not Found,
		// triggering automatic session re-handshake in Teamie agent-runtime.
		return true, nil
	}
	return false, nil
}

func (m *requestScopedSessionManager) Terminate(sessionID string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" {
		m.registry.delete(sessionID)
		m.mcpSrv.UnregisterSession(context.Background(), sessionID)
	}
	return false, nil
}

type sessionResolver struct {
	registry *sessionRegistry
	mcpSrv   *server.MCPServer
}

func (sr *sessionResolver) ResolveSessionIdManager(r *http.Request) server.SessionIdManager {
	claims, _ := claimsFromContext(r.Context())
	return &requestScopedSessionManager{
		registry: sr.registry,
		mcpSrv:   sr.mcpSrv,
		claims:   claims,
	}
}

// Handler serves MCP Streamable HTTP (2025-03-26) with strict HS256 auth,
// stateful session binding, Idempotency-Key propagation, and targeted Fog-of-War notifications.
type Handler struct {
	verifier   *auth.Verifier
	gameMgr    *game.Manager
	mcpSrv     *server.MCPServer
	streamable *server.StreamableHTTPServer
	sessions   *sessionRegistry

	notifCh   chan []game.TargetedNotification
	closeOnce sync.Once
	closed    chan struct{}
	wg        sync.WaitGroup
}

// NewHandler wires the 9 orthogonal Werewolf MCP tools onto an authenticated Streamable HTTP handler.
func NewHandler(version string, verifier *auth.Verifier, gameMgr *game.Manager) *Handler {
	if strings.TrimSpace(version) == "" {
		version = "0.1.0"
	}
	instructions := "狼人杀裁判台（Werewolf）：支持 6p/9p 人机同局博弈。重要纪律：1) 严格遵守每次工具返回或通知中的 publicChatPolicy；NIGHT 夜间严禁调用 im_send_message 在公屏梦游发言（狼人请用 wolf_chat 暗聊）；2) DAY_SPEECH 白天仅在轮到你持麦（activeSpeakerSeat == 你的座位）时在群聊发言一次，发言完毕后必须立即调用 finish_speech 交麦；3) 一切身份与房间均由平台 Context Token 自动绑定，无需也不可伪造 userId/conversationId。"

	mcpSrv := server.NewMCPServer(
		ServerName,
		version,
		server.WithToolCapabilities(false),
		server.WithInstructions(instructions),
	)

	reg := newSessionRegistry()
	resolver := &sessionResolver{
		registry: reg,
		mcpSrv:   mcpSrv,
	}

	h := &Handler{
		verifier: verifier,
		gameMgr:  gameMgr,
		mcpSrv:   mcpSrv,
		sessions: reg,
		notifCh:  make(chan []game.TargetedNotification, 256),
		closed:   make(chan struct{}),
	}

	h.registerTools()

	h.streamable = server.NewStreamableHTTPServer(
		mcpSrv,
		server.WithSessionIdManagerResolver(resolver),
		server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			if claims, ok := claimsFromContext(r.Context()); ok {
				ctx = context.WithValue(ctx, ctxClaimsKey{}, claims)
			}
			if idemKey := strings.TrimSpace(r.Header.Get("Idempotency-Key")); idemKey != "" {
				ctx = context.WithValue(ctx, ctxIdemKey{}, idemKey)
			}
			return ctx
		}),
	)

	h.wg.Add(1)
	go h.notificationLoop()

	return h
}

// Close stops the background notification dispatcher cleanly.
func (h *Handler) Close() {
	h.closeOnce.Do(func() {
		close(h.closed)
	})
	h.wg.Wait()
}

// ServeHTTP enforces HS256 Context Token authentication and Mcp-Session-Id integrity
// before delegating to the MCP Streamable HTTP server.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	claims, err := h.verifier.VerifyAuthorizationHeader(r.Header.Get("Authorization"))
	if err != nil {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]string{
			"error":   "unauthorized",
			"message": err.Error(),
		})
		return
	}

	sessionID := strings.TrimSpace(r.Header.Get(server.HeaderKeySessionID))
	if (r.Method == http.MethodGet || r.Method == http.MethodDelete) && sessionID == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error":   "missing_mcp_session_id",
			"message": "Mcp-Session-Id header is required for GET and DELETE",
		})
		return
	}

	if sessionID != "" {
		meta, ok := h.sessions.get(sessionID)
		if !ok {
			writeJSONStatus(w, http.StatusNotFound, map[string]string{
				"error":   "mcp_session_not_found",
				"message": "MCP session expired or server restarted; please re-initialize",
			})
			return
		}
		if meta.WorkspaceID != claims.WorkspaceID ||
			meta.ConversationID != claims.ConversationID ||
			meta.UserID != claims.UserID {
			writeJSONStatus(w, http.StatusForbidden, map[string]string{
				"error":   "mcp_session_identity_mismatch",
				"message": "Context Token identity or conversation does not match Mcp-Session-Id",
			})
			return
		}
	}

	ctx := context.WithValue(r.Context(), ctxClaimsKey{}, *claims)
	h.streamable.ServeHTTP(w, r.WithContext(ctx))
}

// EnqueueNotifications schedules targeted Fog-of-War notifications for delivery over MCP SSE.
func (h *Handler) EnqueueNotifications(notifs []game.TargetedNotification) {
	if len(notifs) == 0 {
		return
	}
	copied := make([]game.TargetedNotification, len(notifs))
	copy(copied, notifs)
	select {
	case h.notifCh <- copied:
	case <-h.closed:
	default:
		// Never block game state transitions if queue is saturated
	}
}

func (h *Handler) notificationLoop() {
	defer h.wg.Done()
	for {
		select {
		case <-h.closed:
			return
		case batch := <-h.notifCh:
			// Brief yield so any in-flight synchronous tools/call HTTP POST completes
			// writing its JSON response before notifications flow through the GET SSE stream.
			time.Sleep(5 * time.Millisecond)
			for _, n := range batch {
				h.deliverOne(n)
			}
		}
	}
}

func (h *Handler) deliverOne(n game.TargetedNotification) {
	sids := h.sessions.sessionsForPlayer(n.WorkspaceID, n.ConversationID, n.TargetUserID)
	if len(sids) == 0 {
		return
	}

	payload := n.Payload
	paramsMap, err := payloadToBoundedParams(payload)
	if err != nil {
		return
	}

	for _, sid := range sids {
		if err := h.mcpSrv.SendNotificationToSpecificClient(sid, NotificationMethod, paramsMap); err != nil {
			if errors.Is(err, server.ErrSessionNotFound) {
				h.sessions.delete(sid)
			}
		}
	}
}

func payloadToBoundedParams(p game.NotificationPayload) (map[string]any, error) {
	encode := func(cand game.NotificationPayload) (map[string]any, int, error) {
		raw, err := json.Marshal(cand)
		if err != nil {
			return nil, 0, err
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, 0, err
		}
		envelope := map[string]any{
			"jsonrpc": "2.0",
			"method":  NotificationMethod,
			"params":  m,
		}
		envBytes, err := json.Marshal(envelope)
		if err != nil {
			return nil, 0, err
		}
		return m, len(envBytes), nil
	}

	m, size, err := encode(p)
	if err != nil {
		return nil, err
	}
	if size < MaxNotificationBytes {
		return m, nil
	}

	// Truncate text hints if ever needed to strictly stay under 2 KB
	runes := []rune(p.Summary)
	if len(runes) > 120 {
		p.Summary = string(runes[:120]) + "…"
	}
	hintRunes := []rune(p.ActionHint)
	if len(hintRunes) > 120 {
		p.ActionHint = string(hintRunes[:120]) + "…"
	}
	m, _, err = encode(p)
	return m, err
}

func claimsFromContext(ctx context.Context) (auth.Claims, bool) {
	c, ok := ctx.Value(ctxClaimsKey{}).(auth.Claims)
	return c, ok
}

func idempotencyKeyFromRequest(ctx context.Context, req mcp.CallToolRequest) string {
	if req.Header != nil {
		if v := strings.TrimSpace(req.Header.Get("Idempotency-Key")); v != "" {
			return v
		}
	}
	if v, ok := ctx.Value(ctxIdemKey{}).(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func (h *Handler) registerTools() {
	// 1. get_my_view (read-only, zero side effects)
	h.mcpSrv.AddTool(
		mcp.NewTool(
			"get_my_view",
			mcp.WithDescription("获取当前群聊狼人杀房间与你本人的迷雾裁剪视角（底牌、座位、阶段、单人麦序、publicChatPolicy 与待办提示）。只读无副作用。"),
		),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			claims, ok := claimsFromContext(ctx)
			if !ok {
				return mcp.NewToolResultError("missing verified context token"), nil
			}
			view := h.gameMgr.GetView(claims)
			return marshalToolResult(view)
		},
	)

	// 2. join_lobby
	h.mcpSrv.AddTool(
		mcp.NewTool(
			"join_lobby",
			mcp.WithDescription("在 LOBBY 准备阶段（或上一局 GAME_OVER 后）入座参赛。"),
			mcp.WithString("preset",
				mcp.Description("可选板子预设：6p（2狼2民1预1女巫）或 9p（3狼3民1预1女巫1猎人），仅空桌或1号房主生效。"),
				mcp.Enum("6p", "9p"),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			claims, ok := claimsFromContext(ctx)
			if !ok {
				return mcp.NewToolResultError("missing verified context token"), nil
			}
			preset := req.GetString("preset", "")
			idemKey := idempotencyKeyFromRequest(ctx, req)
			room := h.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
			resp, err := room.JoinLobby(claims, preset, idemKey)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return marshalToolResult(resp)
		},
	)

	// 3. leave_lobby
	h.mcpSrv.AddTool(
		mcp.NewTool(
			"leave_lobby",
			mcp.WithDescription("在 LOBBY 准备阶段离开当前座位。"),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			claims, ok := claimsFromContext(ctx)
			if !ok {
				return mcp.NewToolResultError("missing verified context token"), nil
			}
			idemKey := idempotencyKeyFromRequest(ctx, req)
			room := h.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
			resp, err := room.LeaveLobby(claims, idemKey)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return marshalToolResult(resp)
		},
	)

	// 4. start_game
	h.mcpSrv.AddTool(
		mcp.NewTool(
			"start_game",
			mcp.WithDescription("当房间满员（6p 满 6 人或 9p 满 9 人）后发暗牌并进入第 1 夜（NIGHT）。"),
			mcp.WithString("preset",
				mcp.Description("可选板子预设：6p 或 9p。"),
				mcp.Enum("6p", "9p"),
			),
			mcp.WithNumber("phaseTimeoutSec",
				mcp.Description("可选单阶段超时秒数（5-300，默认 90）。"),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			claims, ok := claimsFromContext(ctx)
			if !ok {
				return mcp.NewToolResultError("missing verified context token"), nil
			}
			preset := req.GetString("preset", "")
			timeoutSec := req.GetInt("phaseTimeoutSec", req.GetInt("phase_timeout_sec", 0))
			idemKey := idempotencyKeyFromRequest(ctx, req)
			room := h.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
			resp, err := room.StartGame(claims, preset, timeoutSec, idemKey)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return marshalToolResult(resp)
		},
	)

	// 5. wolf_chat
	h.mcpSrv.AddTool(
		mcp.NewTool(
			"wolf_chat",
			mcp.WithDescription("夜间狼人专属暗频交流（仅存活狼人在 NIGHT 阶段可用，严禁在群聊公屏暴露战术）。"),
			mcp.WithString("message",
				mcp.Required(),
				mcp.Description("发送给存活狼队友的暗频战术消息（上限 240 字）。"),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			claims, ok := claimsFromContext(ctx)
			if !ok {
				return mcp.NewToolResultError("missing verified context token"), nil
			}
			message := req.GetString("message", "")
			idemKey := idempotencyKeyFromRequest(ctx, req)
			room := h.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
			resp, err := room.WolfChat(claims, message, idemKey)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return marshalToolResult(resp)
		},
	)

	// 6. cast_night_action
	h.mcpSrv.AddTool(
		mcp.NewTool(
			"cast_night_action",
			mcp.WithDescription("在 NIGHT 阶段提交夜间行动：狼人传 actionType=kill(或pass) 与 targetSeat；预言家传 actionType=check 与 targetSeat；女巫传 actionType=save|poison|pass（毒人时传 targetSeat）。"),
			mcp.WithString("actionType",
				mcp.Required(),
				mcp.Description("夜间行动类型：kill | check | save | poison | pass"),
				mcp.Enum("kill", "check", "save", "poison", "pass"),
			),
			mcp.WithNumber("targetSeat",
				mcp.Description("目标座位号（1..N；pass 或 save 时无需传或传 0）。"),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			claims, ok := claimsFromContext(ctx)
			if !ok {
				return mcp.NewToolResultError("missing verified context token"), nil
			}
			actionType := req.GetString("actionType", req.GetString("action", ""))
			targetSeat := req.GetInt("targetSeat", req.GetInt("target_seat", 0))
			idemKey := idempotencyKeyFromRequest(ctx, req)
			room := h.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
			resp, err := room.CastNightAction(claims, actionType, targetSeat, idemKey)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return marshalToolResult(resp)
		},
	)

	// 7. finish_speech
	h.mcpSrv.AddTool(
		mcp.NewTool(
			"finish_speech",
			mcp.WithDescription("在 DAY_SPEECH 白天轮流发言阶段，仅当前持麦玩家在群聊发送完本轮公屏发言后调用，结束发言并交麦给下一位玩家。"),
			mcp.WithString("summary",
				mcp.Description("可选一句话发言摘要（记录至公共时间线，不超过 80 字）。"),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			claims, ok := claimsFromContext(ctx)
			if !ok {
				return mcp.NewToolResultError("missing verified context token"), nil
			}
			summary := req.GetString("summary", "")
			idemKey := idempotencyKeyFromRequest(ctx, req)
			room := h.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
			resp, err := room.FinishSpeech(claims, summary, idemKey)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return marshalToolResult(resp)
		},
	)

	// 8. submit_vote
	h.mcpSrv.AddTool(
		mcp.NewTool(
			"submit_vote",
			mcp.WithDescription("在 DAY_VOTE 白天放逐投票阶段提交暗票（全员投完前严格保密）。targetSeat 传 1..N 投放逐票，传 0 弃票。"),
			mcp.WithNumber("targetSeat",
				mcp.Required(),
				mcp.Description("放逐目标座位号（1..N），弃票传 0。"),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			claims, ok := claimsFromContext(ctx)
			if !ok {
				return mcp.NewToolResultError("missing verified context token"), nil
			}
			targetSeat := req.GetInt("targetSeat", req.GetInt("target_seat", -1))
			if targetSeat < 0 {
				return mcp.NewToolResultError("targetSeat 必填（1..N 或 0 弃票）"), nil
			}
			idemKey := idempotencyKeyFromRequest(ctx, req)
			room := h.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
			resp, err := room.SubmitVote(claims, targetSeat, idemKey)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return marshalToolResult(resp)
		},
	)

	// 9. hunter_shoot
	h.mcpSrv.AddTool(
		mcp.NewTool(
			"hunter_shoot",
			mcp.WithDescription("仅当猎人被狼刀出局或白天被投票放逐且收到 hunter_shoot 待办时调用：targetSeat 传 1..N 开枪带走一名存活玩家，传 0 压枪不开枪。"),
			mcp.WithNumber("targetSeat",
				mcp.Required(),
				mcp.Description("开枪目标座位号（1..N），压枪不开枪传 0。"),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			claims, ok := claimsFromContext(ctx)
			if !ok {
				return mcp.NewToolResultError("missing verified context token"), nil
			}
			targetSeat := req.GetInt("targetSeat", req.GetInt("target_seat", -1))
			if targetSeat < 0 {
				return mcp.NewToolResultError("targetSeat 必填（1..N 或 0 压枪）"), nil
			}
			idemKey := idempotencyKeyFromRequest(ctx, req)
			room := h.gameMgr.GetOrCreateRoom(claims.WorkspaceID, claims.ConversationID)
			resp, err := room.HunterShoot(claims, targetSeat, idemKey)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return marshalToolResult(resp)
		},
	)
}

func marshalToolResult(v any) (*mcp.CallToolResult, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal response failed: %v", err)), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}

func writeJSONStatus(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
