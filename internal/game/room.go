package game

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zzqDeco/teamie-werewolf/internal/auth"
)

var (
	ErrInvalidAction = errors.New("invalid game action")
	ErrForbiddenRole = errors.New("forbidden for current role or seat")
	ErrWrongPhase    = errors.New("action not allowed in current phase")
)

// ActionError wraps a domain sentinel error with a machine-readable code and localized message.
type ActionError struct {
	Kind    error
	Code    string
	Message string
}

func (e *ActionError) Error() string {
	return e.Message
}

func (e *ActionError) Unwrap() error {
	return e.Kind
}

// NewActionError constructs a typed domain rejection error.
func NewActionError(kind error, code, msg string) error {
	return &ActionError{
		Kind:    kind,
		Code:    code,
		Message: msg,
	}
}

// ErrorCodeOf extracts a stable machine-readable errorCode from any domain error.
func ErrorCodeOf(err error) string {
	if err == nil {
		return ""
	}
	var ae *ActionError
	if errors.As(err, &ae) && ae.Code != "" {
		return ae.Code
	}
	switch {
	case errors.Is(err, ErrForbiddenRole):
		return "forbidden_role"
	case errors.Is(err, ErrWrongPhase):
		return "wrong_phase"
	default:
		return "invalid_action"
	}
}

type playerState struct {
	Seat        int
	UserID      string
	DisplayName string
	Role        Role
	Alive       bool
}

type cachedAction struct {
	key      string
	action   string
	message  string
	recorded time.Time
}

// Room is a concurrency-safe state machine for one (workspaceId, conversationId) group conversation.
type Room struct {
	mu             sync.Mutex
	workspaceID    string
	conversationID string

	preset          Preset
	phase           Phase
	round           int
	eventSeq        int64
	phaseDeadline   time.Time
	phaseTimeoutSec int
	timings         Timings

	players []*playerState // sorted 1..N

	// Night state
	nightStep           NightStep
	wolfVotes           map[int]int // wolfSeat -> targetSeat (0 = empty knife)
	lastWolfVoter       int
	wolfLockedTarget    int
	wolfChat            []WolfChatEntry
	wolfChatSeq         int64
	seerCheckedRound    int
	seerStepClosed      bool
	seerChecks          []SeerCheckRecord
	antidoteLeft        bool
	poisonLeft          bool
	witchActed          bool
	witchSaved          bool
	witchPoisonTarget   int
	lastNightCasualties []int

	// Hunter state
	pendingHunterSeat   int
	hunterTriggerOrigin Phase

	// Day speech state
	speakingOrder      []int
	spokenSeats        map[int]bool
	currentSpeakerSeat int

	// Day vote state
	dayVotes    map[int]int // voterSeat -> targetSeat (0 = abstain), secret until tallied
	voteHistory []VoteTallyRecord

	// Post-game unseat tracking (preserves GAME_OVER recap scoreboard while letting players vacate)
	postGameLeft map[string]bool

	// Public timeline & winner
	publicEvents []PublicEvent
	winner       Alignment
	winReason    string

	// Timer generation guard
	timerGen uint64
	timer    *time.Timer

	// Idempotency cache: userId + "\x00" + idempotencyKey
	idemOrder []string
	idemCache map[string]cachedAction

	// Optional deterministic shuffle hook for tests
	shuffleFn func([]Role)

	// Notification hooks (invoked outside mu)
	onNotifications func([]TargetedNotification)
	onRoomUpdated   func(workspaceID, conversationID string)
}

// NewRoom creates an initialized room in LOBBY phase.
func NewRoom(
	workspaceID, conversationID string,
	timings Timings,
	onNotifications func([]TargetedNotification),
	onRoomUpdated func(workspaceID, conversationID string),
) *Room {
	if timings.NightTimeout <= 0 {
		timings = DefaultTimings()
	}
	return &Room{
		workspaceID:     workspaceID,
		conversationID:  conversationID,
		preset:          Preset6P,
		phase:           PhaseLobby,
		phaseTimeoutSec: int(timings.NightTimeout.Seconds()),
		timings:         timings,
		wolfVotes:       make(map[int]int),
		spokenSeats:     make(map[int]bool),
		dayVotes:        make(map[int]int),
		postGameLeft:    make(map[string]bool),
		idemCache:       make(map[string]cachedAction),
		onNotifications: onNotifications,
		onRoomUpdated:   onRoomUpdated,
	}
}

// EmptyLobbyView returns a pure, side-effect-free LOBBY view without allocating a Room in memory.
// Used when probing or inspecting conversations that have not started any lobby activity.
func EmptyLobbyView(claims auth.Claims, timings Timings) PlayerView {
	timeoutSec := int(timings.NightTimeout.Seconds())
	if timeoutSec <= 0 {
		timeoutSec = 90
	}
	policy := PublicChatPolicy{
		Allowed: true,
		Reason:  "大厅准备阶段，可在群聊自由交流并点击入座或调用 join_lobby 加入对局。",
	}
	return PlayerView{
		WorkspaceID:     claims.WorkspaceID,
		ConversationID:  claims.ConversationID,
		Preset:          Preset6P,
		TargetPlayers:   PresetTargetCount(Preset6P),
		Phase:           PhaseLobby,
		Round:           0,
		EventSeq:        0,
		PhaseTimeoutSec: timeoutSec,
		Me: MeView{
			UserID:        claims.UserID,
			DisplayName:   claims.DisplayName,
			Seated:        false,
			Alive:         false,
			PendingAction: "join_lobby",
			ActionHint:    "点击【入座参赛】或调用 join_lobby 占据座位，满 6 人（或 9 人局满 9 人）即可发牌开局。",
		},
		Seats:            []SeatView{},
		PublicChatPolicy: policy,
	}
}

// SetShuffleFunc overrides role shuffling for deterministic tests.
func (r *Room) SetShuffleFunc(fn func([]Role)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shuffleFn = fn
}

// Close cancels any active phase timer.
func (r *Room) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelTimerLocked()
}

// GetView builds a strictly cropped Fog-of-War snapshot for claims.UserID.
func (r *Room) GetView(claims auth.Claims) PlayerView {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Refresh display name if seated
	if p := r.findPlayerByUserLocked(claims.UserID); p != nil && claims.DisplayName != "" {
		p.DisplayName = claims.DisplayName
	}
	return r.buildViewLocked(claims)
}

func (r *Room) checkIdempotencyLocked(claims auth.Claims, idemKey string) (*ActionResponse, bool) {
	idemKey = strings.TrimSpace(idemKey)
	if idemKey == "" {
		return nil, false
	}
	composite := claims.UserID + "\x00" + idemKey
	cached, ok := r.idemCache[composite]
	if !ok {
		return nil, false
	}
	view := r.buildViewLocked(claims)
	return &ActionResponse{
		OK:               true,
		Action:           cached.action,
		Message:          cached.message,
		IdempotentReplay: true,
		PublicChatPolicy: view.PublicChatPolicy,
		View:             view,
	}, true
}

func (r *Room) storeIdempotencyLocked(userID, idemKey, action, message string) {
	idemKey = strings.TrimSpace(idemKey)
	if idemKey == "" {
		return
	}
	composite := userID + "\x00" + idemKey
	if _, exists := r.idemCache[composite]; !exists {
		if len(r.idemOrder) >= 512 {
			oldest := r.idemOrder[0]
			r.idemOrder = r.idemOrder[1:]
			delete(r.idemCache, oldest)
		}
		r.idemOrder = append(r.idemOrder, composite)
	}
	r.idemCache[composite] = cachedAction{
		key:      composite,
		action:   action,
		message:  message,
		recorded: time.Now().UTC(),
	}
}

func (r *Room) dispatchEffects(notifs []TargetedNotification, changed bool) {
	if changed && r.onRoomUpdated != nil {
		r.onRoomUpdated(r.workspaceID, r.conversationID)
	}
	if len(notifs) > 0 && r.onNotifications != nil {
		r.onNotifications(notifs)
	}
}

// JoinLobby seats the caller in LOBBY (or resets a finished GAME_OVER room into a fresh LOBBY).
func (r *Room) JoinLobby(claims auth.Claims, requestedPreset string, idemKey string) (ActionResponse, error) {
	r.mu.Lock()
	if replay, ok := r.checkIdempotencyLocked(claims, idemKey); ok {
		r.mu.Unlock()
		return *replay, nil
	}

	var notifs []TargetedNotification
	if r.phase == PhaseGameOver {
		r.resetToFreshLobbyLocked()
	}
	if r.phase != PhaseLobby {
		phase := r.phase
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(
			ErrWrongPhase,
			"game_in_progress",
			fmt.Sprintf("对局已在进行中（当前阶段 %s），无法中途入座；请查看 view 按当前牌局状态行动", phase),
		)
	}

	if requestedPreset != "" {
		p := Preset(strings.ToLower(strings.TrimSpace(requestedPreset)))
		if p != Preset6P && p != Preset9P {
			r.mu.Unlock()
			return ActionResponse{}, NewActionError(ErrInvalidAction, "invalid_preset", "preset 仅支持 6p 或 9p")
		}
		target := PresetTargetCount(p)
		if len(r.players) <= target && (len(r.players) == 0 || r.players[0].UserID == claims.UserID) {
			r.preset = p
		}
	}

	existing := r.findPlayerByUserLocked(claims.UserID)
	var msg string
	if existing != nil {
		if claims.DisplayName != "" {
			existing.DisplayName = claims.DisplayName
		}
		msg = fmt.Sprintf("你已在 %d 号座位就座（当前 %d/%d 人）。", existing.Seat, len(r.players), PresetTargetCount(r.preset))
	} else {
		maxPlayers := PresetTargetCount(r.preset)
		if len(r.players) >= maxPlayers {
			preset := r.preset
			cur := len(r.players)
			r.mu.Unlock()
			return ActionResponse{}, NewActionError(
				ErrInvalidAction,
				"room_full",
				fmt.Sprintf("当前 %s 房间座位已满（%d/%d），无法继续入座", preset, cur, maxPlayers),
			)
		}
		seat := len(r.players) + 1
		r.players = append(r.players, &playerState{
			Seat:        seat,
			UserID:      claims.UserID,
			DisplayName: claims.DisplayName,
			Alive:       true,
		})
		r.bumpSeqLocked()
		r.appendPublicEventLocked(fmt.Sprintf("%d号 %s 入座（%d/%d）。", seat, claims.DisplayName, len(r.players), maxPlayers))
		msg = fmt.Sprintf("成功入座 %d 号位（当前 %d/%d 人）。", seat, len(r.players), maxPlayers)

		if len(r.players) == maxPlayers {
			// Notify Seat 1 (host) that the table is full and ready to start_game
			host := r.players[0]
			if host.UserID != claims.UserID {
				notifs = append(notifs, r.makeNotificationLocked(
					host.UserID,
					"lobby_full_ready",
					host,
					fmt.Sprintf("房间已满员（%d/%d），可立即调用 start_game 发牌开局！", maxPlayers, maxPlayers),
					"请调用 mcp__werewolf__start_game 开始游戏。",
				))
			}
		}
	}

	r.storeIdempotencyLocked(claims.UserID, idemKey, "join_lobby", msg)
	view := r.buildViewLocked(claims)
	resp := ActionResponse{
		OK:               true,
		Action:           "join_lobby",
		Message:          msg,
		PublicChatPolicy: view.PublicChatPolicy,
		View:             view,
	}
	r.mu.Unlock()
	r.dispatchEffects(notifs, true)
	return resp, nil
}

// LeaveLobby removes the caller's seat while in LOBBY (compacting 1..K),
// or marks the caller as having vacated the table after GAME_OVER (resetting to empty LOBBY once all players leave).
func (r *Room) LeaveLobby(claims auth.Claims, idemKey string) (ActionResponse, error) {
	r.mu.Lock()
	if replay, ok := r.checkIdempotencyLocked(claims, idemKey); ok {
		r.mu.Unlock()
		return *replay, nil
	}
	if r.phase != PhaseLobby && r.phase != PhaseGameOver {
		phase := r.phase
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(
			ErrWrongPhase,
			"game_in_progress",
			fmt.Sprintf("对局进行中（当前阶段 %s），无法中途离座；需等待 GAME_OVER 对局结束后方可退座", phase),
		)
	}

	var msg string
	if r.phase == PhaseGameOver {
		caller := r.findPlayerByUserLocked(claims.UserID)
		if caller == nil || r.postGameLeft[claims.UserID] {
			msg = "你已离席散场（当前不在座位上）。"
		} else {
			if r.postGameLeft == nil {
				r.postGameLeft = make(map[string]bool)
			}
			r.postGameLeft[claims.UserID] = true
			allLeft := len(r.players) > 0
			for _, p := range r.players {
				if !r.postGameLeft[p.UserID] {
					allLeft = false
					break
				}
			}
			if allLeft {
				r.resetToFreshLobbyLocked()
				msg = "全员已离席散场，房间已清空回空闲准备大厅（0/6）。"
			} else {
				r.bumpSeqLocked()
				msg = "已离席散场（本局复盘面板仍保留展示；如需开启新一局可调用 join_lobby）。"
			}
		}
	} else {
		idx := -1
		for i, p := range r.players {
			if p.UserID == claims.UserID {
				idx = i
				break
			}
		}
		if idx < 0 {
			msg = "你当前未在座位上。"
		} else {
			oldSeat := r.players[idx].Seat
			r.players = append(r.players[:idx], r.players[idx+1:]...)
			for i, p := range r.players {
				p.Seat = i + 1
			}
			r.bumpSeqLocked()
			r.appendPublicEventLocked(fmt.Sprintf("%s 离开了原 %d 号座位（当前 %d/%d）。", claims.DisplayName, oldSeat, len(r.players), PresetTargetCount(r.preset)))
			msg = "已离开大厅座位。"
		}
	}

	r.storeIdempotencyLocked(claims.UserID, idemKey, "leave_lobby", msg)
	view := r.buildViewLocked(claims)
	resp := ActionResponse{
		OK:               true,
		Action:           "leave_lobby",
		Message:          msg,
		PublicChatPolicy: view.PublicChatPolicy,
		View:             view,
	}
	r.mu.Unlock()
	r.dispatchEffects(nil, true)
	return resp, nil
}

// ConfigureLobby updates preset or timeout while in LOBBY.
func (r *Room) ConfigureLobby(claims auth.Claims, presetStr string, phaseTimeoutSec int, idemKey string) (ActionResponse, error) {
	r.mu.Lock()
	if replay, ok := r.checkIdempotencyLocked(claims, idemKey); ok {
		r.mu.Unlock()
		return *replay, nil
	}
	if r.phase != PhaseLobby {
		phase := r.phase
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(ErrWrongPhase, "wrong_phase", fmt.Sprintf("仅大厅准备阶段可调整板子配置（当前阶段 %s）", phase))
	}
	if presetStr != "" {
		p := Preset(strings.ToLower(strings.TrimSpace(presetStr)))
		if p != Preset6P && p != Preset9P {
			r.mu.Unlock()
			return ActionResponse{}, NewActionError(ErrInvalidAction, "invalid_preset", "preset 仅支持 6p 或 9p")
		}
		if len(r.players) > PresetTargetCount(p) {
			cur := len(r.players)
			r.mu.Unlock()
			return ActionResponse{}, NewActionError(ErrInvalidAction, "invalid_preset", fmt.Sprintf("当前已入座 %d 人，超过 %s 上限", cur, p))
		}
		r.preset = p
	}
	if phaseTimeoutSec >= 5 && phaseTimeoutSec <= 300 {
		r.phaseTimeoutSec = phaseTimeoutSec
		d := time.Duration(phaseTimeoutSec) * time.Second
		r.timings.NightTimeout = d
		r.timings.WitchTimeout = d
		r.timings.SpeakerTurnTimeout = d
		r.timings.VoteTimeout = d
	}
	r.bumpSeqLocked()
	msg := fmt.Sprintf("已更新房间配置：%s（阶段限时 %ds）。", r.preset, r.phaseTimeoutSec)
	r.storeIdempotencyLocked(claims.UserID, idemKey, "configure_lobby", msg)
	view := r.buildViewLocked(claims)
	resp := ActionResponse{
		OK:               true,
		Action:           "configure_lobby",
		Message:          msg,
		PublicChatPolicy: view.PublicChatPolicy,
		View:             view,
	}
	r.mu.Unlock()
	r.dispatchEffects(nil, true)
	return resp, nil
}

// StartGame deals secret roles and transitions LOBBY -> NIGHT.
func (r *Room) StartGame(claims auth.Claims, presetStr string, phaseTimeoutSec int, idemKey string) (ActionResponse, error) {
	r.mu.Lock()
	if replay, ok := r.checkIdempotencyLocked(claims, idemKey); ok {
		r.mu.Unlock()
		return *replay, nil
	}
	if r.phase != PhaseLobby {
		phase := r.phase
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(
			ErrWrongPhase,
			"game_already_started",
			fmt.Sprintf("对局已处于 %s 阶段（已由其他玩家开局），无需重复调用 start_game；请直接查看返回中的 view 按当前阶段行动", phase),
		)
	}
	caller := r.findPlayerByUserLocked(claims.UserID)
	if caller == nil {
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(ErrForbiddenRole, "not_seated", "只有已入座玩家可发牌开局，请先调用 join_lobby")
	}
	if presetStr != "" {
		p := Preset(strings.ToLower(strings.TrimSpace(presetStr)))
		if p != Preset6P && p != Preset9P {
			r.mu.Unlock()
			return ActionResponse{}, NewActionError(ErrInvalidAction, "invalid_preset", "preset 仅支持 6p 或 9p")
		}
		r.preset = p
	}
	if phaseTimeoutSec >= 5 && phaseTimeoutSec <= 300 {
		r.phaseTimeoutSec = phaseTimeoutSec
		d := time.Duration(phaseTimeoutSec) * time.Second
		r.timings.NightTimeout = d
		r.timings.WitchTimeout = d
		r.timings.SpeakerTurnTimeout = d
		r.timings.VoteTimeout = d
	}

	expected := PresetTargetCount(r.preset)
	if len(r.players) != expected {
		preset := r.preset
		cur := len(r.players)
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(
			ErrInvalidAction,
			"lobby_not_full",
			fmt.Sprintf("人数未满，%s 需要 %d 名玩家，当前仅 %d 人入座", preset, expected, cur),
		)
	}

	deck := PresetDeck(r.preset)
	if r.shuffleFn != nil {
		r.shuffleFn(deck)
	} else {
		cryptoShuffleRoles(deck)
	}

	for i, p := range r.players {
		p.Seat = i + 1
		p.Role = deck[i]
		p.Alive = true
	}

	r.round = 1
	r.antidoteLeft = true
	r.poisonLeft = true
	r.wolfChat = nil
	r.wolfChatSeq = 0
	r.seerChecks = nil
	r.seerCheckedRound = 0
	r.seerStepClosed = false
	r.voteHistory = nil
	r.postGameLeft = make(map[string]bool)
	r.winner = ""
	r.winReason = ""
	r.pendingHunterSeat = 0

	r.bumpSeqLocked()
	r.appendPublicEventLocked(fmt.Sprintf("游戏正式开始（%s 板子），天黑请闭眼，进入第 1 夜！", r.preset))
	slog.Info("room_game_started",
		"convId", r.conversationID,
		"preset", r.preset,
		"players", len(r.players),
		"phaseTimeoutSec", r.phaseTimeoutSec,
	)
	notifs := r.enterNightLocked()

	msg := fmt.Sprintf("发牌完成！你的底牌是【%s】（%d号位），已进入第 1 夜。", RoleLabel(caller.Role), caller.Seat)
	r.storeIdempotencyLocked(claims.UserID, idemKey, "start_game", msg)
	view := r.buildViewLocked(claims)
	resp := ActionResponse{
		OK:               true,
		Action:           "start_game",
		Message:          msg,
		PublicChatPolicy: view.PublicChatPolicy,
		View:             view,
	}
	r.mu.Unlock()
	r.dispatchEffects(notifs, true)
	return resp, nil
}

// WolfChat posts a private message visible strictly to WEREWOLF players during NIGHT.
func (r *Room) WolfChat(claims auth.Claims, rawMsg string, idemKey string) (ActionResponse, error) {
	r.mu.Lock()
	if replay, ok := r.checkIdempotencyLocked(claims, idemKey); ok {
		r.mu.Unlock()
		return *replay, nil
	}
	if r.phase != PhaseNight {
		phase := r.phase
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(ErrWrongPhase, "wrong_phase", fmt.Sprintf("狼人暗频仅在夜间（NIGHT）开放，当前阶段为 %s", phase))
	}
	caller := r.findPlayerByUserLocked(claims.UserID)
	if caller == nil || !caller.Alive || caller.Role != RoleWerewolf {
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(ErrForbiddenRole, "forbidden_role", "仅存活狼人可使用狼人暗频")
	}

	text := strings.TrimSpace(rawMsg)
	if text == "" {
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(ErrInvalidAction, "empty_message", "暗聊内容不能为空")
	}
	if utf8.RuneCountInString(text) > 240 {
		runes := []rune(text)
		text = string(runes[:240])
	}

	r.wolfChatSeq++
	entry := WolfChatEntry{
		Seq:         r.wolfChatSeq,
		Round:       r.round,
		Seat:        caller.Seat,
		DisplayName: caller.DisplayName,
		Message:     text,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	r.wolfChat = append(r.wolfChat, entry)
	if len(r.wolfChat) > 50 {
		r.wolfChat = r.wolfChat[len(r.wolfChat)-50:]
	}
	r.bumpSeqLocked()

	var notifs []TargetedNotification
	for _, p := range r.players {
		if p.Alive && p.Role == RoleWerewolf && p.UserID != caller.UserID {
			notifs = append(notifs, r.makeNotificationLocked(
				p.UserID,
				"wolf_chat_message",
				p,
				fmt.Sprintf("【狼人暗频】%d号(%s)：%s", caller.Seat, caller.DisplayName, truncateRunes(text, 120)),
				"你可通过 wolf_chat 回复战术，或调用 cast_night_action(actionType=\"kill\", targetSeat=...) 统一袭击目标。严禁调用 im_send_message 在公屏发言！",
			))
		}
	}

	msg := "暗聊消息已发送至存活狼队友。"
	r.storeIdempotencyLocked(claims.UserID, idemKey, "wolf_chat", msg)
	view := r.buildViewLocked(claims)
	resp := ActionResponse{
		OK:               true,
		Action:           "wolf_chat",
		Message:          msg,
		PublicChatPolicy: view.PublicChatPolicy,
		View:             view,
	}
	r.mu.Unlock()
	r.dispatchEffects(notifs, true)
	return resp, nil
}

// CastNightAction handles夜间刀人/查验/解药/毒药/跳过 during NIGHT.
func (r *Room) CastNightAction(claims auth.Claims, actionType string, targetSeat int, idemKey string) (ActionResponse, error) {
	r.mu.Lock()
	if replay, ok := r.checkIdempotencyLocked(claims, idemKey); ok {
		r.mu.Unlock()
		return *replay, nil
	}
	if r.phase != PhaseNight {
		phase := r.phase
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(
			ErrWrongPhase,
			"phase_expired",
			fmt.Sprintf("黑夜阶段已结束（当前为 %s），请查看返回中的最新 view 按白天阶段行动", phase),
		)
	}
	caller := r.findPlayerByUserLocked(claims.UserID)
	if caller == nil || !caller.Alive {
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(ErrForbiddenRole, "forbidden_role", "仅存活入座玩家可执行夜间行动")
	}

	act := strings.ToLower(strings.TrimSpace(actionType))
	var msg string
	var notifs []TargetedNotification

	switch caller.Role {
	case RoleWerewolf:
		if r.nightStep != NightStepWolvesAndSeer {
			r.mu.Unlock()
			return ActionResponse{}, NewActionError(
				ErrWrongPhase,
				"wolf_target_locked",
				"本夜狼队已全员达成一致并锁定袭击目标，正在等待神职行动与天亮",
			)
		}
		if act != "kill" && act != "pass" {
			r.mu.Unlock()
			return ActionResponse{}, NewActionError(ErrInvalidAction, "invalid_action", "狼人仅支持 actionType=\"kill\" 或 \"pass\"")
		}
		if act == "pass" {
			targetSeat = 0
		}
		if targetSeat != 0 {
			tp := r.findPlayerBySeatLocked(targetSeat)
			if tp == nil || !tp.Alive {
				r.mu.Unlock()
				return ActionResponse{}, NewActionError(ErrInvalidAction, "invalid_target", fmt.Sprintf("目标座位 %d 不存在或已出局", targetSeat))
			}
		}
		r.wolfVotes[caller.Seat] = targetSeat
		r.lastWolfVoter = caller.Seat
		r.bumpSeqLocked()

		targetDesc := "空刀"
		if targetSeat > 0 {
			targetDesc = fmt.Sprintf("%d号", targetSeat)
		}
		unanimous, _ := r.aliveWolvesUnanimousLocked()
		allVoted := r.allAliveWolvesVotedLocked()
		breakdown := r.wolfVotesSummaryLocked()

		if unanimous {
			msg = fmt.Sprintf("狼队意见达成一致，已锁定今夜袭击目标：%s！", targetDesc)
			for _, p := range r.players {
				if p.Alive && p.Role == RoleWerewolf && p.UserID != caller.UserID {
					notifs = append(notifs, r.makeNotificationLocked(
						p.UserID,
						"wolf_target_locked",
						p,
						fmt.Sprintf("【狼队锁刀】%d号已确认一致目标，今夜袭击锁定为 %s，静候天亮。", caller.Seat, targetDesc),
						"今夜狼队袭击目标已锁定，严禁调用 im_send_message 在公屏发言，请等待天亮。",
					))
				}
			}
		} else if allVoted {
			msg = fmt.Sprintf("已提交袭击意向：%s。当前狼队票型分歧（%s），暂未锁刀！请用 wolf_chat 协商或调用 cast_night_action 统一同一目标后立即锁刀（超时则按多数/后落票结算）。", targetDesc, breakdown)
			for _, p := range r.players {
				if p.Alive && p.Role == RoleWerewolf && p.UserID != caller.UserID {
					notifs = append(notifs, r.makeNotificationLocked(
						p.UserID,
						"wolf_vote_conflict",
						p,
						fmt.Sprintf("【狼队分歧】%d号选择了 %s，当前票型：%s（意见未统一，暂未锁刀）。", caller.Seat, targetDesc, breakdown),
						"请通过 wolf_chat 协商刀法，或调用 cast_night_action 改为同一目标以立即锁定刀口。严禁在公屏发言！",
					))
				}
			}
		} else {
			msg = fmt.Sprintf("已提交袭击意向：%s（当前已投：%s）。在狼队全员达成一致锁刀前你仍可修改目标，正等待队友落票。", targetDesc, breakdown)
			for _, p := range r.players {
				if p.Alive && p.Role == RoleWerewolf && p.UserID != caller.UserID {
					notifs = append(notifs, r.makeNotificationLocked(
						p.UserID,
						"wolf_vote_updated",
						p,
						fmt.Sprintf("【狼队刀口提议】%d号提议袭击 %s（当前已投：%s）。", caller.Seat, targetDesc, breakdown),
						"若同意请对同一目标调用 cast_night_action 锁定刀口，或用 wolf_chat 商议其他目标。严禁在公屏发言！",
					))
				}
			}
		}
		notifs = append(notifs, r.advanceNightProgressLocked()...)

	case RoleSeer:
		if act != "check" {
			r.mu.Unlock()
			return ActionResponse{}, NewActionError(ErrInvalidAction, "invalid_action", "预言家仅支持 actionType=\"check\"")
		}
		if r.seerCheckedRound == r.round {
			r.mu.Unlock()
			return ActionResponse{}, NewActionError(ErrInvalidAction, "already_acted", "本夜你已完成过身份查验，请静候天亮")
		}
		tp := r.findPlayerBySeatLocked(targetSeat)
		if tp == nil || !tp.Alive || tp.Seat == caller.Seat {
			r.mu.Unlock()
			return ActionResponse{}, NewActionError(ErrInvalidAction, "invalid_target", "请选择一名除自己以外的存活玩家座位进行查验")
		}
		align := AlignmentGood
		alignLabel := "好人（金水）"
		if tp.Role == RoleWerewolf {
			align = AlignmentWolf
			alignLabel = "狼人（查杀）"
		}
		r.seerCheckedRound = r.round
		r.seerChecks = append(r.seerChecks, SeerCheckRecord{
			Round:       r.round,
			TargetSeat:  tp.Seat,
			DisplayName: tp.DisplayName,
			Alignment:   align,
		})
		msg = fmt.Sprintf("查验结果：%d号（%s）的身份阵营为【%s】！天亮前严禁在群聊公屏发言。", tp.Seat, tp.DisplayName, alignLabel)
		r.bumpSeqLocked()
		notifs = append(notifs, r.advanceNightProgressLocked()...)

	case RoleWitch:
		if r.nightStep != NightStepWitch {
			r.mu.Unlock()
			return ActionResponse{}, NewActionError(ErrWrongPhase, "wait_for_wolves", "狼队尚未锁定袭击目标，请等待收到 your_turn_night_witch 通知后再使用女巫技能")
		}
		if r.witchActed {
			r.mu.Unlock()
			return ActionResponse{}, NewActionError(ErrInvalidAction, "already_acted", "本夜女巫已完成行动")
		}
		switch act {
		case "save":
			if !r.antidoteLeft {
				r.mu.Unlock()
				return ActionResponse{}, NewActionError(ErrInvalidAction, "no_antidote", "你的解药已经用完，请选择 pass 或 poison")
			}
			if r.wolfLockedTarget <= 0 {
				r.mu.Unlock()
				return ActionResponse{}, NewActionError(ErrInvalidAction, "no_attack_target", "今夜狼人空刀，无人可救，请选择 pass 或 poison")
			}
			r.antidoteLeft = false
			r.witchSaved = true
			r.witchActed = true
			msg = fmt.Sprintf("已使用解药救起今夜遇袭的 %d 号玩家。", r.wolfLockedTarget)
		case "poison":
			if !r.poisonLeft {
				r.mu.Unlock()
				return ActionResponse{}, NewActionError(ErrInvalidAction, "no_poison", "你的毒药已经用完，请选择 pass 或 save")
			}
			tp := r.findPlayerBySeatLocked(targetSeat)
			if tp == nil || !tp.Alive || tp.Seat == caller.Seat {
				r.mu.Unlock()
				return ActionResponse{}, NewActionError(ErrInvalidAction, "invalid_target", "请选择一名除自己以外的存活玩家使用毒药")
			}
			r.poisonLeft = false
			r.witchPoisonTarget = tp.Seat
			r.witchActed = true
			msg = fmt.Sprintf("已对 %d 号玩家使用毒药。", tp.Seat)
		case "pass":
			r.witchActed = true
			msg = "本夜女巫选择不使用任何药水。"
		default:
			r.mu.Unlock()
			return ActionResponse{}, NewActionError(ErrInvalidAction, "invalid_action", "女巫仅支持 actionType=\"save\"、\"poison\" 或 \"pass\"")
		}
		r.bumpSeqLocked()
		notifs = append(notifs, r.advanceNightProgressLocked()...)

	default:
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(ErrForbiddenRole, "forbidden_role", fmt.Sprintf("你的角色（%s）在夜间无主动技能，请静候天亮", RoleLabel(caller.Role)))
	}

	r.storeIdempotencyLocked(claims.UserID, idemKey, "cast_night_action", msg)
	view := r.buildViewLocked(claims)
	resp := ActionResponse{
		OK:               true,
		Action:           "cast_night_action",
		Message:          msg,
		PublicChatPolicy: view.PublicChatPolicy,
		View:             view,
	}
	r.mu.Unlock()
	r.dispatchEffects(notifs, true)
	return resp, nil
}

// FinishSpeech ends the current speaker's turn in DAY_SPEECH and hands the mic to the next alive speaker (or opens DAY_VOTE).
func (r *Room) FinishSpeech(claims auth.Claims, summary string, idemKey string) (ActionResponse, error) {
	r.mu.Lock()
	if replay, ok := r.checkIdempotencyLocked(claims, idemKey); ok {
		r.mu.Unlock()
		return *replay, nil
	}
	if r.phase != PhaseDaySpeech {
		phase := r.phase
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(
			ErrWrongPhase,
			"phase_expired",
			fmt.Sprintf("当前不是白天轮流发言阶段（已处于 %s），无需再调用 finish_speech；请查看返回中的最新 view", phase),
		)
	}
	caller := r.findPlayerByUserLocked(claims.UserID)
	if caller == nil || !caller.Alive || caller.Seat != r.currentSpeakerSeat {
		curSpeaker := r.currentSpeakerSeat
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(
			ErrForbiddenRole,
			"not_your_turn",
			fmt.Sprintf("当前持麦人为 %d 号玩家，尚未轮到你交麦（或你的麦序已超时切走）", curSpeaker),
		)
	}

	note := strings.TrimSpace(summary)
	if note != "" {
		r.appendPublicEventLocked(fmt.Sprintf("%d号 %s 结束发言（摘要：%s）。", caller.Seat, caller.DisplayName, truncateRunes(note, 80)))
	} else {
		r.appendPublicEventLocked(fmt.Sprintf("%d号 %s 结束本轮发言。", caller.Seat, caller.DisplayName))
	}

	notifs := r.advanceSpeakerLocked()
	msg := "已结束你的发言并交出麦序。"
	r.storeIdempotencyLocked(claims.UserID, idemKey, "finish_speech", msg)
	view := r.buildViewLocked(claims)
	resp := ActionResponse{
		OK:               true,
		Action:           "finish_speech",
		Message:          msg,
		PublicChatPolicy: view.PublicChatPolicy,
		View:             view,
	}
	r.mu.Unlock()
	r.dispatchEffects(notifs, true)
	return resp, nil
}

// SubmitVote casts a secret ballot during DAY_VOTE. Votes remain strictly hidden until all alive players vote or timeout expires.
func (r *Room) SubmitVote(claims auth.Claims, targetSeat int, idemKey string) (ActionResponse, error) {
	r.mu.Lock()
	if replay, ok := r.checkIdempotencyLocked(claims, idemKey); ok {
		r.mu.Unlock()
		return *replay, nil
	}
	if r.phase != PhaseDayVote || r.pendingHunterSeat > 0 {
		phase := r.phase
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(ErrWrongPhase, "wrong_phase", fmt.Sprintf("当前不在放逐投票收票阶段（当前阶段 %s）", phase))
	}
	caller := r.findPlayerByUserLocked(claims.UserID)
	if caller == nil || !caller.Alive {
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(ErrForbiddenRole, "forbidden_role", "仅存活玩家可参与白天放逐投票")
	}
	if targetSeat < 0 {
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(ErrInvalidAction, "invalid_target", "targetSeat 不能为负数（0 表示弃票）")
	}
	if targetSeat > 0 {
		tp := r.findPlayerBySeatLocked(targetSeat)
		if tp == nil || !tp.Alive {
			r.mu.Unlock()
			return ActionResponse{}, NewActionError(ErrInvalidAction, "invalid_target", fmt.Sprintf("投票目标 %d 号不存在或已出局", targetSeat))
		}
	}

	r.dayVotes[caller.Seat] = targetSeat
	r.bumpSeqLocked()

	var msg string
	if targetSeat == 0 {
		msg = "已提交暗票：弃票（全员投完开票前严格保密，开票前仍可重复调用覆盖改票）。"
	} else {
		msg = fmt.Sprintf("已提交暗票：投给 %d 号（全员投完开票前严格保密，开票前仍可重复调用覆盖改票）。", targetSeat)
	}

	var notifs []TargetedNotification
	if r.allAliveVotedLocked() {
		notifs = r.tallyDayVotesLocked()
	}

	r.storeIdempotencyLocked(claims.UserID, idemKey, "submit_vote", msg)
	view := r.buildViewLocked(claims)
	resp := ActionResponse{
		OK:               true,
		Action:           "submit_vote",
		Message:          msg,
		PublicChatPolicy: view.PublicChatPolicy,
		View:             view,
	}
	r.mu.Unlock()
	r.dispatchEffects(notifs, true)
	return resp, nil
}

// HunterShoot resolves the Hunter's revenge shot when triggered in DAY_ANNOUNCE or DAY_VOTE.
func (r *Room) HunterShoot(claims auth.Claims, targetSeat int, idemKey string) (ActionResponse, error) {
	r.mu.Lock()
	if replay, ok := r.checkIdempotencyLocked(claims, idemKey); ok {
		r.mu.Unlock()
		return *replay, nil
	}
	if r.pendingHunterSeat <= 0 {
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(ErrWrongPhase, "wrong_phase", "当前没有待发动的猎人开枪技能")
	}
	caller := r.findPlayerByUserLocked(claims.UserID)
	if caller == nil || caller.Seat != r.pendingHunterSeat || caller.Role != RoleHunter {
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(ErrForbiddenRole, "forbidden_role", "仅触发技能的猎人本人可执行开枪操作")
	}
	if targetSeat < 0 {
		r.mu.Unlock()
		return ActionResponse{}, NewActionError(ErrInvalidAction, "invalid_target", "targetSeat 不能为负数（0 表示压枪不开枪）")
	}
	if targetSeat > 0 {
		tp := r.findPlayerBySeatLocked(targetSeat)
		if tp == nil || !tp.Alive || tp.Seat == caller.Seat {
			r.mu.Unlock()
			return ActionResponse{}, NewActionError(ErrInvalidAction, "invalid_target", "请选择一名存活的其他玩家开枪，或传入 0 压枪")
		}
	}

	notifs, msg := r.resolveHunterShotLocked(targetSeat)
	r.storeIdempotencyLocked(claims.UserID, idemKey, "hunter_shoot", msg)
	view := r.buildViewLocked(claims)
	resp := ActionResponse{
		OK:               true,
		Action:           "hunter_shoot",
		Message:          msg,
		PublicChatPolicy: view.PublicChatPolicy,
		View:             view,
	}
	r.mu.Unlock()
	r.dispatchEffects(notifs, true)
	return resp, nil
}

// --- Internal State Transitions (must hold r.mu) ---

func (r *Room) resetToFreshLobbyLocked() {
	r.cancelTimerLocked()
	r.phase = PhaseLobby
	r.round = 0
	r.players = nil
	r.wolfVotes = make(map[int]int)
	r.wolfLockedTarget = 0
	r.wolfChat = nil
	r.wolfChatSeq = 0
	r.seerCheckedRound = 0
	r.seerStepClosed = false
	r.seerChecks = nil
	r.antidoteLeft = false
	r.poisonLeft = false
	r.witchActed = false
	r.witchSaved = false
	r.witchPoisonTarget = 0
	r.lastNightCasualties = nil
	r.pendingHunterSeat = 0
	r.speakingOrder = nil
	r.spokenSeats = make(map[int]bool)
	r.currentSpeakerSeat = 0
	r.dayVotes = make(map[int]int)
	r.voteHistory = nil
	r.postGameLeft = make(map[string]bool)
	r.publicEvents = nil
	r.winner = ""
	r.winReason = ""
	r.phaseDeadline = time.Time{}
	r.bumpSeqLocked()
}

func (r *Room) enterNightLocked() []TargetedNotification {
	r.phase = PhaseNight
	r.nightStep = NightStepWolvesAndSeer
	r.wolfVotes = make(map[int]int)
	r.lastWolfVoter = 0
	r.wolfLockedTarget = 0
	r.seerStepClosed = false
	r.witchActed = false
	r.witchSaved = false
	r.witchPoisonTarget = 0
	r.pendingHunterSeat = 0
	r.currentSpeakerSeat = 0
	r.speakingOrder = nil
	r.spokenSeats = make(map[int]bool)
	r.dayVotes = make(map[int]int)
	r.bumpSeqLocked()

	slog.Info("room_enter_night", "convId", r.conversationID, "round", r.round)

	r.scheduleTimerLocked(r.timings.NightTimeout, func() []TargetedNotification {
		if r.phase != PhaseNight {
			return nil
		}
		r.seerStepClosed = true
		if r.nightStep == NightStepWolvesAndSeer {
			r.lockWolfTargetFromVotesLocked()
			slog.Info("room_night_wolves_timeout",
				"convId", r.conversationID,
				"round", r.round,
				"lockedTarget", r.wolfLockedTarget,
			)
			return r.enterWitchStepOrDawnLocked()
		}
		r.witchActed = true
		slog.Info("room_night_seer_timeout", "convId", r.conversationID, "round", r.round)
		return r.resolveNightToDayAnnounceLocked()
	})

	var notifs []TargetedNotification
	wolfNames := r.aliveWolfLabelsLocked()
	for _, p := range r.players {
		if !p.Alive {
			continue
		}
		switch p.Role {
		case RoleWerewolf:
			notifs = append(notifs, r.makeNotificationLocked(
				p.UserID,
				"your_turn_night_wolf",
				p,
				fmt.Sprintf("第 %d 夜开始！你的身份是【狼人】（存活狼队：%s）。", r.round, strings.Join(wolfNames, "、")),
				"严禁调用 im_send_message 在公屏发言！可先调用 wolf_chat 与狼队友协商刀法，再调用 cast_night_action(actionType=\"kill\", targetSeat=...) 提交袭击目标（全员目标一致即锁刀）。",
			))
		case RoleSeer:
			notifs = append(notifs, r.makeNotificationLocked(
				p.UserID,
				"your_turn_night_seer",
				p,
				fmt.Sprintf("第 %d 夜开始！你的身份是【预言家】。", r.round),
				"严禁调用 im_send_message 在公屏发言！请立即调用 cast_night_action(actionType=\"check\", targetSeat=...) 查验一名存活玩家阵营。",
			))
		}
	}

	// Edge case: if all wolves are somehow dead (should already be GAME_OVER), advance immediately.
	if r.countAliveByRoleLocked(RoleWerewolf) == 0 {
		notifs = append(notifs, r.advanceNightProgressLocked()...)
	}
	return notifs
}

func (r *Room) enterWitchStepOrDawnLocked() []TargetedNotification {
	witch := r.findAliveRoleLocked(RoleWitch)
	if witch != nil && (r.antidoteLeft || r.poisonLeft) {
		r.nightStep = NightStepWitch
		r.bumpSeqLocked()

		witchDur := r.timings.WitchTimeout
		if witchDur <= 0 {
			witchDur = r.timings.NightTimeout
		}
		if !r.phaseDeadline.IsZero() {
			if rem := time.Until(r.phaseDeadline); rem > witchDur {
				witchDur = rem
			}
		}
		r.scheduleTimerLocked(witchDur, func() []TargetedNotification {
			if r.phase != PhaseNight || r.nightStep != NightStepWitch {
				return nil
			}
			r.witchActed = true
			r.seerStepClosed = true
			slog.Info("room_night_witch_timeout", "convId", r.conversationID, "round", r.round)
			return r.resolveNightToDayAnnounceLocked()
		})

		attackedDesc := "（你的解药已用完，无法获知今夜刀口）"
		if r.antidoteLeft {
			if r.wolfLockedTarget > 0 {
				attackedDesc = fmt.Sprintf("今夜 %d 号玩家遇袭！", r.wolfLockedTarget)
			} else {
				attackedDesc = "今夜狼人选择空刀（无人遇袭）。"
			}
		}
		return []TargetedNotification{
			r.makeNotificationLocked(
				witch.UserID,
				"your_turn_night_witch",
				witch,
				fmt.Sprintf("第 %d 夜女巫行动阶段：%s（解药剩余=%v，毒药剩余=%v）", r.round, attackedDesc, r.antidoteLeft, r.poisonLeft),
				"严禁调用 im_send_message 在公屏发言！请立即调用 cast_night_action 选择 save（救人）、poison（毒人）或 pass（不用药）。",
			),
		}
	}

	r.nightStep = NightStepWitch
	r.witchActed = true
	seer := r.findAliveRoleLocked(RoleSeer)
	seerDone := seer == nil || r.seerCheckedRound == r.round || r.seerStepClosed
	if seerDone {
		return r.resolveNightToDayAnnounceLocked()
	}
	return nil
}

func (r *Room) advanceNightProgressLocked() []TargetedNotification {
	if r.phase != PhaseNight {
		return nil
	}
	var notifs []TargetedNotification

	unanimous, _ := r.aliveWolvesUnanimousLocked()
	if r.nightStep == NightStepWolvesAndSeer && unanimous {
		r.lockWolfTargetFromVotesLocked()
		notifs = append(notifs, r.enterWitchStepOrDawnLocked()...)
		return notifs
	}

	seer := r.findAliveRoleLocked(RoleSeer)
	seerDone := seer == nil || r.seerCheckedRound == r.round || r.seerStepClosed
	if r.nightStep == NightStepWitch && r.witchActed && seerDone {
		notifs = append(notifs, r.resolveNightToDayAnnounceLocked()...)
	}
	return notifs
}

func (r *Room) lockWolfTargetFromVotesLocked() {
	counts := make(map[int]int)
	bestTarget := 0
	bestCount := -1
	for _, target := range r.wolfVotes {
		counts[target]++
	}
	for target, cnt := range counts {
		if cnt > bestCount {
			bestCount = cnt
			bestTarget = target
		} else if cnt == bestCount && r.lastWolfVoter > 0 && r.wolfVotes[r.lastWolfVoter] == target {
			bestTarget = target
		}
	}
	if bestCount <= 0 {
		bestTarget = 0
	}
	r.wolfLockedTarget = bestTarget
}

func (r *Room) resolveNightToDayAnnounceLocked() []TargetedNotification {
	r.cancelTimerLocked()
	casualtySet := make(map[int]bool)
	wolfKillSeat := 0
	if r.wolfLockedTarget > 0 && !r.witchSaved {
		if p := r.findPlayerBySeatLocked(r.wolfLockedTarget); p != nil && p.Alive {
			wolfKillSeat = p.Seat
			casualtySet[p.Seat] = true
		}
	}
	poisonKillSeat := 0
	if r.witchPoisonTarget > 0 {
		if p := r.findPlayerBySeatLocked(r.witchPoisonTarget); p != nil && p.Alive {
			poisonKillSeat = p.Seat
			casualtySet[p.Seat] = true
		}
	}

	casualties := make([]int, 0, len(casualtySet))
	for seat := range casualtySet {
		casualties = append(casualties, seat)
	}
	sort.Ints(casualties)
	r.lastNightCasualties = casualties

	var hunterTriggered *playerState
	for _, seat := range casualties {
		p := r.findPlayerBySeatLocked(seat)
		if p != nil {
			p.Alive = false
			// Authentic Werewolf rule: Hunter died to wolf attack AND was NOT poisoned by Witch!
			if p.Role == RoleHunter && seat == wolfKillSeat && seat != poisonKillSeat {
				hunterTriggered = p
			}
		}
	}

	r.phase = PhaseDayAnnounce
	r.bumpSeqLocked()

	if len(casualties) == 0 {
		r.appendPublicEventLocked(fmt.Sprintf("第 %d 天天亮了：昨晚是平安夜，无人倒牌！", r.round))
	} else {
		labels := make([]string, 0, len(casualties))
		for _, s := range casualties {
			if p := r.findPlayerBySeatLocked(s); p != nil {
				labels = append(labels, fmt.Sprintf("%d号(%s)", p.Seat, p.DisplayName))
			}
		}
		r.appendPublicEventLocked(fmt.Sprintf("第 %d 天天亮了：昨晚 %s 倒牌（暗牌局不公布死因与身份）。", r.round, strings.Join(labels, "、")))
	}

	if hunterTriggered != nil {
		r.pendingHunterSeat = hunterTriggered.Seat
		r.hunterTriggerOrigin = PhaseDayAnnounce
		r.scheduleTimerLocked(r.timings.HunterTimeout, func() []TargetedNotification {
			if r.pendingHunterSeat <= 0 {
				return nil
			}
			notifs, _ := r.resolveHunterShotLocked(0)
			return notifs
		})
		return []TargetedNotification{
			r.makeNotificationLocked(
				hunterTriggered.UserID,
				"your_turn_hunter_shoot",
				hunterTriggered,
				fmt.Sprintf("你在第 %d 夜倒牌并触发【猎人】开枪技能！", r.round),
				"请立即调用 mcp__werewolf__hunter_shoot(targetSeat=1..N) 开枪带走一名存活玩家，或传 targetSeat=0 压枪。",
			),
		}
	}

	if winner, reason := r.evaluateWinnerLocked(); winner != "" {
		return r.enterGameOverLocked(winner, reason)
	}

	if r.timings.DayAnnounceDelay <= 0 {
		return r.enterDaySpeechLocked()
	}

	r.scheduleTimerLocked(r.timings.DayAnnounceDelay, func() []TargetedNotification {
		if r.phase != PhaseDayAnnounce {
			return nil
		}
		return r.enterDaySpeechLocked()
	})
	return nil
}

func (r *Room) enterDaySpeechLocked() []TargetedNotification {
	r.cancelTimerLocked()
	if winner, reason := r.evaluateWinnerLocked(); winner != "" {
		return r.enterGameOverLocked(winner, reason)
	}

	r.phase = PhaseDaySpeech
	r.spokenSeats = make(map[int]bool)

	total := len(r.players)
	startSeat := ((r.round - 1) % total) + 1
	if len(r.lastNightCasualties) > 0 {
		startSeat = (r.lastNightCasualties[0] % total) + 1
	}

	order := make([]int, 0, total)
	for offset := 0; offset < total; offset++ {
		seat := ((startSeat - 1 + offset) % total) + 1
		p := r.findPlayerBySeatLocked(seat)
		if p != nil && p.Alive {
			order = append(order, seat)
		}
	}
	r.speakingOrder = order
	if len(order) == 0 {
		return r.enterDayVoteLocked()
	}

	r.currentSpeakerSeat = order[0]
	r.bumpSeqLocked()
	r.appendPublicEventLocked(fmt.Sprintf("进入第 %d 天白天轮流发言阶段，按麦序 %v 依次单人发言，首先请 %d 号发言。", r.round, order, r.currentSpeakerSeat))
	slog.Info("room_enter_day_speech", "convId", r.conversationID, "round", r.round, "speaker", r.currentSpeakerSeat, "order", order)
	r.armSpeakerTimerLocked()

	speaker := r.findPlayerBySeatLocked(r.currentSpeakerSeat)
	if speaker == nil {
		return nil
	}
	dawnBrief := "昨晚是平安夜。"
	if len(r.lastNightCasualties) > 0 {
		dawnBrief = fmt.Sprintf("昨晚倒牌座位：%v。", r.lastNightCasualties)
	}
	return []TargetedNotification{
		r.makeNotificationLocked(
			speaker.UserID,
			"your_turn_to_speak",
			speaker,
			fmt.Sprintf("第 %d 天白天发言轮到你（%d号 %s）！%s", r.round, speaker.Seat, speaker.DisplayName, dawnBrief),
			"请立即调用一次 im_send_message 在群聊公屏发表 80～150 字口语化精炼陈词（禁长篇小作文），发完后立刻调用 mcp__werewolf__finish_speech 交麦，谨防超时！",
		),
	}
}

func (r *Room) armSpeakerTimerLocked() {
	expectedSpeaker := r.currentSpeakerSeat
	r.scheduleTimerLocked(r.timings.SpeakerTurnTimeout, func() []TargetedNotification {
		if r.phase != PhaseDaySpeech || r.currentSpeakerSeat != expectedSpeaker {
			return nil
		}
		slog.Warn("room_speaker_timeout", "convId", r.conversationID, "round", r.round, "speaker", expectedSpeaker)
		r.appendPublicEventLocked(fmt.Sprintf("%d号发言超时，裁判自动切换至下一位麦序。", expectedSpeaker))
		return r.advanceSpeakerLocked()
	})
}

func (r *Room) advanceSpeakerLocked() []TargetedNotification {
	r.cancelTimerLocked()
	if r.currentSpeakerSeat > 0 {
		r.spokenSeats[r.currentSpeakerSeat] = true
	}

	nextSeat := 0
	for _, seat := range r.speakingOrder {
		p := r.findPlayerBySeatLocked(seat)
		if p != nil && p.Alive && !r.spokenSeats[seat] {
			nextSeat = seat
			break
		}
	}

	if nextSeat == 0 {
		return r.enterDayVoteLocked()
	}

	r.currentSpeakerSeat = nextSeat
	r.bumpSeqLocked()
	slog.Info("room_next_speaker", "convId", r.conversationID, "round", r.round, "speaker", nextSeat)
	r.armSpeakerTimerLocked()

	speaker := r.findPlayerBySeatLocked(nextSeat)
	if speaker == nil {
		return nil
	}
	return []TargetedNotification{
		r.makeNotificationLocked(
			speaker.UserID,
			"your_turn_to_speak",
			speaker,
			fmt.Sprintf("第 %d 天白天发言现在轮到你（%d号 %s）！前序玩家已交麦。", r.round, speaker.Seat, speaker.DisplayName),
			"请立即调用一次 im_send_message 在群聊公屏发表 80～150 字口语化精炼陈词（禁长篇小作文），发完后立刻调用 mcp__werewolf__finish_speech 交麦，谨防超时！",
		),
	}
}

func (r *Room) enterDayVoteLocked() []TargetedNotification {
	r.cancelTimerLocked()
	r.phase = PhaseDayVote
	r.currentSpeakerSeat = 0
	r.dayVotes = make(map[int]int)
	r.bumpSeqLocked()
	r.appendPublicEventLocked(fmt.Sprintf("第 %d 天全员发言完毕，进入白天暗票放逐投票阶段！", r.round))
	slog.Info("room_enter_day_vote", "convId", r.conversationID, "round", r.round)

	r.scheduleTimerLocked(r.timings.VoteTimeout, func() []TargetedNotification {
		if r.phase != PhaseDayVote || r.pendingHunterSeat > 0 {
			return nil
		}
		slog.Warn("room_vote_timeout", "convId", r.conversationID, "round", r.round)
		for _, p := range r.players {
			if p.Alive {
				if _, voted := r.dayVotes[p.Seat]; !voted {
					r.dayVotes[p.Seat] = 0
				}
			}
		}
		return r.tallyDayVotesLocked()
	})

	var notifs []TargetedNotification
	for _, p := range r.players {
		if !p.Alive {
			continue
		}
		notifs = append(notifs, r.makeNotificationLocked(
			p.UserID,
			"your_turn_to_vote",
			p,
			fmt.Sprintf("第 %d 天进入暗票放逐投票阶段，请投出你的选票（全员投完开票前严格保密）。", r.round),
			"请静默调用 mcp__werewolf__submit_vote(targetSeat=1..N 或 0 弃票)；全员落票前可重复调用改票，投票期间严禁调用 im_send_message 在公屏发言。",
		))
	}
	return notifs
}

func (r *Room) tallyDayVotesLocked() []TargetedNotification {
	r.cancelTimerLocked()

	votesCopy := make(map[int]int, len(r.dayVotes))
	counts := make(map[int]int)
	for _, p := range r.players {
		if p.Alive {
			target := r.dayVotes[p.Seat]
			votesCopy[p.Seat] = target
			if target > 0 {
				counts[target]++
			}
		}
	}

	maxCount := 0
	var topSeats []int
	for target, cnt := range counts {
		if cnt > maxCount {
			maxCount = cnt
			topSeats = []int{target}
		} else if cnt == maxCount {
			topSeats = append(topSeats, target)
		}
	}
	sort.Ints(topSeats)

	eliminatedSeat := 0
	isTie := false
	var summary string
	if maxCount == 0 || len(topSeats) > 1 {
		isTie = true
		if maxCount == 0 {
			summary = fmt.Sprintf("第 %d 天放逐投票开票：全员弃票，无人出局！", r.round)
		} else {
			summary = fmt.Sprintf("第 %d 天放逐投票开票：%v 号同为最高票（%d 票）平票，今日无人出局！", r.round, topSeats, maxCount)
		}
	} else {
		eliminatedSeat = topSeats[0]
		ep := r.findPlayerBySeatLocked(eliminatedSeat)
		if ep != nil {
			ep.Alive = false
			summary = fmt.Sprintf("第 %d 天放逐投票开票：%d号（%s）以 %d 票被公投放逐出局！", r.round, ep.Seat, ep.DisplayName, maxCount)
		}
	}

	r.voteHistory = append(r.voteHistory, VoteTallyRecord{
		Round:          r.round,
		VotesBySeat:    votesCopy,
		EliminatedSeat: eliminatedSeat,
		IsTie:          isTie,
		Summary:        summary,
	})
	r.bumpSeqLocked()
	r.appendPublicEventLocked(summary)

	if eliminatedSeat > 0 {
		ep := r.findPlayerBySeatLocked(eliminatedSeat)
		if ep != nil && ep.Role == RoleHunter {
			r.pendingHunterSeat = ep.Seat
			r.hunterTriggerOrigin = PhaseDayVote
			r.scheduleTimerLocked(r.timings.HunterTimeout, func() []TargetedNotification {
				if r.pendingHunterSeat <= 0 {
					return nil
				}
				notifs, _ := r.resolveHunterShotLocked(0)
				return notifs
			})
			return []TargetedNotification{
				r.makeNotificationLocked(
					ep.UserID,
					"your_turn_hunter_shoot",
					ep,
					fmt.Sprintf("你在第 %d 天被放逐出局，触发【猎人】开枪技能！", r.round),
					"请立即调用 mcp__werewolf__hunter_shoot(targetSeat=1..N) 开枪带走一名存活玩家，或传 targetSeat=0 压枪。",
				),
			}
		}
	}

	if winner, reason := r.evaluateWinnerLocked(); winner != "" {
		return r.enterGameOverLocked(winner, reason)
	}

	r.round++
	r.appendPublicEventLocked(fmt.Sprintf("天黑请闭眼，进入第 %d 夜！", r.round))
	return r.enterNightLocked()
}

func (r *Room) resolveHunterShotLocked(targetSeat int) ([]TargetedNotification, string) {
	r.cancelTimerLocked()
	hunterSeat := r.pendingHunterSeat
	origin := r.hunterTriggerOrigin
	r.pendingHunterSeat = 0
	r.hunterTriggerOrigin = ""

	var msg string
	if targetSeat > 0 {
		tp := r.findPlayerBySeatLocked(targetSeat)
		if tp != nil && tp.Alive {
			tp.Alive = false
			r.appendPublicEventLocked(fmt.Sprintf("猎人 %d 号发动技能，开枪带走了 %d号（%s）！", hunterSeat, tp.Seat, tp.DisplayName))
			msg = fmt.Sprintf("你开枪带走了 %d 号玩家！", tp.Seat)
		}
	} else {
		r.appendPublicEventLocked(fmt.Sprintf("猎人 %d 号选择压枪，未带走任何玩家。", hunterSeat))
		msg = "你选择了压枪不开枪。"
	}
	r.bumpSeqLocked()

	if winner, reason := r.evaluateWinnerLocked(); winner != "" {
		return r.enterGameOverLocked(winner, reason), msg
	}

	if origin == PhaseDayAnnounce {
		return r.enterDaySpeechLocked(), msg
	}
	r.round++
	r.appendPublicEventLocked(fmt.Sprintf("天黑请闭眼，进入第 %d 夜！", r.round))
	return r.enterNightLocked(), msg
}

func (r *Room) evaluateWinnerLocked() (Alignment, string) {
	wolves := 0
	villagers := 0
	gods := 0
	for _, p := range r.players {
		if !p.Alive {
			continue
		}
		switch p.Role {
		case RoleWerewolf:
			wolves++
		case RoleVillager:
			villagers++
		case RoleSeer, RoleWitch, RoleHunter:
			gods++
		}
	}
	if wolves == 0 {
		return AlignmentGood, "好人阵营获胜：所有狼人已被放逐或淘汰！"
	}
	if villagers == 0 {
		return AlignmentWolf, "狼人阵营获胜：所有平民已出局（屠民成功）！"
	}
	if gods == 0 {
		return AlignmentWolf, "狼人阵营获胜：所有神职已出局（屠神成功）！"
	}
	return "", ""
}

func (r *Room) enterGameOverLocked(winner Alignment, reason string) []TargetedNotification {
	r.cancelTimerLocked()
	r.phase = PhaseGameOver
	r.currentSpeakerSeat = 0
	r.pendingHunterSeat = 0
	r.winner = winner
	r.winReason = reason
	r.bumpSeqLocked()
	r.appendPublicEventLocked("对局结束！" + reason + " 全员底牌现已揭晓。")
	slog.Info("room_game_over", "convId", r.conversationID, "round", r.round, "winner", winner, "reason", reason)

	var notifs []TargetedNotification
	for _, p := range r.players {
		notifs = append(notifs, r.makeNotificationLocked(
			p.UserID,
			"game_over",
			p,
			fmt.Sprintf("对局结束：%s", reason),
			"本局已完结。除非人类玩家主动提问，否则请勿自动发送长篇复盘；若不再继续下一局可调用 leave_lobby 散场离席，或调用 join_lobby 开启新局。",
		))
	}
	return notifs
}

// --- Fog-of-War View & Policy Builders (must hold r.mu) ---

func (r *Room) computePolicyForLocked(caller *playerState) PublicChatPolicy {
	switch r.phase {
	case PhaseLobby:
		return PublicChatPolicy{
			Allowed: true,
			Reason:  "大厅准备阶段，可在群聊自由交流并邀请玩家入座。",
		}
	case PhaseNight:
		return PublicChatPolicy{
			Allowed: false,
			Reason:  "当前处于黑夜（NIGHT）阶段，严禁调用 im_send_message 在群聊公屏梦游发言！狼人请仅使用 wolf_chat 暗聊。",
		}
	case PhaseDayAnnounce:
		if r.pendingHunterSeat > 0 && caller != nil && caller.Seat == r.pendingHunterSeat {
			return PublicChatPolicy{
				Allowed:           true,
				ActiveSpeakerSeat: r.pendingHunterSeat,
				Reason:            "你（猎人）倒牌触发技能，可在公屏留简短遗言并立即调用 hunter_shoot 开枪或压枪。",
			}
		}
		return PublicChatPolicy{
			Allowed: false,
			Reason:  "天亮公布昨夜信息阶段，请静候进入白天单人轮流发言阶段。",
		}
	case PhaseDaySpeech:
		if caller != nil && caller.Alive && caller.Seat == r.currentSpeakerSeat {
			return PublicChatPolicy{
				Allowed:           true,
				ActiveSpeakerSeat: r.currentSpeakerSeat,
				Reason:            fmt.Sprintf("现在轮到你（%d号）发言！请在群聊公屏发表一次 80～150 字口语化简短发言，发完立即调用 finish_speech 交麦。", r.currentSpeakerSeat),
			}
		}
		return PublicChatPolicy{
			Allowed:           false,
			ActiveSpeakerSeat: r.currentSpeakerSeat,
			Reason:            fmt.Sprintf("当前严格执行单人麦序，正在由 %d 号玩家发言，即使被提及也严禁插话抢麦！", r.currentSpeakerSeat),
		}
	case PhaseDayVote:
		if r.pendingHunterSeat > 0 && caller != nil && caller.Seat == r.pendingHunterSeat {
			return PublicChatPolicy{
				Allowed:           true,
				ActiveSpeakerSeat: r.pendingHunterSeat,
				Reason:            "你（猎人）被放逐触发技能，请立即调用 hunter_shoot 选择开枪目标或压枪。",
			}
		}
		return PublicChatPolicy{
			Allowed: false,
			Reason:  "当前处于白天暗票放逐投票阶段，请静默调用 submit_vote 投出暗票（开票前可重复调用改票），严禁公屏发言。",
		}
	case PhaseGameOver:
		return PublicChatPolicy{
			Allowed: true,
			Reason:  "对局已结束，全员底牌已公开（请简短交流，切勿刷屏长篇复盘；不再继续可调用 leave_lobby 散场）。",
		}
	default:
		return PublicChatPolicy{Allowed: false, Reason: "请遵循裁判台阶段指引。"}
	}
}

func (r *Room) computePendingActionLocked(caller *playerState) (string, string) {
	if r.phase == PhaseLobby {
		if caller == nil {
			if len(r.players) < PresetTargetCount(r.preset) {
				return "join_lobby", "点击【入座参赛】或调用 join_lobby 加入本局。"
			}
			return "", "座位已满，等待场上玩家开始对局。"
		}
		if len(r.players) == PresetTargetCount(r.preset) {
			return "start_game", "座位已满员！点击【开始发牌】或调用 start_game 进入第 1 夜。"
		}
		return "wait_players", fmt.Sprintf("等待其余玩家入座（当前 %d/%d）。", len(r.players), PresetTargetCount(r.preset))
	}

	if r.phase == PhaseGameOver {
		if caller != nil && r.postGameLeft[caller.UserID] {
			return "", "你已离席散场；如需开启新一局可点击【再来一局】或调用 join_lobby。"
		}
		return "join_lobby", "本局已结束，可调用 leave_lobby 离席散场，或调用 join_lobby 开启新一局。"
	}

	if caller == nil {
		return "", "你是场外观众（迷雾视角），可观看公屏博弈与放逐计票。"
	}

	if r.pendingHunterSeat > 0 && caller.Seat == r.pendingHunterSeat && caller.Role == RoleHunter {
		return "hunter_shoot", "你已触发猎人技能！请选择一名存活玩家开枪带走，或选择压枪。"
	}

	if !caller.Alive {
		return "", "你已出局，请保持观战，切勿在群聊公屏透露底牌或场外信息。"
	}

	switch r.phase {
	case PhaseNight:
		switch caller.Role {
		case RoleWerewolf:
			if r.nightStep == NightStepWolvesAndSeer {
				if _, voted := r.wolfVotes[caller.Seat]; !voted {
					return "cast_night_action", "天黑行动：可在狼人暗频交流战术，并选择一名袭击目标（或空刀）。"
				}
				if unanimous, _ := r.aliveWolvesUnanimousLocked(); !unanimous && r.allAliveWolvesVotedLocked() {
					return "cast_night_action", "狼队当前票型存在分歧（尚未锁刀），请用 wolf_chat 协商或调用 cast_night_action 统一袭击目标。"
				}
				return "wait_night", "你已提交袭击意向（全员一致锁刀前仍可改选），正在等待狼队友完成确认。"
			}
			return "wait_night", "狼队目标已锁定，等待神职行动与天亮。"
		case RoleSeer:
			if r.seerCheckedRound != r.round && !r.seerStepClosed {
				return "cast_night_action", "天黑查验：请选择一名存活玩家查验其阵营（好人/狼人）。"
			}
			return "wait_night", "你已完成今夜查验，请静候天亮。"
		case RoleWitch:
			if r.nightStep == NightStepWitch && !r.witchActed {
				return "cast_night_action", "女巫行动：请选择使用解药救人、使用毒药毒人，或不使用药水。"
			}
			return "wait_night", "请静候狼人行动结束或等待天亮。"
		default:
			return "wait_night", "天黑闭眼中，请静候天亮。"
		}
	case PhaseDayAnnounce:
		return "wait_day_speech", "正在公布昨夜情况，即将进入白天轮流发言。"
	case PhaseDaySpeech:
		if caller.Seat == r.currentSpeakerSeat {
			return "finish_speech", "轮到你发言！请先在群聊公屏发送 80～150 字简短发言，随后立即点击【结束本轮发言】或调用 finish_speech 交麦。"
		}
		return "wait_speaker", fmt.Sprintf("请耐心等待 %d 号玩家发言完毕交麦。", r.currentSpeakerSeat)
	case PhaseDayVote:
		if _, voted := r.dayVotes[caller.Seat]; !voted {
			return "submit_vote", "请选择放逐投票目标或弃票（全员投完开票前严格保密，可改票）。"
		}
		return "wait_votes", "你已投出暗票（全员投完前仍可重新选择改票），等待其余存活玩家完成投票后统一开票。"
	}
	return "", ""
}

func (r *Room) buildViewLocked(claims auth.Claims) PlayerView {
	caller := r.findPlayerByUserLocked(claims.UserID)
	policy := r.computePolicyForLocked(caller)
	pendingAction, actionHint := r.computePendingActionLocked(caller)

	seats := make([]SeatView, 0, len(r.players))
	for _, p := range r.players {
		var knownRole Role
		if r.phase == PhaseGameOver {
			knownRole = p.Role
		} else if caller != nil {
			if p.UserID == caller.UserID {
				knownRole = p.Role
			} else if caller.Role == RoleWerewolf && p.Role == RoleWerewolf {
				knownRole = RoleWerewolf
			}
		}
		_, hasVoted := r.dayVotes[p.Seat]
		seats = append(seats, SeatView{
			Seat:        p.Seat,
			DisplayName: p.DisplayName,
			IsMe:        p.UserID == claims.UserID,
			Alive:       p.Alive,
			KnownRole:   knownRole,
			IsSpeaking:  r.phase == PhaseDaySpeech && r.currentSpeakerSeat == p.Seat,
			HasSpoken:   r.phase == PhaseDaySpeech && r.spokenSeats[p.Seat],
			HasVoted:    r.phase == PhaseDayVote && r.pendingHunterSeat == 0 && hasVoted,
		})
	}

	isSeated := caller != nil
	if r.phase == PhaseGameOver && r.postGameLeft[claims.UserID] {
		isSeated = false
	}

	me := MeView{
		UserID:        claims.UserID,
		DisplayName:   claims.DisplayName,
		Seated:        isSeated,
		PendingAction: pendingAction,
		ActionHint:    actionHint,
	}
	if caller != nil {
		me.Seat = caller.Seat
		me.Alive = caller.Alive
		if r.phase != PhaseLobby {
			me.Role = caller.Role
			me.RoleLabel = RoleLabel(caller.Role)
		}
	}

	// Strictly construct role-gated PrivateView (Fog-of-War red line!)
	var priv *PrivateView
	if caller != nil && r.phase != PhaseLobby {
		p := &PrivateView{}
		hasAny := false

		if caller.Role == RoleWerewolf {
			hasAny = true
			teammates := []int{}
			for _, pl := range r.players {
				if pl.Role == RoleWerewolf {
					teammates = append(teammates, pl.Seat)
				}
			}
			p.WolfTeammateSeats = teammates
			p.WolfCanVote = caller.Alive && r.phase == PhaseNight && r.nightStep == NightStepWolvesAndSeer
			if len(r.wolfChat) > 0 {
				chatCopy := make([]WolfChatEntry, len(r.wolfChat))
				copy(chatCopy, r.wolfChat)
				p.WolfChat = chatCopy
			}
			if r.phase == PhaseNight && len(r.wolfVotes) > 0 {
				intentCopy := make(map[int]int, len(r.wolfVotes))
				for k, v := range r.wolfVotes {
					intentCopy[k] = v
				}
				p.WolfIntentions = intentCopy
			}
		}

		if caller.Role == RoleSeer && len(r.seerChecks) > 0 {
			hasAny = true
			checksCopy := make([]SeerCheckRecord, len(r.seerChecks))
			copy(checksCopy, r.seerChecks)
			p.SeerChecks = checksCopy
		}

		if caller.Role == RoleWitch {
			hasAny = true
			ws := &WitchPrivateState{
				AntidoteLeft: r.antidoteLeft,
				PoisonLeft:   r.poisonLeft,
				CanActNow:    r.phase == PhaseNight && r.nightStep == NightStepWitch && !r.witchActed && caller.Alive,
			}
			// Witch sees tonight's wolf target ONLY during NIGHT Witch step while she still holds the antidote!
			if r.phase == PhaseNight && r.nightStep == NightStepWitch && r.antidoteLeft {
				atk := r.wolfLockedTarget
				ws.TonightAttackedSeat = &atk
			}
			p.WitchState = ws
		}

		if caller.Role == RoleHunter && r.pendingHunterSeat == caller.Seat {
			hasAny = true
			p.HunterCanShoot = true
		}

		if r.phase == PhaseDayVote && r.pendingHunterSeat == 0 {
			if myTarget, ok := r.dayVotes[caller.Seat]; ok {
				hasAny = true
				v := myTarget
				p.MySubmittedVote = &v
			}
		}

		if hasAny {
			priv = p
		}
	}

	var speechView *SpeechProgressView
	if r.phase == PhaseDaySpeech {
		spoken := make([]int, 0, len(r.spokenSeats))
		for s, ok := range r.spokenSeats {
			if ok {
				spoken = append(spoken, s)
			}
		}
		sort.Ints(spoken)
		orderCopy := make([]int, len(r.speakingOrder))
		copy(orderCopy, r.speakingOrder)
		speechView = &SpeechProgressView{
			CurrentSpeakerSeat: r.currentSpeakerSeat,
			SpeakingOrder:      orderCopy,
			SpokenSeats:        spoken,
		}
	}

	var deadlineMs int64
	if !r.phaseDeadline.IsZero() && r.phase != PhaseLobby && r.phase != PhaseGameOver {
		deadlineMs = r.phaseDeadline.UnixMilli()
	}

	var voteHistCopy []VoteTallyRecord
	if len(r.voteHistory) > 0 {
		voteHistCopy = make([]VoteTallyRecord, len(r.voteHistory))
		for i, vh := range r.voteHistory {
			m := make(map[int]int, len(vh.VotesBySeat))
			for k, v := range vh.VotesBySeat {
				m[k] = v
			}
			vh.VotesBySeat = m
			voteHistCopy[i] = vh
		}
	}

	var eventsCopy []PublicEvent
	if len(r.publicEvents) > 0 {
		eventsCopy = make([]PublicEvent, len(r.publicEvents))
		copy(eventsCopy, r.publicEvents)
	}

	return PlayerView{
		WorkspaceID:      r.workspaceID,
		ConversationID:   r.conversationID,
		Preset:           r.preset,
		TargetPlayers:    PresetTargetCount(r.preset),
		Phase:            r.phase,
		Round:            r.round,
		EventSeq:         r.eventSeq,
		PhaseDeadlineMs:  deadlineMs,
		PhaseTimeoutSec:  r.phaseTimeoutSec,
		Me:               me,
		Seats:            seats,
		Speech:           speechView,
		PublicChatPolicy: policy,
		Private:          priv,
		VoteHistory:      voteHistCopy,
		PublicEvents:     eventsCopy,
		Winner:           r.winner,
		WinReason:        r.winReason,
	}
}

func (r *Room) makeNotificationLocked(targetUserID, eventType string, p *playerState, summary, actionHint string) TargetedNotification {
	policy := r.computePolicyForLocked(p)
	pending, _ := r.computePendingActionLocked(p)
	var seat int
	var role Role
	if p != nil {
		seat = p.Seat
		if r.phase != PhaseLobby {
			role = p.Role
		}
	}
	return TargetedNotification{
		WorkspaceID:    r.workspaceID,
		ConversationID: r.conversationID,
		TargetUserID:   targetUserID,
		Payload: NotificationPayload{
			EventSeq:         r.eventSeq,
			EventType:        eventType,
			Phase:            r.phase,
			Round:            r.round,
			Seat:             seat,
			Role:             role,
			PendingAction:    pending,
			Summary:          truncateRunes(summary, 300),
			ActionHint:       truncateRunes(actionHint, 260),
			PublicChatPolicy: policy,
		},
	}
}

// --- Helpers (must hold r.mu) ---

func (r *Room) bumpSeqLocked() {
	r.eventSeq++
}

func (r *Room) appendPublicEventLocked(summary string) {
	ev := PublicEvent{
		Seq:       r.eventSeq,
		Round:     r.round,
		Phase:     r.phase,
		Summary:   summary,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	r.publicEvents = append(r.publicEvents, ev)
	if len(r.publicEvents) > 60 {
		r.publicEvents = r.publicEvents[len(r.publicEvents)-60:]
	}
}

func (r *Room) cancelTimerLocked() {
	r.timerGen++
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.phaseDeadline = time.Time{}
}

func (r *Room) scheduleTimerLocked(d time.Duration, fn func() []TargetedNotification) {
	r.cancelTimerLocked()
	if d <= 0 {
		return
	}
	r.timerGen++
	gen := r.timerGen
	r.phaseDeadline = time.Now().UTC().Add(d)
	r.timer = time.AfterFunc(d, func() {
		r.mu.Lock()
		if r.timerGen != gen {
			r.mu.Unlock()
			return
		}
		r.timer = nil
		r.phaseDeadline = time.Time{}
		notifs := fn()
		r.mu.Unlock()
		r.dispatchEffects(notifs, true)
	})
}

func (r *Room) findPlayerByUserLocked(userID string) *playerState {
	for _, p := range r.players {
		if p.UserID == userID {
			return p
		}
	}
	return nil
}

func (r *Room) findPlayerBySeatLocked(seat int) *playerState {
	for _, p := range r.players {
		if p.Seat == seat {
			return p
		}
	}
	return nil
}

func (r *Room) findAliveRoleLocked(role Role) *playerState {
	for _, p := range r.players {
		if p.Alive && p.Role == role {
			return p
		}
	}
	return nil
}

func (r *Room) countAliveByRoleLocked(role Role) int {
	n := 0
	for _, p := range r.players {
		if p.Alive && p.Role == role {
			n++
		}
	}
	return n
}

func (r *Room) aliveWolfLabelsLocked() []string {
	var out []string
	for _, p := range r.players {
		if p.Alive && p.Role == RoleWerewolf {
			out = append(out, fmt.Sprintf("%d号(%s)", p.Seat, p.DisplayName))
		}
	}
	return out
}

func (r *Room) allAliveWolvesVotedLocked() bool {
	for _, p := range r.players {
		if p.Alive && p.Role == RoleWerewolf {
			if _, ok := r.wolfVotes[p.Seat]; !ok {
				return false
			}
		}
	}
	return true
}

// aliveWolvesUnanimousLocked returns true only when every alive werewolf has submitted a vote
// AND all alive werewolves selected the exact same targetSeat (including unanimous 0 = empty knife).
func (r *Room) aliveWolvesUnanimousLocked() (bool, int) {
	first := true
	agreed := 0
	aliveCount := 0
	for _, p := range r.players {
		if !p.Alive || p.Role != RoleWerewolf {
			continue
		}
		aliveCount++
		v, ok := r.wolfVotes[p.Seat]
		if !ok {
			return false, 0
		}
		if first {
			agreed = v
			first = false
		} else if v != agreed {
			return false, 0
		}
	}
	if aliveCount == 0 {
		return true, 0
	}
	return true, agreed
}

func (r *Room) wolfVotesSummaryLocked() string {
	var parts []string
	for _, p := range r.players {
		if !p.Alive || p.Role != RoleWerewolf {
			continue
		}
		if v, ok := r.wolfVotes[p.Seat]; ok {
			if v == 0 {
				parts = append(parts, fmt.Sprintf("%d号→空刀", p.Seat))
			} else {
				parts = append(parts, fmt.Sprintf("%d号→%d号", p.Seat, v))
			}
		}
	}
	return strings.Join(parts, "，")
}

func (r *Room) allAliveVotedLocked() bool {
	for _, p := range r.players {
		if p.Alive {
			if _, ok := r.dayVotes[p.Seat]; !ok {
				return false
			}
		}
	}
	return true
}

func cryptoShuffleRoles(deck []Role) {
	for i := len(deck) - 1; i > 0; i-- {
		nBig, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			continue
		}
		j := int(nBig.Int64())
		deck[i], deck[j] = deck[j], deck[i]
	}
}

func truncateRunes(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + "…"
}
