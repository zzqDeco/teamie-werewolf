export type Phase =
  | "LOBBY"
  | "NIGHT"
  | "DAY_ANNOUNCE"
  | "DAY_SPEECH"
  | "DAY_VOTE"
  | "GAME_OVER";

export type Role = "WEREWOLF" | "VILLAGER" | "SEER" | "WITCH" | "HUNTER";
export type Alignment = "WOLF" | "GOOD";
export type Preset = "6p" | "9p";

export interface TeamieInitPayload {
  appId: string;
  slug: string;
  name: string;
  webUrl: string;
  mcpUrl?: string;
  token: string;
  expiresAt: string;
  workspaceId: string;
  conversationId: string;
  user: {
    id: string;
    displayName: string;
  };
}

export interface PublicChatPolicy {
  allowed: boolean;
  activeSpeakerSeat?: number;
  reason: string;
}

export interface WolfChatEntry {
  seq: number;
  round: number;
  seat: number;
  displayName: string;
  message: string;
  createdAt: string;
}

export interface SeerCheckRecord {
  round: number;
  targetSeat: number;
  displayName: string;
  alignment: Alignment;
}

export interface WitchPrivateState {
  antidoteLeft: boolean;
  poisonLeft: boolean;
  canActNow: boolean;
  tonightAttackedSeat?: number;
}

export interface VoteTallyRecord {
  round: number;
  votesBySeat: Record<string, number>;
  eliminatedSeat?: number;
  isTie: boolean;
  summary: string;
}

export interface PublicEvent {
  seq: number;
  round: number;
  phase: Phase;
  summary: string;
  createdAt: string;
}

export interface SeatView {
  seat: number;
  displayName: string;
  isMe: boolean;
  alive: boolean;
  knownRole?: Role;
  isSpeaking?: boolean;
  hasSpoken?: boolean;
  hasVoted?: boolean;
}

export interface MeView {
  userId: string;
  displayName: string;
  seated: boolean;
  seat?: number;
  role?: Role;
  roleLabel?: string;
  alive: boolean;
  pendingAction?: string;
  actionHint?: string;
}

export interface PrivateView {
  wolfTeammateSeats?: number[];
  wolfIntentions?: Record<string, number>;
  wolfCanVote?: boolean;
  wolfChat?: WolfChatEntry[];
  seerChecks?: SeerCheckRecord[];
  witchState?: WitchPrivateState;
  mySubmittedVote?: number;
  hunterCanShoot?: boolean;
}

export interface SpeechProgressView {
  currentSpeakerSeat: number;
  speakingOrder: number[];
  spokenSeats: number[];
}

export interface PlayerView {
  workspaceId: string;
  conversationId: string;
  preset: Preset;
  targetPlayers: number;
  phase: Phase;
  round: number;
  eventSeq: number;
  phaseDeadlineMs?: number;
  phaseTimeoutSec: number;
  me: MeView;
  seats: SeatView[];
  speech?: SpeechProgressView;
  publicChatPolicy: PublicChatPolicy;
  private?: PrivateView;
  voteHistory?: VoteTallyRecord[];
  publicEvents?: PublicEvent[];
  winner?: Alignment;
  winReason?: string;
}

export interface ActionResponse {
  ok: boolean;
  action: string;
  errorCode?: string;
  message: string;
  idempotentReplay?: boolean;
  publicChatPolicy: PublicChatPolicy;
  view: PlayerView;
}
