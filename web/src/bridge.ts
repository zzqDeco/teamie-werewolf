import type { TeamieInitPayload } from "./types";

const FIVE_MINUTES_MS = 5 * 60 * 1000;
const MIN_REFRESH_COOLDOWN_MS = 3_000;

export interface BridgeState {
  ready: boolean;
  hostOrigin: string | null;
  context: TeamieInitPayload | null;
}

export interface BridgeOptions {
  win?: Window;
  parentWin?: Window | null;
  now?: () => number;
  onUpdate: (state: BridgeState) => void;
}

/**
 * TeamieHostBridge implements the strict Teamie Marketplace iframe postMessage handshake:
 * 1. Registers message listener BEFORE posting { type: "teamie:mcp-app:ready" } to "*".
 * 2. Strictly validates event.source === window.parent.
 * 3. Locks hostOrigin = event.origin on the first valid "teamie:mcp-app:init" message,
 *    and rejects any subsequent messages from different origins.
 * 4. Keeps payload.token strictly in component instance memory (NEVER touches localStorage/sessionStorage).
 * 5. Automatically requests "teamie:mcp-app:refresh-token" when < 5 minutes remain before expiresAt
 *    or when the backend returns HTTP 401.
 */
export class TeamieHostBridge {
  private readonly win: Window;
  private readonly parentWin: Window | null;
  private readonly now: () => number;
  private readonly onUpdate: (state: BridgeState) => void;

  private hostOrigin: string | null = null;
  private context: TeamieInitPayload | null = null;
  private expiryTimer: ReturnType<typeof setTimeout> | null = null;
  private lastRefreshReqAt = 0;
  private started = false;

  constructor(opts: BridgeOptions) {
    this.win = opts.win ?? window;
    this.parentWin = opts.parentWin !== undefined ? opts.parentWin : window.parent;
    this.now = opts.now ?? (() => Date.now());
    this.onUpdate = opts.onUpdate;
  }

  start(): void {
    if (this.started) return;
    this.started = true;
    this.win.addEventListener("message", this.handleMessage);

    if (this.parentWin && this.parentWin !== this.win) {
      this.parentWin.postMessage({ type: "teamie:mcp-app:ready" }, "*");
    }
  }

  stop(): void {
    if (!this.started) return;
    this.started = false;
    this.win.removeEventListener("message", this.handleMessage);
    this.clearExpiryTimer();
  }

  requestTokenRefresh(force = false): boolean {
    if (!this.hostOrigin || !this.parentWin || this.parentWin === this.win) {
      return false;
    }
    const nowMs = this.now();
    if (!force && nowMs - this.lastRefreshReqAt < MIN_REFRESH_COOLDOWN_MS) {
      return false;
    }
    this.lastRefreshReqAt = nowMs;
    this.parentWin.postMessage(
      { type: "teamie:mcp-app:refresh-token" },
      this.hostOrigin,
    );
    return true;
  }

  getState(): BridgeState {
    return {
      ready: this.context !== null,
      hostOrigin: this.hostOrigin,
      context: this.context,
    };
  }

  private readonly handleMessage = (event: MessageEvent): void => {
    // Red line 1: Strictly verify message comes from window.parent
    if (!this.parentWin || event.source !== this.parentWin) {
      return;
    }
    if (!event.origin || event.origin === "null") {
      return;
    }
    // Red line 2: Once hostOrigin is locked, strictly reject any other origin
    if (this.hostOrigin !== null && event.origin !== this.hostOrigin) {
      return;
    }

    const data = event.data as { type?: unknown; payload?: unknown } | null;
    if (!data || typeof data !== "object" || data.type !== "teamie:mcp-app:init") {
      return;
    }

    const payload = this.validateInitPayload(data.payload);
    if (!payload) {
      return;
    }

    if (this.hostOrigin === null) {
      this.hostOrigin = event.origin;
    }
    this.context = payload;
    this.scheduleExpiryRefresh(payload.expiresAt);
    this.onUpdate(this.getState());
  };

  private validateInitPayload(raw: unknown): TeamieInitPayload | null {
    if (!raw || typeof raw !== "object") return null;
    const p = raw as Record<string, unknown>;
    if (
      typeof p.token !== "string" ||
      !p.token.trim() ||
      typeof p.appId !== "string" ||
      typeof p.workspaceId !== "string" ||
      typeof p.conversationId !== "string" ||
      typeof p.expiresAt !== "string"
    ) {
      return null;
    }
    const u = (p.user ?? {}) as Record<string, unknown>;
    return {
      appId: p.appId,
      slug: typeof p.slug === "string" ? p.slug : "werewolf",
      name: typeof p.name === "string" ? p.name : "狼人杀裁判台",
      webUrl: typeof p.webUrl === "string" ? p.webUrl : "",
      mcpUrl: typeof p.mcpUrl === "string" ? p.mcpUrl : undefined,
      token: p.token,
      expiresAt: p.expiresAt,
      workspaceId: p.workspaceId,
      conversationId: p.conversationId,
      user: {
        id: typeof u.id === "string" ? u.id : "",
        displayName: typeof u.displayName === "string" ? u.displayName : "玩家",
      },
    };
  }

  private scheduleExpiryRefresh(expiresAtIso: string): void {
    this.clearExpiryTimer();
    const expMs = Date.parse(expiresAtIso);
    if (!Number.isFinite(expMs)) {
      return;
    }
    const remainingBeforeWindow = expMs - FIVE_MINUTES_MS - this.now();
    if (remainingBeforeWindow <= 0) {
      // Already within 5 minutes of expiry -> request silent refresh immediately
      this.requestTokenRefresh();
      return;
    }
    this.expiryTimer = setTimeout(() => {
      this.requestTokenRefresh(true);
    }, remainingBeforeWindow);
  }

  private clearExpiryTimer(): void {
    if (this.expiryTimer !== null) {
      clearTimeout(this.expiryTimer);
      this.expiryTimer = null;
    }
  }
}
