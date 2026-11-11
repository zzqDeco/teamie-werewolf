import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { fetchMyView, postGameAction, subscribeViewStream } from "./api";
import { TeamieHostBridge } from "./bridge";
import type { BridgeState } from "./bridge";
import {
  ChevronIcon,
  CrownTrophyIcon,
  DaggerIcon,
  EyeToggleIcon,
  MicCrestIcon,
  MoonStarIcon,
  MuteLockIcon,
  ScaleOrderIcon,
  ScrollIcon,
  SparkleStarIcon,
  SunCrestIcon,
  WaxSealIcon,
  WerewolfEmblem,
} from "./icons";
import { RoundtableArena } from "./RoundtableArena";
import type { TargetingMode } from "./RoundtableArena";
import { ROLE_ARCANA, TAROT_BACK_IMAGE } from "./tarotArt";
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

const ROMAN_ROUNDS = ["", "I", "II", "III", "IV", "V", "VI", "VII", "VIII", "IX", "X"];

function toRoman(n: number): string {
  return ROMAN_ROUNDS[n] ?? String(n);
}

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
  const [zoomTarot, setZoomTarot] = useState(false);

  // Interactive seat targeting & collapsible chronicle dock
  const [selectedSeat, setSelectedSeat] = useState<number | null>(null);
  const [chronicleOpen, setChronicleOpen] = useState(false);
  const [activeTab, setActiveTab] = useState<"events" | "votes">("events");
  const [isSplitChronicle, setIsSplitChronicle] = useState(false);
  const shellRef = useRef<HTMLElement | null>(null);

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

  // Capture initial span whenever phaseDeadlineMs changes so center dial arc scales smoothly
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

  // Reset card concealment & zoom overlay when returning to lobby or losing dealt card
  useEffect(() => {
    if (!view?.me.role || view.phase === "LOBBY") {
      setZoomTarot(false);
      setHideRole(false);
    }
  }, [view?.me.role, view?.phase]);

  // Observe dynamic host pane width (280px..960px) so wide drawers unlock dual-ledger chronicle
  useEffect(() => {
    const el = shellRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const update = (w: number) => setIsSplitChronicle(w >= 700);
    update(el.getBoundingClientRect().width);
    const ro = new ResizeObserver((entries) => {
      for (const entry of entries) {
        update(entry.contentRect.width);
      }
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [bridgeState.ready, Boolean(bridgeState.context), Boolean(view)]);

  // Support Escape key dismissal on full-scale Tarot modal
  useEffect(() => {
    if (!zoomTarot) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setZoomTarot(false);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [zoomTarot]);

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

  // Handshake Waiting Screen featuring the engraved Gold-Foil Astrolabe Card Back
  if (!bridgeState.ready || !bridgeState.context) {
    return (
      <main ref={shellRef} className="ww-shell">
        <section className="ww-wait-screen">
          <div className="ww-wait-tarot">
            <img src={TAROT_BACK_IMAGE} alt="塔罗星盘牌背" />
          </div>
          <strong
            style={{
              fontFamily: "var(--font-serif)",
              fontSize: "16px",
              letterSpacing: "0.06em",
              color: "var(--gold-bright)",
            }}
          >
            狼人杀 · 暗金裁判台
          </strong>
          <span style={{ fontSize: "12.5px", color: "var(--text-secondary)", maxWidth: 320 }}>
            正在等待 Teamie 群聊宿主完成安全握手（<code>teamie:mcp-app:init</code>）…
          </span>
          <small style={{ fontSize: "11.5px", color: "var(--text-faint)", maxWidth: 300 }}>
            请在群聊右侧「群聊应用」面板中打开本应用。
          </small>
        </section>
      </main>
    );
  }

  if (!view) {
    return (
      <main ref={shellRef} className="ww-shell">
        <section className="ww-wait-screen">
          <div className="ww-wait-tarot">
            <img src={TAROT_BACK_IMAGE} alt="正在洗牌" />
          </div>
          <strong style={{ fontFamily: "var(--font-serif)", color: "var(--gold-bright)" }}>
            正在铺陈星盘与命运牌阵…
          </strong>
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

  const arcana = me.role ? ROLE_ARCANA[me.role] : null;
  const isCardFaceDown = !arcana || hideRole;

  const buildArcanaIntel = (): string => {
    if (!me.role || !arcana) {
      return me.seated
        ? "已缔结入座契约，满员发牌后翻转揭晓身份底牌"
        : "尚未入座 · 点击下方按钮入座参与博弈";
    }
    if (hideRole) {
      return "命运底牌已暗置防窥 · 轻触按钮或牌面翻开";
    }
    if (me.role === "WEREWOLF" && priv?.wolfTeammateSeats) {
      const mates = priv.wolfTeammateSeats.filter((n) => n !== me.seat);
      return mates.length > 0
        ? `狼同伴：${mates.map((n) => `${n}号`).join("、")} · 暗频协同锁刀`
        : "独狼在场 · 暗夜独立猎杀";
    }
    if (me.role === "SEER" && priv?.seerChecks && priv.seerChecks.length > 0) {
      return `查验记录：${priv.seerChecks
        .map((r) => `${r.targetSeat}号${r.alignment === "WOLF" ? "查杀" : "金水"}`)
        .join(" · ")}`;
    }
    if (me.role === "WITCH" && priv?.witchState) {
      return `解药：${priv.witchState.antidoteLeft ? "在手" : "已用"} · 毒药：${priv.witchState.poisonLeft ? "在手" : "已用"}`;
    }
    return arcana.motto;
  };

  const latestEventText =
    view.publicEvents && view.publicEvents.length > 0
      ? view.publicEvents[view.publicEvents.length - 1].summary
      : "星盘就绪，等待玩家入座开启对局";

  const latestVoteTally =
    view.voteHistory && view.voteHistory.length > 0
      ? view.voteHistory[view.voteHistory.length - 1]
      : null;

  return (
    <main
      ref={shellRef}
      className={`ww-shell ${
        phase === "NIGHT" ? "is-night" : phase === "GAME_OVER" ? "is-over" : ""
      }`}
    >
      {/* Full-Scale 3D Tarot Artwork Inspection Overlay */}
      {zoomTarot && arcana && !hideRole && (
        <div
          className="ww-tarot-modal"
          role="dialog"
          aria-modal="true"
          aria-label="底牌立绘鉴赏"
          onClick={() => setZoomTarot(false)}
        >
          <div className="ww-arcana-latin">{arcana.latinTitle}</div>
          <div className="ww-modal-card-frame">
            <img src={arcana.image} alt={arcana.cnTitle} />
          </div>
          <div
            style={{
              fontFamily: "var(--font-serif)",
              fontSize: "18px",
              fontWeight: 700,
              color: "var(--gold-bright)",
            }}
          >
            {arcana.cnTitle}
          </div>
          <div style={{ fontSize: "12px", color: "var(--text-secondary)", maxWidth: 220 }}>
            {arcana.motto}
          </div>
          <button
            type="button"
            className="ww-ghost-chip"
            autoFocus
            onClick={(e) => {
              e.stopPropagation();
              setZoomTarot(false);
            }}
          >
            收起立绘 · Esc
          </button>
        </div>
      )}

      {/* 1. Slim Classical Header */}
      <header className="ww-topbar">
        <span
          className={`ww-phase-heading ${
            phase === "NIGHT" ? "night" : phase === "GAME_OVER" ? "over" : ""
          }`}
        >
          {phase === "NIGHT" ? (
            <MoonStarIcon className="ww-mini-svg" />
          ) : phase === "GAME_OVER" ? (
            <CrownTrophyIcon className="ww-mini-svg" />
          ) : (
            <SunCrestIcon className="ww-mini-svg" />
          )}
          <span>
            {round > 0 ? `${toRoman(round)} · ` : ""}
            {PHASE_LABELS[phase]}
          </span>
        </span>

        <span className="ww-preset-tag">
          {preset === "9p" ? "IX · 猎人局" : "VI · 标准局"}
        </span>
      </header>

      {/* 2. Fluid Adaptive Stage (Single-Column <640px, Dual-Column Panoramic Tabletop >=640px) */}
      <div className="ww-stage">
        <div className="ww-hero-region">
          {/* True 3D Flipping Engraved Tarot Identity Showcase */}
          <section className="ww-tarot-showcase">
            <button
              type="button"
              className="ww-card-3d-scene"
              onClick={() => {
                if (!arcana) return;
                if (hideRole) {
                  setHideRole(false);
                } else {
                  setZoomTarot(true);
                }
              }}
              title={
                !arcana
                  ? "发牌后揭晓命运塔罗"
                  : hideRole
                    ? "点击翻开底牌"
                    : "点击放大鉴赏塔罗原画"
              }
            >
              <div className={`ww-card-3d-inner ${isCardFaceDown ? "is-flipped" : ""}`}>
                <div className="ww-card-face card-front">
                  <img
                    src={arcana ? arcana.image : TAROT_BACK_IMAGE}
                    alt={arcana ? arcana.cnTitle : "未发牌"}
                  />
                </div>
                <div className="ww-card-face card-back">
                  <img src={TAROT_BACK_IMAGE} alt="塔罗牌背" />
                </div>
              </div>
            </button>

            <div className="ww-arcana-info">
              <div className="ww-arcana-latin">
                {arcana && !hideRole ? arcana.latinTitle : "ARCANA · FATE"}
              </div>
              <div className="ww-arcana-title-row">
                <span
                  className={`ww-arcana-role ${
                    !hideRole && me.role === "WEREWOLF" ? "is-wolf" : ""
                  }`}
                >
                  {!me.role
                    ? me.displayName
                    : hideRole
                      ? "命运底牌已暗置"
                      : `我的底牌：${me.roleLabel || ROLE_LABELS[me.role]}`}
                </span>
                {me.seated && me.seat !== undefined && (
                  <span className="ww-arcana-seat">
                    {me.seat}号{!me.alive ? "·出局" : ""}
                  </span>
                )}
              </div>

              <div className="ww-arcana-intel">{buildArcanaIntel()}</div>

              {arcana && (
                <div className="ww-arcana-actions">
                  <button
                    type="button"
                    className="ww-tarot-id"
                    onClick={() => setHideRole((v) => !v)}
                  >
                    <EyeToggleIcon hidden={hideRole} className="ww-mini-svg" />
                    <span>{hideRole ? "翻开底牌" : "暗置防窥"}</span>
                  </button>
                  {!hideRole && (
                    <button
                      type="button"
                      className="ww-ghost-chip"
                      onClick={() => setZoomTarot(true)}
                    >
                      <span>立绘鉴赏</span>
                    </button>
                  )}
                </div>
              )}
            </div>
          </section>

          {/* Public Chat Discipline Ribbon (Zero System Emojis) */}
          <section
            className={`ww-policy-ribbon ${
              !publicChatPolicy.allowed ? "locked" : isMyMic ? "my-mic" : ""
            }`}
          >
            {!publicChatPolicy.allowed ? (
              <MuteLockIcon className="ww-mini-svg" style={{ marginTop: 2 }} />
            ) : isMyMic ? (
              <MicCrestIcon className="ww-mini-svg" style={{ marginTop: 2 }} />
            ) : (
              <ScaleOrderIcon className="ww-mini-svg" style={{ marginTop: 2 }} />
            )}
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
                ×
              </button>
            </div>
          )}
        </div>

        <div className="ww-filigree-rule ww-narrow-only" aria-hidden="true">
          ✦
        </div>

        {/* Arcane Roundtable Astrolabe Theater */}
        <div className="ww-board-region">
          <RoundtableArena
            phase={phase}
            targetPlayers={targetPlayers}
            seats={seats}
            mySeat={me.seat}
            hideRole={hideRole}
            seerChecks={priv?.seerChecks}
            currentSpeakerSeat={view.speech?.currentSpeakerSeat}
            targetingMode={targetingMode}
            isDangerAim={isDangerAim}
            effectiveTargetSeat={effectiveTargetSeat}
            canSelectSeat={canSelectSeat}
            onSelectSeat={(seatNum) =>
              setSelectedSeat((prev) => (prev === seatNum ? null : seatNum))
            }
            remainingSec={remainingSec}
            countdownPct={countdownPct}
            winner={view.winner}
          />
        </div>

        <div className="ww-filigree-rule ww-narrow-only" aria-hidden="true">
          ✦
        </div>

        {/* ================================================================
            De-Boxed Contextual Action Altars (Fluid 280px -> 960px)
            ================================================================ */}
        <div className="ww-command-region">

        {/* LOBBY ALTAR */}
        {phase === "LOBBY" && (
          <section className="ww-altar-pane">
            <div className="ww-altar-header">
              <span className="ww-altar-heading">
                <SparkleStarIcon className="ww-mini-svg" />
                <span>契约仪式</span>
              </span>
              <span className="ww-altar-note">
                {preset === "9p" ? "3狼3民·预女猎" : "2狼2民·预女"}
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
              <div className="ww-btn-row">
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
                    ? "洗牌发牌 · 入夜"
                    : `等待满员 (${seats.length}/${targetPlayers})`}
                </button>
              </div>
            )}
          </section>
        )}

        {/* NIGHT ALTAR */}
        {phase === "NIGHT" && (
          <section className="ww-altar-pane">
            <div className="ww-altar-header">
              <span className="ww-altar-heading">
                <MoonStarIcon className="ww-mini-svg" />
                <span>暗夜圣坛</span>
              </span>
              <span className="ww-altar-note">
                {me.seated && me.pendingAction ? "请执行技能" : "静候破晓"}
              </span>
            </div>

            {/* WEREWOLF SANCTUM */}
            {me.role === "WEREWOLF" && (
              <>
                <div className="ww-wolf-sanctum">
                  <div className="ww-altar-header">
                    <span
                      style={{
                        fontSize: "11.5px",
                        fontWeight: 700,
                        color: "#ff9ca7",
                        display: "inline-flex",
                        alignItems: "center",
                        gap: "5px",
                      }}
                    >
                      <WerewolfEmblem className="ww-mini-svg" />
                      <span>血契暗频</span>
                    </span>
                    {priv?.wolfIntentions &&
                      Object.keys(priv.wolfIntentions).length > 0 && (
                        <span style={{ fontSize: "10.5px", color: "#e8a8b0" }}>
                          {Object.entries(priv.wolfIntentions)
                            .map(([w, t]) =>
                              Number(t) === 0 ? `${w}→空` : `${w}→${t}号`,
                            )
                            .join(" ")}
                        </span>
                      )}
                  </div>

                  <div className="ww-wolf-stream" ref={wolfStreamRef}>
                    {!priv?.wolfChat || priv.wolfChat.length === 0 ? (
                      <span style={{ fontSize: "11px", color: "#d49ba2", opacity: 0.82 }}>
                        暗频仅狼队友可见，先商议今夜袭击目标。
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
                        placeholder="发送暗频密语…"
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
                  <div className="ww-action-pair">
                    <button
                      type="button"
                      disabled={busy || effectiveTargetSeat === null}
                      className="ww-btn ww-btn-crimson ww-w-full"
                      onClick={() => {
                        if (effectiveTargetSeat === null) return;
                        void runAction("cast_night_action", {
                          actionType: "kill",
                          targetSeat: effectiveTargetSeat,
                        });
                      }}
                    >
                      <DaggerIcon className="ww-mini-svg" />
                      <span>
                        {effectiveTargetSeat !== null
                          ? myWolfPick === effectiveTargetSeat
                            ? `已选袭击 ${effectiveTargetSeat}号（点圆桌可改）`
                            : myWolfPick !== undefined
                              ? `改选袭击 ${effectiveTargetSeat}号`
                              : `锁定袭击 ${effectiveTargetSeat}号`
                          : "请在圆桌上轻触袭击目标"}
                      </span>
                    </button>
                    <button
                      type="button"
                      disabled={busy}
                      className={`ww-btn ww-w-full ${
                        myWolfPick === 0 && selectedSeat === null ? "ww-btn-active" : ""
                      }`}
                      onClick={() => {
                        setSelectedSeat(null);
                        void runAction("cast_night_action", {
                          actionType: "pass",
                          targetSeat: 0,
                        });
                      }}
                    >
                      放弃袭击（空刀）
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
                    <SparkleStarIcon className="ww-mini-svg" />
                    <span>
                      {effectiveTargetSeat !== null
                        ? `启示查验 ${effectiveTargetSeat}号 阵营`
                        : "请在圆桌上轻触查验目标"}
                    </span>
                  </button>
                ) : (
                  <div className="ww-sub-strip">
                    本夜星象查验已完成，请静候破晓。
                  </div>
                )}

                {priv?.seerChecks && priv.seerChecks.length > 0 && (
                  <div className="ww-sub-strip">
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
                          第{r.round}夜 · {r.targetSeat}号
                        </span>
                        <b
                          style={{
                            color:
                              r.alignment === "WOLF"
                                ? "var(--crimson)"
                                : "var(--emerald)",
                          }}
                        >
                          {r.alignment === "WOLF" ? "查杀(狼)" : "金水(好人)"}
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
                  <span className={`ww-badge ${priv.witchState.antidoteLeft ? "good" : ""}`}>
                    解药：{priv.witchState.antidoteLeft ? "可用" : "已耗"}
                  </span>
                  <span className={`ww-badge ${priv.witchState.poisonLeft ? "wolf" : ""}`}>
                    毒药：{priv.witchState.poisonLeft ? "可用" : "已耗"}
                  </span>
                </div>

                {me.alive && priv.witchState.canActNow ? (
                  <>
                    {priv.witchState.antidoteLeft && (
                      <div className="ww-sub-strip">
                        {priv.witchState.tonightAttackedSeat &&
                        priv.witchState.tonightAttackedSeat > 0 ? (
                          <div
                            style={{
                              display: "flex",
                              flexDirection: "column",
                              gap: "6px",
                            }}
                          >
                            <span>
                              今夜 <b>{priv.witchState.tonightAttackedSeat}号席</b> 遇袭倒牌
                            </span>
                            <button
                              type="button"
                              disabled={busy}
                              className="ww-btn ww-btn-emerald ww-w-full"
                              onClick={() =>
                                void runAction("cast_night_action", {
                                  actionType: "save",
                                })
                              }
                            >
                              赐予解药救起 {priv.witchState.tonightAttackedSeat}号
                            </button>
                          </div>
                        ) : (
                          <span>今夜狼人空刀，无人遇袭。</span>
                        )}
                      </div>
                    )}

                    {priv.witchState.poisonLeft && (
                      <button
                        type="button"
                        disabled={busy || effectiveTargetSeat === null}
                        className="ww-btn ww-btn-crimson ww-w-full"
                        onClick={() => {
                          if (effectiveTargetSeat === null) return;
                          void runAction("cast_night_action", {
                            actionType: "poison",
                            targetSeat: effectiveTargetSeat,
                          });
                        }}
                      >
                        {effectiveTargetSeat !== null
                          ? `赐下毒药淘汰 ${effectiveTargetSeat}号`
                          : "点选圆桌座位可用毒药"}
                      </button>
                    )}

                    <button
                      type="button"
                      disabled={busy}
                      className="ww-btn ww-w-full"
                      onClick={() =>
                        void runAction("cast_night_action", { actionType: "pass" })
                      }
                    >
                      不使用药水（跳过）
                    </button>
                  </>
                ) : (
                  <div className="ww-sub-strip">
                    {me.actionHint || "正在等待狼人与预言家回合结束…"}
                  </div>
                )}
              </>
            )}

            {/* VILLAGER / HUNTER / DEAD WOLF / SPECTATOR IDLE NIGHT */}
            {(!me.seated ||
              !me.role ||
              me.role === "VILLAGER" ||
              me.role === "HUNTER" ||
              (me.role === "WEREWOLF" && !me.alive)) && (
              <div className="ww-sub-strip">
                {me.actionHint || "夜幕深沉，请静候神狼博弈结束迎来黎明。"}
              </div>
            )}
          </section>
        )}

        {/* DAY_ANNOUNCE ALTAR (When waiting on dawn transition or non-shooter watching Hunter) */}
        {phase === "DAY_ANNOUNCE" && !priv?.hunterCanShoot && (
          <section className="ww-altar-pane">
            <div className="ww-altar-header">
              <span className="ww-altar-heading">
                <SunCrestIcon className="ww-mini-svg" />
                <span>破晓宣告</span>
              </span>
            </div>
            <div className="ww-sub-strip">
              {me.actionHint || latestEventText}
            </div>
          </section>
        )}

        {/* HUNTER REVENGE ALTAR */}
        {priv?.hunterCanShoot && (
          <section className="ww-altar-pane">
            <div className="ww-altar-header">
              <span className="ww-altar-heading" style={{ color: "#ff9ca7" }}>
                猎魔银弹 · 临终反击
              </span>
            </div>
            <div className="ww-action-pair">
              <button
                type="button"
                disabled={busy || effectiveTargetSeat === null}
                className="ww-btn ww-btn-crimson ww-w-full"
                onClick={() => {
                  if (effectiveTargetSeat === null) return;
                  void runAction("hunter_shoot", { targetSeat: effectiveTargetSeat });
                }}
              >
                {effectiveTargetSeat !== null
                  ? `开枪带走 ${effectiveTargetSeat}号`
                  : "请在圆桌上轻触带走目标"}
              </button>
              <button
                type="button"
                disabled={busy}
                className="ww-btn ww-w-full"
                onClick={() => void runAction("hunter_shoot", { targetSeat: 0 })}
              >
                压枪不开枪
              </button>
            </div>
          </section>
        )}

        {/* DAY_SPEECH MIC STEPPER ALTAR */}
        {phase === "DAY_SPEECH" && view.speech && (
          <section className="ww-altar-pane">
            <div className="ww-altar-header">
              <span className="ww-altar-heading">
                <MicCrestIcon className="ww-mini-svg" />
                <span>单人麦序轨</span>
              </span>
              <span className="ww-altar-note">
                {view.speech.spokenSeats.length}/{view.speech.speakingOrder.length}
              </span>
            </div>

            <div className="ww-mic-track">
              {view.speech.speakingOrder.map((seatNum, idx) => {
                const isDone = view.speech?.spokenSeats.includes(seatNum);
                const isCurr = view.speech?.currentSpeakerSeat === seatNum;
                return (
                  <span
                    key={seatNum}
                    style={{ display: "inline-flex", alignItems: "center", gap: "3px" }}
                  >
                    <span
                      className={`ww-mic-node ${
                        isCurr ? "current" : isDone ? "done" : ""
                      }`}
                    >
                      {isDone ? "✓ " : ""}
                      {seatNum}号
                    </span>
                    {idx < (view.speech?.speakingOrder.length ?? 0) - 1 && (
                      <span style={{ color: "var(--text-faint)", fontSize: "10px" }}>
                        →
                      </span>
                    )}
                  </span>
                );
              })}
            </div>

            {isMyMic ? (
              <>
                <input
                  className="ww-input"
                  placeholder="可选：一句话归票摘要（计入记录）"
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
                  群聊陈词完毕 · 交麦
                </button>
              </>
            ) : (
              <div className="ww-sub-strip">
                当前由 <b>{view.speech.currentSpeakerSeat}号</b> 在左侧群聊陈词，请待其交麦。
              </div>
            )}
          </section>
        )}

        {/* DAY_VOTE SECRET BALLOT ALTAR */}
        {phase === "DAY_VOTE" && !priv?.hunterCanShoot && (
          <section className="ww-altar-pane">
            <div className="ww-altar-header">
              <span className="ww-altar-heading">
                <WaxSealIcon className="ww-mini-svg" />
                <span>火漆放逐暗票</span>
              </span>
              {priv?.mySubmittedVote !== undefined && (
                <span className="ww-badge gold">
                  已投：{priv.mySubmittedVote === 0 ? "弃票" : `${priv.mySubmittedVote}号`}
                </span>
              )}
            </div>

            {me.seated && me.alive ? (
              <div className="ww-action-pair">
                <button
                  type="button"
                  disabled={busy || effectiveTargetSeat === null}
                  className="ww-btn ww-btn-gold ww-w-full"
                  onClick={() => {
                    if (effectiveTargetSeat === null) return;
                    void runAction("submit_vote", {
                      targetSeat: effectiveTargetSeat,
                    });
                  }}
                >
                  <WaxSealIcon className="ww-mini-svg" />
                  <span>
                    {effectiveTargetSeat !== null
                      ? priv?.mySubmittedVote === effectiveTargetSeat
                        ? `已暗投 ${effectiveTargetSeat}号（点圆桌可改票）`
                        : priv?.mySubmittedVote !== undefined
                          ? `改投 ${effectiveTargetSeat}号 放逐`
                          : `盖印放逐 ${effectiveTargetSeat}号`
                      : "请在圆桌上轻触放逐目标"}
                  </span>
                </button>

                <button
                  type="button"
                  disabled={busy}
                  className={`ww-btn ww-w-full ${
                    priv?.mySubmittedVote === 0 && selectedSeat === null
                      ? "ww-btn-active"
                      : ""
                  }`}
                  onClick={() => {
                    setSelectedSeat(null);
                    void runAction("submit_vote", { targetSeat: 0 });
                  }}
                >
                  投弃权票
                </button>
              </div>
            ) : (
              <div className="ww-sub-strip">
                正在等待存活玩家完成火漆暗票投递…
              </div>
            )}
          </section>
        )}

        {/* GAME_OVER VICTORY ALTAR */}
        {phase === "GAME_OVER" && (
          <section className="ww-altar-pane">
            <div className="ww-altar-header">
              <span className="ww-altar-heading">
                <CrownTrophyIcon className="ww-mini-svg" />
                <span>
                  {view.winner === "GOOD" ? "好人阵营获胜" : "狼人阵营获胜"}
                </span>
              </span>
              <span className="ww-badge gold">底牌已全开</span>
            </div>

            <div className="ww-sub-strip">{view.winReason}</div>

            {latestVoteTally && (
              <div className="ww-vote-round">
                <div style={{ fontSize: "11px", fontWeight: 700, color: "var(--gold)" }}>
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
                  离席
                </button>
              )}
              <button
                type="button"
                disabled={busy}
                className="ww-btn ww-btn-gold ww-flex-1"
                onClick={() => void runAction("join_lobby", { preset })}
              >
                再开一局
              </button>
            </div>
          </section>
        )}
        </div>
      </div>

      {/* 3. Collapsible Bottom Chronicle & Vote Flow Dock */}
      <footer className="ww-chronicle-dock">
        <button
          type="button"
          className="ww-chronicle-trigger"
          onClick={() => setChronicleOpen((v) => !v)}
          aria-expanded={chronicleOpen}
        >
          <span className="ww-chronicle-latest">
            <ScrollIcon className="ww-mini-svg" />
            <span>{latestEventText}</span>
          </span>
          <span className="ww-chronicle-toggle-pill">
            <span>记录 / 票型</span>
            <ChevronIcon open={chronicleOpen} className="ww-mini-svg" />
          </span>
        </button>

        {chronicleOpen && (
          <div className="ww-chronicle-body">
            {isSplitChronicle ? (
              <div className="ww-chronicle-split">
                <div className="ww-chronicle-col">
                  <div className="ww-chronicle-col-title">
                    编年记录 ({view.publicEvents?.length ?? 0})
                  </div>
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
                </div>
                <div className="ww-chronicle-col">
                  <div className="ww-chronicle-col-title">
                    历史票型 ({view.voteHistory?.length ?? 0})
                  </div>
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
                </div>
              </div>
            ) : (
              <>
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
              </>
            )}
          </div>
        )}
      </footer>
    </main>
  );
}
