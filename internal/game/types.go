package game

import "time"

// Phase enumerates the canonical Werewolf game state machine stages.
type Phase string

const (
	PhaseLobby       Phase = "LOBBY"
	PhaseNight       Phase = "NIGHT"
	PhaseDayAnnounce Phase = "DAY_ANNOUNCE"
	PhaseDaySpeech   Phase = "DAY_SPEECH"
	PhaseDayVote     Phase = "DAY_VOTE"
	PhaseGameOver    Phase = "GAME_OVER"
)

// Role enumerates player secret identities.
type Role string

const (
	RoleWerewolf Role = "WEREWOLF"
	RoleVillager Role = "VILLAGER"
	RoleSeer     Role = "SEER"
	RoleWitch    Role = "WITCH"
	RoleHunter   Role = "HUNTER"
)

// Alignment indicates camp affiliation.
type Alignment string

const (
	AlignmentWolf Alignment = "WOLF"
	AlignmentGood Alignment = "GOOD"
)

// Preset identifies supported board setups.
type Preset string

const (
	Preset6P Preset = "6p" // 2 WEREWOLF + 2 VILLAGER + 1 SEER + 1 WITCH
	Preset9P Preset = "9p" // 3 WEREWOLF + 3 VILLAGER + 1 SEER + 1 WITCH + 1 HUNTER
)

// NightStep tracks internal sub-stages within PhaseNight without exposing non-canonical top-level phases.
type NightStep string

const (
	NightStepWolvesAndSeer NightStep = "WOLVES_AND_SEER"
	NightStepWitch         NightStep = "WITCH"
)

// PublicChatPolicy guides both AI Agents and human players on whether speaking in Teamie public group chat is allowed.
type PublicChatPolicy struct {
	Allowed           bool   `json:"allowed"`
	ActiveSpeakerSeat int    `json:"activeSpeakerSeat,omitempty"`
	Reason            string `json:"reason"`
}

// WolfChatEntry is a private dark-channel message visible strictly to WEREWOLF players.
type WolfChatEntry struct {
	Seq         int64  `json:"seq"`
	Round       int    `json:"round"`
	Seat        int    `json:"seat"`
	DisplayName string `json:"displayName"`
	Message     string `json:"message"`
	CreatedAt   string `json:"createdAt"`
}

// SeerCheckRecord is visible strictly to the SEER player.
type SeerCheckRecord struct {
	Round       int       `json:"round"`
	TargetSeat  int       `json:"targetSeat"`
	DisplayName string    `json:"displayName"`
	Alignment   Alignment `json:"alignment"`
}

// WitchPrivateState is visible strictly to the WITCH player.
type WitchPrivateState struct {
	AntidoteLeft        bool `json:"antidoteLeft"`
	PoisonLeft          bool `json:"poisonLeft"`
	CanActNow           bool `json:"canActNow"`
	TonightAttackedSeat *int `json:"tonightAttackedSeat,omitempty"`
}

// VoteTallyRecord is published only AFTER a DAY_VOTE round closes and tallies.
type VoteTallyRecord struct {
	Round          int         `json:"round"`
	VotesBySeat    map[int]int `json:"votesBySeat"` // voterSeat -> targetSeat (0 = abstain)
	EliminatedSeat int         `json:"eliminatedSeat,omitempty"`
	IsTie          bool        `json:"isTie"`
	Summary        string      `json:"summary"`
}

// PublicEvent is a fog-safe public timeline entry (never contains roles, potion usage, or unrevealed votes).
type PublicEvent struct {
	Seq       int64  `json:"seq"`
	Round     int    `json:"round"`
	Phase     Phase  `json:"phase"`
	Summary   string `json:"summary"`
	CreatedAt string `json:"createdAt"`
}

// SeatView represents one seat cropped strictly to the requesting player's perspective.
type SeatView struct {
	Seat        int    `json:"seat"`
	DisplayName string `json:"displayName"`
	IsMe        bool   `json:"isMe"`
	Alive       bool   `json:"alive"`
	KnownRole   Role   `json:"knownRole,omitempty"` // Populated ONLY for self, wolf teammates (if wolf), or when GAME_OVER
	IsSpeaking  bool   `json:"isSpeaking,omitempty"`
	HasSpoken   bool   `json:"hasSpoken,omitempty"`
	HasVoted    bool   `json:"hasVoted,omitempty"`
}

// MeView describes the caller's own status and required action.
type MeView struct {
	UserID        string `json:"userId"`
	DisplayName   string `json:"displayName"`
	Seated        bool   `json:"seated"`
	Seat          int    `json:"seat,omitempty"`
	Role          Role   `json:"role,omitempty"`
	RoleLabel     string `json:"roleLabel,omitempty"`
	Alive         bool   `json:"alive"`
	PendingAction string `json:"pendingAction,omitempty"`
	ActionHint    string `json:"actionHint,omitempty"`
}

// PrivateView holds role-specific secret state, strictly nil/omitted for unauthorized roles.
type PrivateView struct {
	WolfTeammateSeats []int             `json:"wolfTeammateSeats,omitempty"`
	WolfIntentions    map[int]int       `json:"wolfIntentions,omitempty"`
	WolfChat          []WolfChatEntry   `json:"wolfChat,omitempty"`
	SeerChecks        []SeerCheckRecord `json:"seerChecks,omitempty"`
	WitchState        *WitchPrivateState `json:"witchState,omitempty"`
	MySubmittedVote   *int              `json:"mySubmittedVote,omitempty"`
	HunterCanShoot    bool              `json:"hunterCanShoot,omitempty"`
}

// SpeechProgressView tracks strict single-speaker turn order during DAY_SPEECH.
type SpeechProgressView struct {
	CurrentSpeakerSeat int   `json:"currentSpeakerSeat"`
	SpeakingOrder      []int `json:"speakingOrder"`
	SpokenSeats        []int `json:"spokenSeats"`
}

// PlayerView is the complete Fog-of-War response returned by get_my_view and /api/view.
type PlayerView struct {
	WorkspaceID      string              `json:"workspaceId"`
	ConversationID   string              `json:"conversationId"`
	Preset           Preset              `json:"preset"`
	TargetPlayers    int                 `json:"targetPlayers"`
	Phase            Phase               `json:"phase"`
	Round            int                 `json:"round"`
	EventSeq         int64               `json:"eventSeq"`
	PhaseDeadlineMs  int64               `json:"phaseDeadlineMs,omitempty"`
	PhaseTimeoutSec  int                 `json:"phaseTimeoutSec"`
	Me               MeView              `json:"me"`
	Seats            []SeatView          `json:"seats"`
	Speech           *SpeechProgressView `json:"speech,omitempty"`
	PublicChatPolicy PublicChatPolicy    `json:"publicChatPolicy"`
	Private          *PrivateView        `json:"private,omitempty"`
	VoteHistory      []VoteTallyRecord   `json:"voteHistory,omitempty"`
	PublicEvents     []PublicEvent       `json:"publicEvents,omitempty"`
	Winner           Alignment           `json:"winner,omitempty"`
	WinReason        string              `json:"winReason,omitempty"`
}

// ActionResponse wraps the outcome of any state-changing action along with the updated Fog-of-War PlayerView.
type ActionResponse struct {
	OK               bool             `json:"ok"`
	Action           string           `json:"action"`
	Message          string           `json:"message"`
	IdempotentReplay bool             `json:"idempotentReplay,omitempty"`
	PublicChatPolicy PublicChatPolicy `json:"publicChatPolicy"`
	View             PlayerView       `json:"view"`
}

// NotificationPayload is the <2KB Fog-of-War JSON-RPC notification body pushed over MCP SSE.
type NotificationPayload struct {
	EventSeq         int64            `json:"eventSeq"`
	EventType        string           `json:"eventType"`
	Phase            Phase            `json:"phase"`
	Round            int              `json:"round"`
	Seat             int              `json:"seat,omitempty"`
	Role             Role             `json:"role,omitempty"`
	PendingAction    string           `json:"pendingAction,omitempty"`
	Summary          string           `json:"summary"`
	ActionHint       string           `json:"actionHint,omitempty"`
	PublicChatPolicy PublicChatPolicy `json:"publicChatPolicy"`
}

// TargetedNotification binds a private/cropped notification to a single recipient sub in one room.
type TargetedNotification struct {
	WorkspaceID    string
	ConversationID string
	TargetUserID   string
	Payload        NotificationPayload
}

// Timings configures auto-advance timeouts for a room.
type Timings struct {
	NightTimeout       time.Duration
	DayAnnounceDelay   time.Duration
	HunterTimeout      time.Duration
	SpeakerTurnTimeout time.Duration
	VoteTimeout        time.Duration
}

// DefaultTimings returns production phase timeouts.
func DefaultTimings() Timings {
	return Timings{
		NightTimeout:       90 * time.Second,
		DayAnnounceDelay:   2500 * time.Millisecond,
		HunterTimeout:      45 * time.Second,
		SpeakerTurnTimeout: 90 * time.Second,
		VoteTimeout:        60 * time.Second,
	}
}

// RoleLabel returns the localized Chinese name of a role.
func RoleLabel(r Role) string {
	switch r {
	case RoleWerewolf:
		return "狼人"
	case RoleVillager:
		return "平民"
	case RoleSeer:
		return "预言家"
	case RoleWitch:
		return "女巫"
	case RoleHunter:
		return "猎人"
	default:
		return ""
	}
}

// PresetTargetCount returns the required player count for a preset.
func PresetTargetCount(p Preset) int {
	if p == Preset9P {
		return 9
	}
	return 6
}

// PresetDeck returns a fresh role deck slice for the given preset.
func PresetDeck(p Preset) []Role {
	if p == Preset9P {
		return []Role{
			RoleWerewolf, RoleWerewolf, RoleWerewolf,
			RoleVillager, RoleVillager, RoleVillager,
			RoleSeer, RoleWitch, RoleHunter,
		}
	}
	return []Role{
		RoleWerewolf, RoleWerewolf,
		RoleVillager, RoleVillager,
		RoleSeer, RoleWitch,
	}
}
