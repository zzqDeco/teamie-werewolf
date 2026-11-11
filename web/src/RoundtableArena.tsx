import { useId, useState } from "react";
import { CrownTrophyIcon, MoonStarIcon, SparkleStarIcon, SunCrestIcon } from "./icons";
import { ROLE_ARCANA } from "./tarotArt";
import type { Phase, Role, SeatView, SeerCheckRecord } from "./types";

export type TargetingMode =
  | "none"
  | "wolf_kill"
  | "seer_check"
  | "witch_poison"
  | "hunter_shoot"
  | "day_vote";

const ENAMEL_PALETTE = [
  "#523821",
  "#3b2d5e",
  "#1f4a42",
  "#5c2631",
  "#263f63",
  "#542d4c",
  "#473c24",
  "#214954",
  "#593324",
];

const ROLE_SHORT_LABELS: Record<Role, string> = {
  WEREWOLF: "狼人",
  VILLAGER: "平民",
  SEER: "预言家",
  WITCH: "女巫",
  HUNTER: "猎人",
};

function getInitials(name: string): string {
  const clean = name.trim();
  if (!clean) return "?";
  // For CJK characters, first single character looks cleanest inside a medallion
  if (/[\u4e00-\u9fa5]/.test(clean[0])) {
    return clean[0];
  }
  const parts = clean.split(/[\s_-]+/).filter(Boolean);
  if (parts.length >= 2) {
    return (parts[0][0] + parts[1][0]).toUpperCase();
  }
  return clean.slice(0, 2).toUpperCase();
}

function formatClock(sec: number): string {
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

export interface RoundtableArenaProps {
  phase: Phase;
  targetPlayers: number;
  seats: SeatView[];
  mySeat?: number;
  hideRole: boolean;
  seerChecks?: SeerCheckRecord[];
  currentSpeakerSeat?: number;
  targetingMode: TargetingMode;
  isDangerAim: boolean;
  effectiveTargetSeat: number | null;
  canSelectSeat: (seat: SeatView) => boolean;
  onSelectSeat: (seatNum: number) => void;
  remainingSec: number | null;
  countdownPct: number;
  winner?: "WOLF" | "GOOD";
}

export function RoundtableArena({
  phase,
  targetPlayers,
  seats,
  mySeat,
  hideRole,
  seerChecks,
  currentSpeakerSeat,
  targetingMode,
  isDangerAim,
  effectiveTargetSeat,
  canSelectSeat,
  onSelectSeat,
  remainingSec,
  countdownPct,
  winner,
}: RoundtableArenaProps) {
  const glowGradId = useId();
  const [focusedSeat, setFocusedSeat] = useState<number | null>(null);

  const totalSlots = Math.max(targetPlayers || 6, seats.length, 6);
  const isNine = totalSlots > 6;
  const orbitPct = isNine ? 37.5 : 36;
  const orbitRadiusSvg = (orbitPct / 100) * 260;

  // Build full 1..totalSlots array so empty seats in LOBBY sit gracefully on the ring
  const slots = Array.from({ length: totalSlots }, (_, idx) => {
    const seatNum = idx + 1;
    const found = seats.find((s) => s.seat === seatNum) ?? null;
    // Seat 1 at 12 o'clock (-90 deg), proceeding clockwise
    const angleRad = -Math.PI / 2 + (idx * 2 * Math.PI) / totalSlots;
    const xPct = 50 + orbitPct * Math.cos(angleRad);
    const yPct = 50 + orbitPct * Math.sin(angleRad);
    // SVG viewBox is 260x260 -> center (130, 130)
    const svgX = 130 + orbitRadiusSvg * Math.cos(angleRad);
    const svgY = 130 + orbitRadiusSvg * Math.sin(angleRad);
    return { seatNum, seat: found, angleRad, xPct, yPct, svgX, svgY };
  });

  const activeSpeakerSlot =
    phase === "DAY_SPEECH" && currentSpeakerSeat
      ? slots.find((s) => s.seatNum === currentSpeakerSeat)
      : undefined;

  const aimedSlot =
    effectiveTargetSeat !== null
      ? slots.find((s) => s.seatNum === effectiveTargetSeat)
      : undefined;

  // Center countdown SVG circle metrics
  const dialRadius = 41;
  const dialCircumference = 2 * Math.PI * dialRadius;
  const showTimer =
    remainingSec !== null && phase !== "LOBBY" && phase !== "GAME_OVER";
  const dashOffset = showTimer
    ? dialCircumference * (1 - Math.min(100, Math.max(0, countdownPct)) / 100)
    : 0;
  const isUrgent = showTimer && remainingSec <= 15;

  const centerCue = (() => {
    if (phase === "LOBBY") return `${seats.length}/${totalSlots} 入座`;
    if (phase === "GAME_OVER") {
      return winner === "GOOD" ? "好人获胜" : "狼人获胜";
    }
    if (targetingMode !== "none") {
      return effectiveTargetSeat ? `锁定 ${effectiveTargetSeat}号` : "轻触瞄准";
    }
    if (phase === "DAY_SPEECH" && currentSpeakerSeat) {
      return `${currentSpeakerSeat}号陈词`;
    }
    if (phase === "NIGHT") return "暗夜闭眼";
    return "破晓审判";
  })();

  // Determine which seat is featured in the bottom untruncated Spotlight Inspector Bar
  const inspectedSeat: SeatView | undefined = (() => {
    if (effectiveTargetSeat !== null) {
      const s = seats.find((item) => item.seat === effectiveTargetSeat);
      if (s) return s;
    }
    if (targetingMode === "none" && focusedSeat !== null) {
      const s = seats.find((item) => item.seat === focusedSeat);
      if (s) return s;
    }
    if (phase === "DAY_SPEECH" && currentSpeakerSeat) {
      const s = seats.find((item) => item.seat === currentSpeakerSeat);
      if (s) return s;
    }
    if (mySeat !== undefined) {
      const s = seats.find((item) => item.seat === mySeat);
      if (s) return s;
    }
    return seats[0];
  })();

  const renderInspectorStatus = (s: SeatView): string => {
    const parts: string[] = [];
    if (!s.alive) {
      parts.push("✕ 已出局");
    } else if (s.isSpeaking) {
      parts.push("正在持麦发言");
    } else if (phase === "DAY_VOTE" && s.hasVoted) {
      parts.push("已投暗票");
    } else if (s.hasSpoken) {
      parts.push("本轮已发言");
    } else {
      parts.push("在场存活");
    }

    if (s.knownRole && (!hideRole || phase === "GAME_OVER")) {
      parts.push(ROLE_SHORT_LABELS[s.knownRole]);
    } else if (!hideRole && seerChecks) {
      const rec = seerChecks.find((c) => c.targetSeat === s.seat);
      if (rec) {
        parts.push(rec.alignment === "WOLF" ? "查杀" : "金水");
      }
    }

    if (effectiveTargetSeat === s.seat && targetingMode !== "none") {
      parts.push("◎ 当前瞄准目标");
    }
    return parts.join(" · ");
  };

  return (
    <div className="ww-arena-block">
      <div className={`ww-astrolabe-wrap ${isNine ? "is-nine" : "is-six"}`}>
        {/* Sacred Astrolabe SVG Orbit & Dynamic Pointer Rays */}
        <svg
          className="ww-astrolabe-svg"
          viewBox="0 0 260 260"
          aria-hidden="true"
        >
          <defs>
            <radialGradient id={glowGradId} cx="50%" cy="50%" r="50%">
              <stop
                offset="0%"
                stopColor={
                  phase === "NIGHT"
                    ? "rgba(159, 142, 244, 0.16)"
                    : "rgba(212, 175, 99, 0.16)"
                }
              />
              <stop offset="100%" stopColor="rgba(13, 12, 19, 0)" />
            </radialGradient>
          </defs>

          <circle cx="130" cy="130" r="120" fill={`url(#${glowGradId})`} />

          {/* Outer Filigree & Seat Orbit Rings */}
          <circle
            cx="130"
            cy="130"
            r="116"
            fill="none"
            stroke="rgba(212, 175, 99, 0.12)"
            strokeWidth="0.8"
            strokeDasharray="2 4"
          />
          <circle
            cx="130"
            cy="130"
            r={orbitRadiusSvg}
            fill="none"
            stroke="rgba(212, 175, 99, 0.24)"
            strokeWidth="1"
          />
          <circle
            cx="130"
            cy="130"
            r="52"
            fill="none"
            stroke="rgba(212, 175, 99, 0.15)"
            strokeWidth="0.8"
          />

          {/* Subtle Compass Crosshairs */}
          <line
            x1="130"
            y1="18"
            x2="130"
            y2="34"
            stroke="rgba(212, 175, 99, 0.2)"
            strokeWidth="1"
          />
          <line
            x1="130"
            y1="226"
            x2="130"
            y2="242"
            stroke="rgba(212, 175, 99, 0.2)"
            strokeWidth="1"
          />
          <line
            x1="18"
            y1="130"
            x2="34"
            y2="130"
            stroke="rgba(212, 175, 99, 0.2)"
            strokeWidth="1"
          />
          <line
            x1="226"
            y1="130"
            x2="242"
            y2="130"
            stroke="rgba(212, 175, 99, 0.2)"
            strokeWidth="1"
          />

          {/* Speaker Spotlight Needle in DAY_SPEECH */}
          {activeSpeakerSlot && (
            <line
              x1="130"
              y1="130"
              x2={activeSpeakerSlot.svgX}
              y2={activeSpeakerSlot.svgY}
              stroke="rgba(236, 208, 139, 0.55)"
              strokeWidth="2"
              strokeLinecap="round"
            />
          )}

          {/* Active Aiming Reticle Vector */}
          {aimedSlot && targetingMode !== "none" && (
            <line
              x1="130"
              y1="130"
              x2={aimedSlot.svgX}
              y2={aimedSlot.svgY}
              stroke={
                isDangerAim
                  ? "rgba(224, 83, 99, 0.72)"
                  : "rgba(236, 208, 139, 0.72)"
              }
              strokeWidth="1.6"
              strokeDasharray="4 3"
            />
          )}

          {/* Center Dial Base + Countdown Progress Ring */}
          <circle
            cx="130"
            cy="130"
            r={dialRadius}
            fill="rgba(16, 14, 24, 0.94)"
            stroke="rgba(212, 175, 99, 0.22)"
            strokeWidth="2.5"
          />

          {showTimer && (
            <circle
              cx="130"
              cy="130"
              r={dialRadius}
              fill="none"
              stroke={isUrgent ? "#e05363" : "#d4af63"}
              strokeWidth="2.8"
              strokeLinecap="round"
              strokeDasharray={dialCircumference}
              strokeDashoffset={dashOffset}
              transform="rotate(-90 130 130)"
              style={{ transition: "stroke-dashoffset 0.4s linear, stroke 0.25s ease" }}
            />
          )}
        </svg>

        {/* Center Astrolabe Core Readout */}
        <div className={`ww-astrolabe-core ${isUrgent ? "is-urgent" : ""}`}>
          <span className="ww-core-icon">
            {phase === "NIGHT" ? (
              <MoonStarIcon className="ww-mini-svg" />
            ) : phase === "GAME_OVER" ? (
              <CrownTrophyIcon className="ww-mini-svg" />
            ) : phase === "LOBBY" ? (
              <SparkleStarIcon className="ww-mini-svg" />
            ) : (
              <SunCrestIcon className="ww-mini-svg" />
            )}
          </span>
          {showTimer && remainingSec !== null ? (
            <span className="ww-core-clock">{formatClock(remainingSec)}</span>
          ) : (
            <span className="ww-core-clock">
              {phase === "GAME_OVER" ? "终局" : `${seats.length}/${totalSlots}`}
            </span>
          )}
          <span className="ww-core-cue">{centerCue}</span>
        </div>

        {/* Radial Seat Nodes */}
        {slots.map(({ seatNum, seat, xPct, yPct }) => {
          if (!seat) {
            return (
              <div
                key={`empty-${seatNum}`}
                className="ww-orbit-node is-empty"
                style={{ left: `${xPct}%`, top: `${yPct}%` }}
              >
                <div className="ww-orbit-medallion empty-medallion">
                  <span className="ww-seat-num-chip muted-chip">{seatNum}</span>
                  <span className="ww-empty-dot">·</span>
                </div>
                <span className="ww-orbit-name faint-name">待入座</span>
              </div>
            );
          }

          const selectable = canSelectSeat(seat);
          const inspectableOnly = targetingMode === "none";
          const interactive = selectable || inspectableOnly;
          const isAimed = selectable && effectiveTargetSeat === seat.seat;
          const isInspectedFocus =
            inspectableOnly && inspectedSeat?.seat === seat.seat;
          const showPortrait =
            Boolean(seat.knownRole) && (!hideRole || phase === "GAME_OVER");
          const seerRec =
            !hideRole && seerChecks
              ? seerChecks.find((c) => c.targetSeat === seat.seat)
              : undefined;
          const bgEnamel = seat.alive
            ? ENAMEL_PALETTE[(seat.seat - 1) % ENAMEL_PALETTE.length]
            : "#25222e";

          return (
            <button
              key={seat.seat}
              type="button"
              disabled={!interactive}
              onClick={() => {
                if (selectable) {
                  onSelectSeat(seat.seat);
                } else if (inspectableOnly) {
                  setFocusedSeat((prev) => (prev === seat.seat ? null : seat.seat));
                }
              }}
              style={{ left: `${xPct}%`, top: `${yPct}%` }}
              aria-label={`${seat.seat}号 ${seat.displayName} ${seat.alive ? "存活" : "已出局"}`}
              className={[
                "ww-orbit-node",
                "ww-seat-card",
                interactive ? "can-select" : "",
                seat.isMe ? "is-me" : "",
                seat.isSpeaking ? "is-speaking" : "",
                !seat.alive ? "is-dead" : "",
                isAimed ? "is-selected" : "",
                isInspectedFocus ? "is-inspected" : "",
                isAimed && isDangerAim ? "danger-aim" : "",
              ]
                .filter(Boolean)
                .join(" ")}
            >
              <div
                className="ww-orbit-medallion"
                style={{ background: showPortrait ? "#121018" : bgEnamel }}
              >
                {showPortrait && seat.knownRole ? (
                  <img
                    src={ROLE_ARCANA[seat.knownRole].image}
                    alt={ROLE_SHORT_LABELS[seat.knownRole]}
                    className="ww-medallion-portrait"
                  />
                ) : (
                  <span className="ww-medallion-initials">
                    {getInitials(seat.displayName)}
                  </span>
                )}

                {/* Permanent Seat Number Chip (1..9 ALWAYS visible even when eliminated) */}
                <span className="ww-seat-seal ww-seat-num-chip">{seat.seat}</span>

                {/* Dedicated Top-Right Seer Check Alignment Seal (never overwritten by vote/speech/aim status) */}
                {seerRec && (
                  <span
                    className={`ww-orbit-seer-chip ${
                      seerRec.alignment === "WOLF" ? "flag-danger" : "flag-gold"
                    }`}
                    title={`第${seerRec.round}夜查验：${
                      seerRec.alignment === "WOLF" ? "查杀(狼)" : "金水(好人)"
                    }`}
                  >
                    {seerRec.alignment === "WOLF" ? "狼" : "金"}
                  </span>
                )}

                {/* Bottom-Right Live Turn / Vote / Aim Micro-Badge */}
                {isAimed ? (
                  <span
                    className={`ww-orbit-flag ${
                      isDangerAim ? "flag-danger" : "flag-gold"
                    }`}
                  >
                    ◎
                  </span>
                ) : !seat.alive ? (
                  <span className="ww-orbit-flag flag-dead" title="已出局">
                    ✕
                  </span>
                ) : seat.isSpeaking ? (
                  <span className="ww-orbit-flag flag-mic" title="正在发言">
                    <span className="ww-eq-mini b1" />
                    <span className="ww-eq-mini b2" />
                    <span className="ww-eq-mini b3" />
                  </span>
                ) : (phase === "DAY_VOTE" && seat.hasVoted) ||
                  (phase === "DAY_SPEECH" && seat.hasSpoken) ? (
                  <span className="ww-orbit-flag flag-done">✓</span>
                ) : null}
              </div>

              <span className="ww-orbit-name" title={seat.displayName}>
                {seat.displayName}
              </span>

              {/* Screen-reader & test accessible status text */}
              <span className="ww-sr-only">
                {!seat.alive ? "✕ 已出局" : isAimed ? "已瞄准" : "在场"}
              </span>
            </button>
          );
        })}
      </div>

      {/* Untruncated Spotlight Inspector Bar (ensures full names & roles read cleanly even on 264px) */}
      {inspectedSeat && (
        <div className="ww-inspector-bar">
          <span className="ww-inspector-seat">{inspectedSeat.seat}号席</span>
          <span className="ww-inspector-name">{inspectedSeat.displayName}</span>
          <span className="ww-inspector-meta">
            {renderInspectorStatus(inspectedSeat)}
          </span>
        </div>
      )}
    </div>
  );
}
