import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { fetchMyView, postGameAction, subscribeViewStream } from "./api";
import { TeamieHostBridge } from "./bridge";
import type { BridgeState } from "./bridge";
import {
  ChevronIcon,
  CrownTrophyIcon,
  EyeToggleIcon,
  MoonStarIcon,
  RoleEmblem,
  SunCrestIcon,
  WerewolfEmblem,
} from "./icons";
import type { Phase, PlayerView, Preset, Role, SeatView, VoteTallyRecord } from "./types";

const PHASE_LABELS: Record<Phase, string> = {
  LOBBY: "大厅集结",
  NIGHT: "暗夜降临",
  DAY_ANNOUNCE: "破晓宣告",
  DAY_SPEECH: "圆桌陈词",
  DAY_VOTE: "火漆暗票",
  GAME_OVER: "终局审判",
};

const ROLE_LABELS: Record<Role, string> = {
  WEREWOLF: "狼人",
  VILLAGER: "平民",
  SEER: "预言家",
  WITCH: "女巫",
  HUNTER: "猎人",
};

const ROLE_SUBTITLES: Record<Role, string> = {
  WEREWOLF: "暗夜猎手 · 暗频协同锁刀",
  VILLAGER: "神圣村民 · 听发言辨狼踪",
  SEER: "星象先知 · 每夜洞悉一人阵营",
  WITCH: "炼金女巫 · 执掌解药与毒药",
  HUNTER: "银弹猎魔人 · 出局可拔枪反击",
};

const SEAT_SEAL_COLORS = [
  "#9f6b32",
  "#7c4dff",
  "#2f7d6d",
  "#b03a48",
  "#3d6cb9",
  "#9a4d87",
  "#826a3c",
  "#387c8c",
  "#a35638",
];

type TargetingMode =
  | "none"
  | "wolf_kill"
  | "seer_check"
  | "witch_poison"
  | "hunter_shoot"
  | "day_vote";

interface GroupedVoteRow {
  targetSeat: number;
  voters: number[];
  isEliminated: boolean;
}

function groupVotes(tally: VoteTallyRecord): GroupedVoteRow[] {
  const map = new Map<number, number[]>();
  for (const [voterStr, targetRaw] of Object.entries(tally.votesBySeat)) {
    const voter = Number(voterStr);
    const target = Number(targetRaw);
    const list = map.get(target) ?? [];
    list.push(voter);
    map.set(target, list);
  }
  const rows: GroupedVoteRow[] = [];
  for (const [targetSeat, voters] of map.entries()) {
    voters.sort((a, b) => a - b);
    rows.push({
      targetSeat,
      voters,
      isEliminated: targetSeat > 0 && tally.eliminatedSeat === targetSeat,
    });
  }
  rows.sort((a, b) => {
    if (a.targetSeat === 0) return 1;
    if (b.targetSeat === 0) return -1;
    if (b.voters.length !== a.voters.length) return b.voters.length - a.voters.length;
    return a.targetSeat - b.targetSeat;
  });
  return rows;
}

function formatClock(sec: number): string {
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

export function App() {
  const [bridgeState, setBridgeState] = useState<BridgeState>({
    ready: false,
    hostOrigin: null,
    context: null,
  });
  const bridgeRef = useRef<TeamieHostBridge | null>(null);

  const [view, setView] = useState<PlayerView | null>(null);
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState<{ text: string; isErr?: boolean } | null>(null);
  const [hideRole, setHideRole] = useState(false);

  // Interactive seat targeting & collapsible chronicle dock
  const [selectedSeat, setSelectedSeat] = useState<number | null>(null);
  const [chronicleOpen, setChronicleOpen] = useState(false);
  const [activeTab, setActiveTab] = useState<"events" | "votes">("events");

  // Local inputs & visual timer
  const [wolfDraft, setWolfDraft] = useState("");
  const [speechNote, setSpeechNote] = useState("");
  const [nowTickMs, setNowTickMs] = useState(() => Date.now());
  const [deadlineSpanMs, setDeadlineSpanMs] = useState(60_000);
  const wolfStreamRef = useRef<HTMLDivElement | null>(null);

  // 1. Mount Teamie postMessage bridge (strictly memory-scoped token)
  useEffect(() => {
    const bridge = new TeamieHostBridge({
      onUpdate: (next) => setBridgeState(next),
    });
    bridgeRef.current = bridge;
    bridge.start();
    return () => {
      bridge.stop();
      bridgeRef.current = null;
    };
  }, []);

  const requestRefresh = useCallback(() => {
    bridgeRef.current?.requestTokenRefresh();
  }, []);

  const token = bridgeState.context?.token ?? "";

  // 2. Fetch initial view + subscribe to authenticated SSE stream whenever token updates
  useEffect(() => {
    if (!token) return;
    let cancelled = false;

    void fetchMyView(token, requestRefresh)
      .then((v) => {
        if (!cancelled) setView(v);
      })
      .catch((err: unknown) => {
        if (!cancelled && err instanceof Error) {
          setFeedback({ text: err.message, isErr: true });
        }
      });

    const unsubscribe = subscribeViewStream({
      token,
      onView: (v) => {
        if (!cancelled) setView(v);
      },
      onUnauthorized: requestRefresh,
    });

    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [token, requestRefresh]);

  // Capture initial span whenever phaseDeadlineMs changes so progress bar scales smoothly
  useEffect(() => {
    if (!view?.phaseDeadlineMs) return;
    const now = Date.now();
    setNowTickMs(now);
    const fallbackMs = Math.max(15, view.phaseTimeoutSec || 60) * 1000;
    setDeadlineSpanMs(Math.max(fallbackMs, view.phaseDeadlineMs - now));
    const id = window.setInterval(() => setNowTickMs(Date.now()), 400);
    return () => window.clearInterval(id);
  }, [view?.phaseDeadlineMs, view?.phaseTimeoutSec]);

  // Reset local manual seat aim when phase or round shifts
  useEffect(() => {
    setSelectedSeat(null);
  }, [view?.phase, view?.round, view?.private?.hunterCanShoot]);

  // Keep wolf encrypted chat scrolled to newest entry
  useEffect(() => {
    if (wolfStreamRef.current) {
      wolfStreamRef.current.scrollTop = wolfStreamRef.current.scrollHeight;
    }
  }, [view?.private?.wolfChat?.length]);

  const remainingSec = useMemo(() => {
    if (!view?.phaseDeadlineMs) return null;
    return Math.max(0, Math.ceil((view.phaseDeadlineMs - nowTickMs) / 1000));
  }, [view?.phaseDeadlineMs, nowTickMs]);

  const countdownPct = useMemo(() => {
    if (!view?.phaseDeadlineMs || deadlineSpanMs <= 0) return 0;
    const left = Math.max(0, view.phaseDeadlineMs - nowTickMs);
    return Math.min(100, Math.max(0, (left / deadlineSpanMs) * 100));
  }, [view?.phaseDeadlineMs, nowTickMs, deadlineSpanMs]);

  const runAction = useCallback(
    async (actionName: string, body: Record<string, unknown> = {}) => {
      if (!token || busy) return;
      setBusy(true);
      setFeedback(null);
      try {
        const res = await postGameAction(token, actionName, body, requestRefresh);
        setView(res.view);
        if (res.message) {
          setFeedback({ text: res.message, isErr: !res.ok });
        }
      } catch (err: unknown) {
        setFeedback({
          text: err instanceof Error ? err.message : "请求执行失败",
          isErr: true,
        });
      } finally {
        setBusy(false);
      }
    },
    [token, busy, requestRefresh],
  );

  // Handshake Waiting Screen
  if (!bridgeState.ready || !bridgeState.context) {
    return (
      <main className="ww-shell">
        <section className="ww-wait-screen">
          <div className="ww-crest-ring">
            <WerewolfEmblem className="ww-emblem-svg" style={{ width: 28, height: 28 }} />
          </div>
          <strong style={{ fontSize: "15px", color: "var(--gold-bright)" }}>
            狼人杀 · 暗金裁判台
          </strong>
          <span style={{ fontSize: "12.5px", color: "var(--text-secondary)", maxWidth: 260 }}>
            正在等待 Teamie 群聊宿主完成安全握手（<code>teamie:mcp-app:init</code>）…
          </span>
          <small style={{ fontSize: "11.5px", color: "var(--text-faint)", maxWidth: 250 }}>
            请在 Teamie 群聊右侧「群聊应用」抽屉中打开本应用。
          </small>
        </section>
      </main>
    );
  }

  if (!view) {
    return (
      <main className="ww-shell">
        <section className="ww-wait-screen">
          <div className="ww-crest-ring">
            <RoleEmblem className="ww-emblem-svg" />
          </div>
          <strong style={{ color: "var(--gold-bright)" }}>正在铺设命运牌桌…</strong>
        </section>
      </main>
    );
  }

  const { me, seats, phase, round, preset, targetPlayers, publicChatPolicy, private: priv } = view;
  const isMyMic =
    phase === "DAY_SPEECH" &&
    me.seated &&
    me.alive &&
    view.speech?.currentSpeakerSeat === me.seat;

  const canConfigureLobby =
    phase === "LOBBY" && (seats.length === 0 || seats[0]?.isMe);

  // Determine current interactive seat targeting mode
  const wolfCanAct =
    phase === "NIGHT" &&
    me.seated &&
    me.alive &&
    me.role === "WEREWOLF" &&
    (me.pendingAction === "cast_night_action" || Boolean(priv?.wolfCanVote));

  const seerCanAct =
    phase === "NIGHT" &&
    me.seated &&
    me.alive &&
    me.role === "SEER" &&
    me.pendingAction === "cast_night_action";

  const witchCanPoison =
    phase === "NIGHT" &&
    me.seated &&
    me.alive &&
    me.role === "WITCH" &&
    Boolean(priv?.witchState?.canActNow && priv.witchState.poisonLeft);

  let targetingMode: TargetingMode = "none";
  if (priv?.hunterCanShoot) {
    targetingMode = "hunter_shoot";
  } else if (wolfCanAct) {
    targetingMode = "wolf_kill";
  } else if (seerCanAct) {
    targetingMode = "seer_check";
  } else if (witchCanPoison) {
    targetingMode = "witch_poison";
  } else if (phase === "DAY_VOTE" && me.seated && me.alive) {
    targetingMode = "day_vote";
  }

  const isDangerAim =
    targetingMode === "wolf_kill" ||
    targetingMode === "witch_poison" ||
    targetingMode === "hunter_shoot";

  const canSelectSeat = (s: SeatView): boolean => {
    if (!s.alive) return false;
    switch (targetingMode) {
      case "wolf_kill":
      case "day_vote":
        return true;
      case "seer_check":
      case "witch_poison":
        return !s.isMe;
      case "hunter_shoot":
        return s.seat !== me.seat;
      default:
        return false;
    }
  };

  const myWolfPick =
    me.seat !== undefined ? priv?.wolfIntentions?.[String(me.seat)] : undefined;

  const effectiveTargetSeat: number | null = (() => {
    if (selectedSeat !== null) {
      const found = seats.find((s) => s.seat === selectedSeat);
      if (found && canSelectSeat(found)) return selectedSeat;
    }
    if (targetingMode === "wolf_kill" && myWolfPick && myWolfPick > 0) {
      const found = seats.find((s) => s.seat === myWolfPick);
      if (found && found.alive) return myWolfPick;
    }
    if (
      targetingMode === "day_vote" &&
      priv?.mySubmittedVote &&
      priv.mySubmittedVote > 0
    ) {
      const found = seats.find((s) => s.seat === priv.mySubmittedVote);
      if (found && found.alive) return priv.mySubmittedVote;
    }
    return null;
  })();

  const targetHintLabel: Record<TargetingMode, string> = {
    none: "",
    wolf_kill: "点击座位瞄准袭击目标",
    seer_check: "点击其他存活座位查验",
    witch_poison: "点击座位可用毒药",
    hunter_shoot: "点击存活座位开枪带走",
    day_vote: "点击座位盖印放逐暗票",
  };

  // Build private role subtitle for the Tarot identity plaque
  const buildIdentitySub = (): string => {
    if (!me.role) return "等待全员入座发牌";
    if (hideRole) return "底牌已暗置防窥 · 点击翻开查看身份与线索";
    if (me.role === "WEREWOLF" && priv?.wolfTeammateSeats) {
      const mates = priv.wolfTeammateSeats.filter((n) => n !== me.seat);
      return mates.length > 0
        ? `狼同伴：${mates.map((n) => `${n}号`).join("、")} · 夜间可暗频协商`
        : "独狼在场 · 夜间独立锁定刀口";
    }
    if (me.role === "SEER" && priv?.seerChecks && priv.seerChecks.length > 0) {
      const last = priv.seerChecks[priv.seerChecks.length - 1];
      return `最近查验：${last.targetSeat}号 → ${last.alignment === "WOLF" ? "查杀(狼)" : "金水(好人)"}`;
    }
    if (me.role === "WITCH" && priv?.witchState) {
      return `解药：${priv.witchState.antidoteLeft ? "在手" : "已用"} · 毒药：${priv.witchState.poisonLeft ? "在手" : "已用"}`;
    }
    return ROLE_SUBTITLES[me.role];
  };

  const latestEventText =
    view.publicEvents && view.publicEvents.length > 0
      ? view.publicEvents[view.publicEvents.length - 1].summary
      : "牌桌就绪，等待玩家入座开启对局";

  const latestVoteTally =
    view.voteHistory && view.voteHistory.length > 0
      ? view.voteHistory[view.voteHistory.length - 1]
      : null;

  const emptySeatCount =
    phase === "LOBBY" ? Math.max(0, targetPlayers - seats.length) : 0;

  return (
    <main
      className={`ww-shell ${
        phase === "NIGHT" ? "is-night" : phase === "GAME_OVER" ? "is-over" : ""
      }`}
    >
      {/* 1. Sticky Ornate Header + Fluid Countdown Progress */}
      <header className="ww-topbar">
        <div className="ww-topbar-inner">
          <span
            className={`ww-phase-crest ${
              phase === "NIGHT" ? "night" : phase === "GAME_OVER" ? "over" : ""
            }`}
          >
            {phase === "NIGHT" ? (
              <MoonStarIcon className="ww-phase-icon" />
            ) : phase === "GAME_OVER" ? (
              <CrownTrophyIcon className="ww-phase-icon" />
            ) : (
              <SunCrestIcon className="ww-phase-icon" />
            )}
            <span>
              {round > 0 ? `第${round}${phase === "NIGHT" ? "夜" : "天"} · ` : ""}
              {PHASE_LABELS[phase]}
            </span>
          </span>

          <div className="ww-topbar-meta">
            <span className="ww-preset-pill">
              {preset === "9p" ? "9人猎人局" : "6人标准局"}
            </span>
            {remainingSec !== null && phase !== "LOBBY" && phase !== "GAME_OVER" && (
              <span className={`ww-timer-pill ${remainingSec <= 15 ? "urgent" : ""}`}>
                {formatClock(remainingSec)}
              </span>
            )}
          </div>
        </div>

        {remainingSec !== null && phase !== "LOBBY" && phase !== "GAME_OVER" && (
          <div className="ww-countdown-track" aria-hidden="true">
            <div
              className={`ww-countdown-fill ${remainingSec <= 15 ? "urgent" : ""}`}
              style={{ width: `${countdownPct}%` }}
            />
          </div>
        )}
      </header>

      {/* 2. Main Scrollable Tarot Stage */}
      <div className="ww-stage">
        {/* Tarot Identity Card (or Lobby Seat Strip) */}
        {me.role ? (
          <button
            type="button"
            className={`ww-tarot-id ${
              hideRole ? "is-concealed" : `role-${me.role}`
            }`}
            onClick={() => setHideRole((v) => !v)}
            title="点击翻转隐藏/展开底牌（防窥屏）"
          >
            <div className="ww-id-left">
              <div className="ww-emblem-medallion">
                <RoleEmblem
                  role={hideRole ? undefined : me.role}
                  className="ww-emblem-svg"
                />
              </div>
              <div className="ww-id-copy">
                <div className="ww-id-title">
                  {me.seat ? `${me.seat}号 · ` : ""}
                  {hideRole
                    ? "命运底牌已暗置"
                    : `我的底牌：${me.roleLabel || ROLE_LABELS[me.role]}`}
                  {me.seated && !me.alive ? "（已出局）" : ""}
                </div>
                <div className="ww-id-sub">{buildIdentitySub()}</div>
              </div>
            </div>

            <span className="ww-id-toggle">
              <EyeToggleIcon hidden={hideRole} className="ww-phase-icon" />
              <span>{hideRole ? "翻开" : "暗置"}</span>
            </span>
          </button>
        ) : (
          <div className="ww-lobby-id-strip">
            <div className="ww-id-left">
              <div className="ww-emblem-medallion">
                <RoleEmblem className="ww-emblem-svg" />
              </div>
              <div className="ww-id-copy">
                <div className="ww-id-title">
                  {me.seated ? `${me.seat}号 · ${me.displayName}` : me.displayName}
                </div>
                <div className="ww-id-sub">
                  {me.seated ? "已入座等待发牌" : "观战席 · 入座后参与发牌博弈"}
                </div>
              </div>
            </div>
            <span className={`ww-badge ${me.seated ? "gold" : ""}`}>
              {me.seated ? `${me.seat}号席` : "未入座"}
            </span>
          </div>
        )}

        {/* Public Chat Discipline Banner */}
        <section
          className={`ww-policy-strip ${
            !publicChatPolicy.allowed
              ? "locked"
              : isMyMic
                ? "my-mic"
                : phase === "DAY_SPEECH"
                  ? "wait-mic"
                  : ""
          }`}
        >
          <span aria-hidden="true">
            {!publicChatPolicy.allowed ? "🤫" : isMyMic ? "🎙️" : "⚖️"}
          </span>
          <span>{publicChatPolicy.reason}</span>
        </section>

        {/* Feedback Toast */}
        {feedback && (
          <div className={`ww-toast ${feedback.isErr ? "err" : ""}`} role="status">
            <span>{feedback.text}</span>
            <button
              type="button"
              onClick={() => setFeedback(null)}
              style={{
                background: "transparent",
                border: 0,
                color: "inherit",
                cursor: "pointer",
                padding: "0 2px",
                fontWeight: 700,
              }}
              aria-label="关闭提示"
            >
              ✕
            </button>
          </div>
        )}

        {/* 2-Column Interactive Seat Plaque Grid */}
        <section>
          <div className="ww-block-header">
            <span className="ww-block-title">
              圆桌席位（{seats.length}/{targetPlayers}）
            </span>
            {targetingMode !== "none" ? (
              <span className="ww-block-hint">{targetHintLabel[targetingMode]}</span>
            ) : view.speech && phase === "DAY_SPEECH" ? (
              <span className="ww-block-hint">
                当前持麦：{view.speech.currentSpeakerSeat}号
              </span>
            ) : null}
          </div>

          <div className="ww-seats-grid">
            {seats.map((s) => {
              const selectable = canSelectSeat(s);
              const isAimed = selectable && effectiveTargetSeat === s.seat;
              const sealBg = s.alive
                ? SEAT_SEAL_COLORS[(s.seat - 1) % SEAT_SEAL_COLORS.length]
                : "#47424f";

              const seerRecord =
                !hideRole && priv?.seerChecks
                  ? priv.seerChecks.find((c) => c.targetSeat === s.seat)
                  : undefined;

              const statusText = !s.alive
                ? "✕ 已出局"
                : s.isSpeaking
                  ? "正在发言…"
                  : phase === "DAY_VOTE" && s.hasVoted
                    ? "✓ 已盖暗票"
                    : !s.isSpeaking && s.hasSpoken
                      ? "✓ 已发言"
                      : s.isMe
                        ? "我 · 在场"
                        : "在场存活";

              return (
                <button
                  key={s.seat}
                  type="button"
                  disabled={!selectable}
                  onClick={() => {
                    if (!selectable) return;
                    setSelectedSeat((prev) => (prev === s.seat ? null : s.seat));
                  }}
                  className={[
                    "ww-seat-card",
                    selectable ? "can-select" : "",
                    s.isMe ? "is-me" : "",
                    s.isSpeaking ? "is-speaking" : "",
                    !s.alive ? "is-dead" : "",
                    isAimed ? "is-selected" : "",
                    isAimed && isDangerAim ? "danger-aim" : "",
                  ]
                    .filter(Boolean)
                    .join(" ")}
                >
                  <div className="ww-seat-row-top">
                    <div className="ww-seat-identity">
                      <span className="ww-seat-seal" style={{ background: sealBg }}>
                        {s.seat}
                      </span>
                      <span className="ww-seat-title" title={s.displayName}>
                        {s.displayName}
                      </span>
                    </div>

                    {s.isSpeaking && (
                      <span className="ww-eq-bars" title="正在发言">
                        <span className="ww-eq-bar b1" />
                        <span className="ww-eq-bar b2" />
                        <span className="ww-eq-bar b3" />
                      </span>
                    )}
                  </div>

                  <div className="ww-seat-row-bottom">
                    <span className="ww-seat-status">{statusText}</span>

                    {isAimed ? (
                      <span
                        className={`ww-badge ${
                          isDangerAim ? "aim-danger" : "aim-gold"
                        }`}
                      >
                        ◎ 已瞄准
                      </span>
                    ) : s.knownRole && (!hideRole || phase === "GAME_OVER") ? (
                      <span
                        className={`ww-badge ${
                          s.knownRole === "WEREWOLF" ? "wolf" : "good"
                        }`}
                      >
                        {ROLE_LABELS[s.knownRole]}
                      </span>
                    ) : seerRecord ? (
                      <span
                        className={`ww-badge ${
                          seerRecord.alignment === "WOLF" ? "wolf" : "good"
                        }`}
                      >
                        {seerRecord.alignment === "WOLF" ? "查杀" : "金水"}
                      </span>
                    ) : s.isMe ? (
                      <span className="ww-badge gold">本尊</span>
                    ) : null}
                  </div>
                </button>
              );
            })}

            {/* Visual Empty Tarot Placeholders in Lobby */}
            {Array.from({ length: emptySeatCount }).map((_, idx) => {
              const slotNum = seats.length + idx + 1;
              return (
                <div key={`empty-${slotNum}`} className="ww-seat-card is-empty-slot">
                  <div className="ww-seat-row-top">
                    <div className="ww-seat-identity">
                      <span
                        className="ww-seat-seal"
                        style={{ background: "rgba(212, 175, 99, 0.14)", color: "var(--text-muted)" }}
                      >
                        {slotNum}
                      </span>
                      <span className="ww-seat-status">虚位以待</span>
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        </section>

        {/* ================================================================
            Contextual Tarot Action Altars
            ================================================================ */}

        {/* LOBBY ALTAR */}
        {phase === "LOBBY" && (
          <section className="ww-altar">
            <div className="ww-altar-head">
              <span className="ww-altar-title">牌局契约配置</span>
              <span className="ww-altar-sub">
                {preset === "9p" ? "3狼 · 3民 · 预女猎" : "2狼 · 2民 · 预女"}
              </span>
            </div>

            {canConfigureLobby && (
              <div className="ww-segmented" role="group" aria-label="选择板子人数">
                {(["6p", "9p"] as Preset[]).map((p) => (
                  <button
                    key={p}
                    type="button"
                    disabled={busy || (p === "6p" && seats.length > 6)}
                    className={`ww-seg-item ${preset === p ? "active" : ""}`}
                    onClick={() => void runAction("configure_lobby", { preset: p })}
                  >
                    {p === "6p" ? "6人标准局" : "9人猎人局"}
                  </button>
                ))}
              </div>
            )}

            <div className="ww-btn-row">
              {!me.seated ? (
                <button
                  type="button"
                  disabled={busy || seats.length >= targetPlayers}
                  className="ww-btn ww-btn-gold ww-w-full"
                  onClick={() => void runAction("join_lobby", { preset })}
                >
                  入座缔结契约（{seats.length}/{targetPlayers}）
                </button>
              ) : (
                <>
                  <button
                    type="button"
                    disabled={busy}
                    className="ww-btn"
                    onClick={() => void runAction("leave_lobby")}
                  >
                    离席
                  </button>
                  <button
                    type="button"
                    disabled={busy || seats.length !== targetPlayers}
                    className="ww-btn ww-btn-gold ww-flex-1"
                    onClick={() => void runAction("start_game", { preset })}
                  >
                    {seats.length === targetPlayers
                      ? "✦ 洗牌发牌 · 开启首夜"
                      : `等待满员 (${seats.length}/${targetPlayers})`}
                  </button>
                </>
              )}
            </div>
          </section>
        )}

        {/* NIGHT ALTAR */}
        {phase === "NIGHT" && me.seated && (
          <section className="ww-altar">
            <div className="ww-altar-head">
              <span className="ww-altar-title">
                <MoonStarIcon className="ww-phase-icon" />
                <span>暗夜行动圣坛</span>
              </span>
              <span className="ww-altar-sub">
                {me.pendingAction ? "轮到你行动" : "静候破晓"}
              </span>
            </div>

            {/* WEREWOLF SANCTUM */}
            {me.role === "WEREWOLF" && (
              <>
                <div className="ww-wolf-sanctum">
                  <div className="ww-altar-head">
                    <span
                      style={{
                        fontSize: "12px",
                        fontWeight: 700,
                        color: "#ff9ca7",
                        display: "inline-flex",
                        alignItems: "center",
                        gap: "5px",
                      }}
                    >
                      <WerewolfEmblem className="ww-phase-icon" />
                      <span>狼人血契暗频（仅狼队可见）</span>
                    </span>
                    {priv?.wolfIntentions &&
                      Object.keys(priv.wolfIntentions).length > 0 && (
                        <span style={{ fontSize: "11px", color: "#e8a8b0" }}>
                          刀口：
                          {Object.entries(priv.wolfIntentions)
                            .map(([w, t]) =>
                              Number(t) === 0 ? `${w}号→空刀` : `${w}号→${t}号`,
                            )
                            .join("，")}
                        </span>
                      )}
                  </div>

                  <div className="ww-wolf-stream" ref={wolfStreamRef}>
                    {!priv?.wolfChat || priv.wolfChat.length === 0 ? (
                      <span style={{ fontSize: "11.5px", color: "#d49ba2", opacity: 0.8 }}>
                        暗频已加密接通，先和狼队友统一今夜袭击目标吧。
                      </span>
                    ) : (
                      priv.wolfChat.map((c) => {
                        const isMine = c.seat === me.seat;
                        return (
                          <div
                            key={c.seq}
                            className={`ww-chat-item ${isMine ? "mine" : ""}`}
                          >
                            <span className="ww-chat-meta">
                              {c.seat}号 · {c.displayName}
                            </span>
                            <div className="ww-chat-bubble">{c.message}</div>
                          </div>
                        );
                      })
                    )}
                  </div>

                  {me.alive && (
                    <form
                      className="ww-btn-row"
                      onSubmit={(e) => {
                        e.preventDefault();
                        const text = wolfDraft.trim();
                        if (!text) return;
                        setWolfDraft("");
                        void runAction("wolf_chat", { message: text });
                      }}
                    >
                      <input
                        className="ww-input ww-flex-1"
                        placeholder="发送暗频战术（公屏不可见）…"
                        value={wolfDraft}
                        maxLength={240}
                        onChange={(e) => setWolfDraft(e.target.value)}
                      />
                      <button
                        type="submit"
                        disabled={busy || !wolfDraft.trim()}
                        className="ww-btn"
                      >
                        密语
                      </button>
                    </form>
                  )}
                </div>

                {wolfCanAct && (
                  <div className="ww-btn-row">
                    <button
                      type="button"
                      disabled={busy}
                      className={`ww-btn ${myWolfPick === 0 && selectedSeat === null ? "ww-btn-active" : ""}`}
                      onClick={() => {
                        setSelectedSeat(null);
                        void runAction("cast_night_action", {
                          actionType: "pass",
                          targetSeat: 0,
                        });
                      }}
                    >
                      放弃空刀
                    </button>
                    <button
                      type="button"
                      disabled={busy || effectiveTargetSeat === null}
                      className="ww-btn ww-btn-crimson ww-flex-1"
                      onClick={() => {
                        if (effectiveTargetSeat === null) return;
                        void runAction("cast_night_action", {
                          actionType: "kill",
                          targetSeat: effectiveTargetSeat,
                        });
                      }}
                    >
                      {effectiveTargetSeat !== null
                        ? myWolfPick === effectiveTargetSeat
                          ? `✓ 已选袭击 ${effectiveTargetSeat}号（狼队一致前可改）`
                          : myWolfPick !== undefined
                            ? `🗡️ 改选袭击 ${effectiveTargetSeat}号`
                            : `🗡️ 锁定袭击 ${effectiveTargetSeat}号`
                        : "请先在上方点击袭击座位"}
                    </button>
                  </div>
                )}
              </>
            )}

            {/* SEER SANCTUM */}
            {me.role === "SEER" && (
              <>
                {seerCanAct ? (
                  <button
                    type="button"
                    disabled={busy || effectiveTargetSeat === null}
                    className="ww-btn ww-btn-gold ww-w-full"
                    onClick={() => {
                      if (effectiveTargetSeat === null) return;
                      void runAction("cast_night_action", {
                        actionType: "check",
                        targetSeat: effectiveTargetSeat,
                      });
                    }}
                  >
                    {effectiveTargetSeat !== null
                      ? `✦ 启示查验 ${effectiveTargetSeat}号 阵营`
                      : "请先在上方点击要查验的玩家座位"}
                  </button>
                ) : (
                  <div className="ww-sub-box" style={{ color: "var(--text-muted)" }}>
                    本夜星象查验已完成，请静候破晓后再择机报查验。
                  </div>
                )}

                {priv?.seerChecks && priv.seerChecks.length > 0 && (
                  <div className="ww-sub-box">
                    <div
                      style={{
                        fontSize: "11.5px",
                        fontWeight: 700,
                        color: "var(--gold-bright)",
                        marginBottom: "4px",
                      }}
                    >
                      先知查验卷轴
                    </div>
                    {priv.seerChecks.map((r) => (
                      <div
                        key={r.round}
                        style={{
                          display: "flex",
                          justifyContent: "space-between",
                          padding: "2px 0",
                        }}
                      >
                        <span>
                          第{r.round}夜 · {r.targetSeat}号 {r.displayName}
                        </span>
                        <b
                          style={{
                            color:
                              r.alignment === "WOLF"
                                ? "var(--crimson)"
                                : "var(--emerald)",
                          }}
                        >
                          {r.alignment === "WOLF" ? "狼人（查杀）" : "好人（金水）"}
                        </b>
                      </div>
                    ))}
                  </div>
                )}
              </>
            )}

            {/* WITCH SANCTUM */}
            {me.role === "WITCH" && priv?.witchState && (
              <>
                <div className="ww-btn-row">
                  <span
                    className={`ww-badge ${
                      priv.witchState.antidoteLeft ? "good" : ""
                    }`}
                  >
                    解药：{priv.witchState.antidoteLeft ? "可用" : "已耗尽"}
                  </span>
                  <span
                    className={`ww-badge ${
                      priv.witchState.poisonLeft ? "wolf" : ""
                    }`}
                  >
                    毒药：{priv.witchState.poisonLeft ? "可用" : "已耗尽"}
                  </span>
                </div>

                {me.alive && priv.witchState.canActNow ? (
                  <>
                    {priv.witchState.antidoteLeft && (
                      <div className="ww-sub-box">
                        {priv.witchState.tonightAttackedSeat &&
                        priv.witchState.tonightAttackedSeat > 0 ? (
                          <div className="ww-btn-row" style={{ justifyContent: "space-between" }}>
                            <span>
                              今夜 <b>{priv.witchState.tonightAttackedSeat}号</b> 遇袭倒牌
                            </span>
                            <button
                              type="button"
                              disabled={busy}
                              className="ww-btn ww-btn-emerald"
                              onClick={() =>
                                void runAction("cast_night_action", {
                                  actionType: "save",
                                })
                              }
                            >
                              赐予解药救起
                            </button>
                          </div>
                        ) : (
                          <span style={{ color: "var(--text-muted)" }}>
                            今夜狼人选择空刀，无人遇袭。
                          </span>
                        )}
                      </div>
                    )}

                    <div className="ww-btn-row">
                      <button
                        type="button"
                        disabled={busy}
                        className="ww-btn ww-flex-1"
                        onClick={() =>
                          void runAction("cast_night_action", { actionType: "pass" })
                        }
                      >
                        不用药（跳过）
                      </button>

                      {priv.witchState.poisonLeft && (
                        <button
                          type="button"
                          disabled={busy || effectiveTargetSeat === null}
                          className="ww-btn ww-btn-crimson ww-flex-1"
                          onClick={() => {
                            if (effectiveTargetSeat === null) return;
                            void runAction("cast_night_action", {
                              actionType: "poison",
                              targetSeat: effectiveTargetSeat,
                            });
                          }}
                        >
                          {effectiveTargetSeat !== null
                            ? `☠️ 毒杀 ${effectiveTargetSeat}号`
                            : "点座位用毒"}
                        </button>
                      )}
                    </div>
                  </>
                ) : (
                  <div className="ww-sub-box" style={{ color: "var(--text-muted)" }}>
                    {me.actionHint || "正在等待狼人与预言家回合结束…"}
                  </div>
                )}
              </>
            )}

            {/* VILLAGER / HUNTER / ELIMINATED IDLE NIGHT */}
            {(me.role === "VILLAGER" || me.role === "HUNTER" || !me.alive) && (
              <div className="ww-sub-box" style={{ color: "var(--text-muted)" }}>
                {me.actionHint || "夜幕深沉，请闭眼静候神狼博弈结束迎来黎明。"}
              </div>
            )}
          </section>
        )}

        {/* HUNTER REVENGE ALTAR */}
        {priv?.hunterCanShoot && (
          <section className="ww-altar danger-altar">
            <div className="ww-altar-head">
              <span className="ww-altar-title" style={{ color: "#ff9ca7" }}>
                猎魔银弹 · 临终反击触发
              </span>
            </div>
            <div style={{ fontSize: "12px", color: "var(--text-secondary)" }}>
              请点击上方存活座位瞄准带走目标，或选择压枪不开枪：
            </div>
            <div className="ww-btn-row">
              <button
                type="button"
                disabled={busy}
                className="ww-btn"
                onClick={() => void runAction("hunter_shoot", { targetSeat: 0 })}
              >
                压枪不开枪
              </button>
              <button
                type="button"
                disabled={busy || effectiveTargetSeat === null}
                className="ww-btn ww-btn-crimson ww-flex-1"
                onClick={() => {
                  if (effectiveTargetSeat === null) return;
                  void runAction("hunter_shoot", { targetSeat: effectiveTargetSeat });
                }}
              >
                {effectiveTargetSeat !== null
                  ? `🎯 开枪带走 ${effectiveTargetSeat}号`
                  : "请先在上方点击带走目标"}
              </button>
            </div>
          </section>
        )}

        {/* DAY_SPEECH MIC STEPPER ALTAR */}
        {phase === "DAY_SPEECH" && view.speech && (
          <section className="ww-altar">
            <div className="ww-altar-head">
              <span className="ww-altar-title">圆桌单人麦序轨</span>
              <span className="ww-altar-sub">
                进度 {view.speech.spokenSeats.length}/{view.speech.speakingOrder.length}
              </span>
            </div>

            <div className="ww-mic-track">
              {view.speech.speakingOrder.map((seatNum, idx) => {
                const isDone = view.speech?.spokenSeats.includes(seatNum);
                const isCurr = view.speech?.currentSpeakerSeat === seatNum;
                return (
                  <span
                    key={seatNum}
                    style={{ display: "inline-flex", alignItems: "center", gap: "4px" }}
                  >
                    <span
                      className={`ww-mic-node ${
                        isCurr ? "current" : isDone ? "done" : ""
                      }`}
                    >
                      {isDone ? "✓" : isCurr ? "🎙️" : ""}
                      {seatNum}号
                    </span>
                    {idx < (view.speech?.speakingOrder.length ?? 0) - 1 && (
                      <span className="ww-mic-arrow">→</span>
                    )}
                  </span>
                );
              })}
            </div>

            {isMyMic ? (
              <>
                <input
                  className="ww-input"
                  placeholder="可选：填写一句话站边/归票摘要（计入编年记录）"
                  value={speechNote}
                  maxLength={80}
                  onChange={(e) => setSpeechNote(e.target.value)}
                />
                <button
                  type="button"
                  disabled={busy}
                  className="ww-btn ww-btn-gold ww-w-full"
                  onClick={() => {
                    const summary = speechNote.trim();
                    setSpeechNote("");
                    void runAction("finish_speech", summary ? { summary } : {});
                  }}
                >
                  ✓ 群聊陈词完毕 · 交麦给下一位
                </button>
              </>
            ) : (
              <div className="ww-sub-box" style={{ color: "var(--text-muted)" }}>
                当前由 <b>{view.speech.currentSpeakerSeat}号</b> 在左侧群聊陈词，请待其交麦后再发言。
              </div>
            )}
          </section>
        )}

        {/* DAY_VOTE SECRET BALLOT ALTAR */}
        {phase === "DAY_VOTE" && !priv?.hunterCanShoot && (
          <section className="ww-altar">
            <div className="ww-altar-head">
              <span className="ww-altar-title">火漆放逐暗票台</span>
              {priv?.mySubmittedVote !== undefined && (
                <span className="ww-badge gold">
                  已盖印：{priv.mySubmittedVote === 0 ? "弃票" : `${priv.mySubmittedVote}号`}
                </span>
              )}
            </div>

            {me.seated && me.alive ? (
              <>
                <div style={{ fontSize: "11.5px", color: "var(--text-muted)" }}>
                  开票前全员选票严格保密，结算前可随时点击座位改票（平票无人出局）：
                </div>
                <div className="ww-btn-row">
                  <button
                    type="button"
                    disabled={busy}
                    className={`ww-btn ${
                      priv?.mySubmittedVote === 0 && selectedSeat === null
                        ? "ww-btn-active"
                        : ""
                    }`}
                    onClick={() => {
                      setSelectedSeat(null);
                      void runAction("submit_vote", { targetSeat: 0 });
                    }}
                  >
                    弃票
                  </button>

                  <button
                    type="button"
                    disabled={busy || effectiveTargetSeat === null}
                    className="ww-btn ww-btn-gold ww-flex-1"
                    onClick={() => {
                      if (effectiveTargetSeat === null) return;
                      void runAction("submit_vote", {
                        targetSeat: effectiveTargetSeat,
                      });
                    }}
                  >
                    {effectiveTargetSeat !== null
                      ? priv?.mySubmittedVote === effectiveTargetSeat
                        ? `✓ 已暗投 ${effectiveTargetSeat}号（点其他座位可改票）`
                        : priv?.mySubmittedVote !== undefined
                          ? `🗳️ 改投 ${effectiveTargetSeat}号 放逐`
                          : `🗳️ 盖印放逐 ${effectiveTargetSeat}号`
                      : "请先在上方点击放逐目标座位"}
                  </button>
                </div>
              </>
            ) : (
              <div className="ww-sub-box" style={{ color: "var(--text-muted)" }}>
                正在等待场上存活玩家完成火漆暗票投递…
              </div>
            )}
          </section>
        )}

        {/* GAME_OVER VICTORY ALTAR */}
        {phase === "GAME_OVER" && (
          <section className="ww-altar victory-altar">
            <div className="ww-altar-head">
              <span className="ww-altar-title">
                <CrownTrophyIcon className="ww-phase-icon" />
                <span>
                  {view.winner === "GOOD" ? "好人阵营获胜！" : "狼人阵营获胜！"}
                </span>
              </span>
              <span className="ww-badge gold">全场底牌已揭晓</span>
            </div>

            <div style={{ fontSize: "12.5px", color: "var(--text-secondary)" }}>
              {view.winReason}
            </div>

            {latestVoteTally && (
              <div className="ww-vote-round">
                <div style={{ fontSize: "11.5px", fontWeight: 700, color: "var(--gold)" }}>
                  第{latestVoteTally.round}天终局票型
                </div>
                {groupVotes(latestVoteTally).map((row) => (
                  <div key={row.targetSeat} className="ww-vote-flow-row">
                    <span className="ww-vote-voters">
                      {row.voters.map((v) => `${v}号`).join("、")}
                    </span>
                    <span className={`ww-vote-target ${row.isEliminated ? "out" : ""}`}>
                      ──▶{" "}
                      {row.targetSeat === 0
                        ? `弃票 (${row.voters.length})`
                        : `${row.targetSeat}号 (${row.voters.length}票${row.isEliminated ? "·出局" : ""})`}
                    </span>
                  </div>
                ))}
              </div>
            )}

            <div className="ww-btn-row">
              {me.seated && (
                <button
                  type="button"
                  disabled={busy}
                  className="ww-btn"
                  onClick={() => void runAction("leave_lobby")}
                >
                  离开牌桌
                </button>
              )}
              <button
                type="button"
                disabled={busy}
                className="ww-btn ww-btn-gold ww-flex-1"
                onClick={() => void runAction("join_lobby", { preset })}
              >
                ✦ 再开一局
              </button>
            </div>
          </section>
        )}
      </div>

      {/* 3. Collapsible Bottom Chronicle & Vote Flow Dock */}
      <footer className="ww-chronicle-dock">
        <button
          type="button"
          className="ww-chronicle-trigger"
          onClick={() => setChronicleOpen((v) => !v)}
          aria-expanded={chronicleOpen}
        >
          <span className="ww-chronicle-latest">📜 {latestEventText}</span>
          <span className="ww-chronicle-toggle-pill">
            <span>记录 / 票型</span>
            <ChevronIcon open={chronicleOpen} className="ww-phase-icon" />
          </span>
        </button>

        {chronicleOpen && (
          <div className="ww-chronicle-body">
            <div className="ww-segmented">
              <button
                type="button"
                className={`ww-seg-item ${activeTab === "events" ? "active" : ""}`}
                onClick={() => setActiveTab("events")}
              >
                编年记录 ({view.publicEvents?.length ?? 0})
              </button>
              <button
                type="button"
                className={`ww-seg-item ${activeTab === "votes" ? "active" : ""}`}
                onClick={() => setActiveTab("votes")}
              >
                历史票型 ({view.voteHistory?.length ?? 0})
              </button>
            </div>

            {activeTab === "events" ? (
              <div className="ww-chronicle-list">
                {!view.publicEvents || view.publicEvents.length === 0 ? (
                  <div className="ww-event-row">暂无公共事件记录</div>
                ) : (
                  [...view.publicEvents].reverse().map((ev) => (
                    <div key={ev.seq} className="ww-event-row">
                      <span className="ww-event-tag">
                        {ev.round > 0 ? `R${ev.round}` : "大厅"}
                      </span>
                      <span>{ev.summary}</span>
                    </div>
                  ))
                )}
              </div>
            ) : (
              <div className="ww-chronicle-list">
                {!view.voteHistory || view.voteHistory.length === 0 ? (
                  <div className="ww-event-row">
                    尚未产生公示票型（投票进行中暗票严格保密）
                  </div>
                ) : (
                  [...view.voteHistory].reverse().map((vh) => (
                    <div key={vh.round} className="ww-vote-round">
                      <div
                        style={{
                          fontSize: "11.5px",
                          fontWeight: 700,
                          color: "var(--gold-bright)",
                        }}
                      >
                        第{vh.round}天 · {vh.summary}
                      </div>
                      {groupVotes(vh).map((row) => (
                        <div key={row.targetSeat} className="ww-vote-flow-row">
                          <span className="ww-vote-voters">
                            {row.voters.map((v) => `${v}号`).join("、")}
                          </span>
                          <span
                            className={`ww-vote-target ${
                              row.isEliminated ? "out" : ""
                            }`}
                          >
                            ──▶{" "}
                            {row.targetSeat === 0
                              ? `弃票 (${row.voters.length})`
                              : `${row.targetSeat}号 (${row.voters.length}票${row.isEliminated ? "·出局" : ""})`}
                          </span>
                        </div>
                      ))}
                    </div>
                  ))
                )}
              </div>
            )}
          </div>
        )}
      </footer>
    </main>
  );
}
