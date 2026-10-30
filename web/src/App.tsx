import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { fetchMyView, postGameAction, subscribeViewStream } from "./api";
import { TeamieHostBridge } from "./bridge";
import type { BridgeState } from "./bridge";
import type { Phase, PlayerView, Preset, Role } from "./types";

const PHASE_LABELS: Record<Phase, string> = {
  LOBBY: "大厅准备",
  NIGHT: "天黑闭眼",
  DAY_ANNOUNCE: "黎明播报",
  DAY_SPEECH: "白天发言",
  DAY_VOTE: "暗票放逐",
  GAME_OVER: "对局结算",
};

const ROLE_LABELS: Record<Role, string> = {
  WEREWOLF: "狼人",
  VILLAGER: "平民",
  SEER: "预言家",
  WITCH: "女巫",
  HUNTER: "猎人",
};

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
  const [activeTab, setActiveTab] = useState<"events" | "votes">("events");

  // Local inputs
  const [wolfDraft, setWolfDraft] = useState("");
  const [speechNote, setSpeechNote] = useState("");
  const [nowTickMs, setNowTickMs] = useState(() => Date.now());

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

  // Local visual countdown (computed client-side from phaseDeadlineMs; zero network ticks!)
  useEffect(() => {
    if (!view?.phaseDeadlineMs) return;
    const id = window.setInterval(() => setNowTickMs(Date.now()), 500);
    return () => window.clearInterval(id);
  }, [view?.phaseDeadlineMs]);

  const remainingSec = useMemo(() => {
    if (!view?.phaseDeadlineMs) return null;
    return Math.max(0, Math.ceil((view.phaseDeadlineMs - nowTickMs) / 1000));
  }, [view?.phaseDeadlineMs, nowTickMs]);

  const runAction = useCallback(
    async (actionName: string, body: Record<string, unknown> = {}) => {
      if (!token || busy) return;
      setBusy(true);
      setFeedback(null);
      try {
        const res = await postGameAction(token, actionName, body, requestRefresh);
        setView(res.view);
        if (res.message) {
          setFeedback({ text: res.message, isErr: false });
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

  // Waiting for Teamie parent postMessage handshake (or standalone preview)
  if (!bridgeState.ready || !bridgeState.context) {
    return (
      <main className="ww-drawer">
        <section className="ww-card ww-handshake-wait">
          <div style={{ fontSize: "24px" }}>🐺</div>
          <strong>狼人杀裁判台</strong>
          <span>
            正在等待 Teamie 群聊宿主完成安全握手（<code>teamie:mcp-app:init</code>）…
          </span>
          <small style={{ color: "var(--text-faint)" }}>
            请在 Teamie 群聊右侧「群聊应用」面板中打开本应用。
          </small>
        </section>
      </main>
    );
  }

  if (!view) {
    return (
      <main className="ww-drawer">
        <section className="ww-card ww-handshake-wait">
          <strong>正在同步牌桌状态…</strong>
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

  const aliveSeats = seats.filter((s) => s.alive);
  const canConfigureLobby =
    phase === "LOBBY" && (seats.length === 0 || seats[0]?.isMe);

  return (
    <main className={`ww-drawer ${phase === "NIGHT" ? "phase-night" : ""}`}>
      {/* 1. Top Status & Secret Role Card */}
      <section className="ww-card">
        <div className="ww-header-row">
          <span
            className={`ww-phase-pill ${
              phase === "NIGHT" ? "is-night" : phase === "GAME_OVER" ? "is-over" : ""
            }`}
          >
            {phase === "NIGHT" ? "🌙" : phase === "GAME_OVER" ? "🏆" : "☀️"}{" "}
            {round > 0 ? `第${round}${phase === "NIGHT" ? "夜" : "天"} · ` : ""}
            {PHASE_LABELS[phase]}
          </span>

          <div style={{ display: "flex", alignItems: "center", gap: "5px" }}>
            <span className="ww-mini-tag">
              {preset === "9p" ? "9人猎人局" : "6人标准局"}
            </span>
            {remainingSec !== null && phase !== "LOBBY" && phase !== "GAME_OVER" && (
              <span className={`ww-timer-badge ${remainingSec <= 15 ? "urgent" : ""}`}>
                ⏱ {remainingSec}s
              </span>
            )}
          </div>
        </div>

        <div className="ww-me-strip">
          <div className="ww-me-name" title={me.displayName}>
            {me.seated ? `${me.seat}号 · ${me.displayName}` : me.displayName}
            {me.seated && !me.alive ? "（已出局）" : ""}
          </div>
          {me.role ? (
            <button
              type="button"
              className={`ww-role-chip ${hideRole ? "" : `role-${me.role}`}`}
              onClick={() => setHideRole((v) => !v)}
              title="点击切换隐藏/显示底牌（防窥屏）"
            >
              {hideRole ? "🙈 底牌已隐藏" : `🎴 ${me.roleLabel || ROLE_LABELS[me.role]}`}
            </button>
          ) : (
            <span className="ww-mini-tag">{me.seated ? "等待发牌" : "未入座"}</span>
          )}
        </div>
      </section>

      {/* 2. Public Chat Policy Banner */}
      <section
        className={`ww-policy-banner ${
          !publicChatPolicy.allowed
            ? "locked"
            : isMyMic
              ? "my-mic"
              : phase === "DAY_SPEECH"
                ? "wait-mic"
                : ""
        }`}
      >
        {!publicChatPolicy.allowed ? "🤫 " : isMyMic ? "🎙️ " : "💬 "}
        {publicChatPolicy.reason}
      </section>

      {/* 3. Seat Grid */}
      <section className="ww-card">
        <div className="ww-section-title">
          <span>
            座位席（{seats.length}/{targetPlayers}）
          </span>
          {view.speech && phase === "DAY_SPEECH" && (
            <span>当前持麦：{view.speech.currentSpeakerSeat}号</span>
          )}
        </div>

        {seats.length === 0 ? (
          <div style={{ color: "var(--text-faint)", fontSize: "12px", padding: "6px 0" }}>
            暂无玩家入座，点击下方【入座参赛】开启牌局（AI Agent 也可通过 join_lobby 入座）。
          </div>
        ) : (
          <div className="ww-seats-grid">
            {seats.map((s) => (
              <div
                key={s.seat}
                className={`ww-seat-tile ${s.isMe ? "is-me" : ""} ${
                  s.isSpeaking ? "is-speaking" : ""
                } ${!s.alive ? "is-dead" : ""}`}
              >
                <div className="ww-seat-top">
                  <span>#{s.seat}</span>
                  <span>{!s.alive ? "💀出局" : s.isMe ? "我" : "存活"}</span>
                </div>
                <div className="ww-seat-name" title={s.displayName}>
                  {s.displayName}
                </div>
                <div className="ww-seat-tags">
                  {s.isSpeaking && <span className="ww-mini-tag mic">🎙️持麦</span>}
                  {!s.isSpeaking && s.hasSpoken && (
                    <span className="ww-mini-tag">已发言</span>
                  )}
                  {phase === "DAY_VOTE" && s.hasVoted && (
                    <span className="ww-mini-tag">已暗投</span>
                  )}
                  {s.knownRole && !hideRole && (
                    <span
                      className={`ww-mini-tag ${
                        s.knownRole === "WEREWOLF" ? "wolf" : ""
                      }`}
                    >
                      {ROLE_LABELS[s.knownRole]}
                    </span>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </section>

      {/* 4. Feedback Banner */}
      {feedback && (
        <div className={`ww-feedback ${feedback.isErr ? "err" : ""}`}>
          {feedback.text}
        </div>
      )}

      {/* 5. Contextual Action Console */}
      {phase === "LOBBY" && (
        <section className="ww-card">
          <div className="ww-section-title">
            <span>开局准备</span>
            <span>{preset === "9p" ? "3狼3民·预女猎" : "2狼2民·预女"}</span>
          </div>

          {canConfigureLobby && (
            <div className="ww-btn-row" style={{ marginBottom: "8px" }}>
              {(["6p", "9p"] as Preset[]).map((p) => (
                <button
                  key={p}
                  type="button"
                  disabled={busy || (p === "6p" && seats.length > 6)}
                  className={`ww-btn ${preset === p ? "ww-btn-selected" : ""}`}
                  style={{ flex: 1 }}
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
                className="ww-btn ww-btn-primary ww-btn-full"
                onClick={() => void runAction("join_lobby", { preset })}
              >
                🪑 入座参赛（{seats.length}/{targetPlayers}）
              </button>
            ) : (
              <>
                <button
                  type="button"
                  disabled={busy}
                  className="ww-btn"
                  onClick={() => void runAction("leave_lobby")}
                >
                  离开座位
                </button>
                <button
                  type="button"
                  disabled={busy || seats.length !== targetPlayers}
                  className="ww-btn ww-btn-primary"
                  style={{ flex: 1 }}
                  onClick={() => void runAction("start_game", { preset })}
                >
                  {seats.length === targetPlayers
                    ? "🎲 立即发牌开局"
                    : `等待满员 (${seats.length}/${targetPlayers})`}
                </button>
              </>
            )}
          </div>
        </section>
      )}

      {/* NIGHT: Wolf Dark Channel + Night Skill Actions */}
      {phase === "NIGHT" && me.seated && (
        <section className="ww-card" style={{ display: "flex", flexDirection: "column", gap: "8px" }}>
          <div className="ww-section-title">
            <span>🌙 夜间行动台</span>
            {me.actionHint && <span style={{ fontWeight: 400 }}>{me.pendingAction || "等待中"}</span>}
          </div>

          {/* Werewolf Exclusive Dark Channel & Kill Selector */}
          {me.role === "WEREWOLF" && (
            <>
              <div className="ww-wolf-box">
                <div style={{ fontSize: "11.5px", fontWeight: 700 }}>
                  🐺 狼人加密暗频（仅狼队友可见）
                  {priv?.wolfTeammateSeats && priv.wolfTeammateSeats.length > 0 && (
                    <span> · 狼队座位：{priv.wolfTeammateSeats.join("、")}号</span>
                  )}
                </div>
                <div className="ww-wolf-log">
                  {!priv?.wolfChat || priv.wolfChat.length === 0 ? (
                    <span style={{ opacity: 0.75 }}>暂无暗频消息，先和狼队友商量刀法吧。</span>
                  ) : (
                    priv.wolfChat.map((c) => (
                      <div key={c.seq}>
                        <b>[{c.seat}号]</b> {c.message}
                      </div>
                    ))
                  )}
                </div>
                {me.alive && (
                  <form
                    className="ww-wolf-form"
                    onSubmit={(e) => {
                      e.preventDefault();
                      const text = wolfDraft.trim();
                      if (!text) return;
                      setWolfDraft("");
                      void runAction("wolf_chat", { message: text });
                    }}
                  >
                    <input
                      className="ww-input"
                      placeholder="输入暗频战术（公屏不可见）…"
                      value={wolfDraft}
                      maxLength={240}
                      onChange={(e) => setWolfDraft(e.target.value)}
                    />
                    <button type="submit" disabled={busy || !wolfDraft.trim()} className="ww-btn">
                      发送
                    </button>
                  </form>
                )}
              </div>

              {me.alive && me.pendingAction === "cast_night_action" && (
                <div>
                  <div style={{ fontSize: "11.5px", fontWeight: 600 }}>
                    选择今夜袭击目标
                    {priv?.wolfIntentions && Object.keys(priv.wolfIntentions).length > 0 && (
                      <span style={{ color: "var(--text-muted)", fontWeight: 400 }}>
                        {" "}
                        （已选：
                        {Object.entries(priv.wolfIntentions)
                          .map(([w, t]) => `${w}号→${Number(t) === 0 ? "空刀" : `${t}号`}`)
                          .join("，")}
                        ）
                      </span>
                    )}
                  </div>
                  <div className="ww-target-grid">
                    {aliveSeats.map((s) => (
                      <button
                        key={s.seat}
                        type="button"
                        disabled={busy}
                        className="ww-btn ww-btn-danger"
                        onClick={() =>
                          void runAction("cast_night_action", {
                            actionType: "kill",
                            targetSeat: s.seat,
                          })
                        }
                      >
                        刀 {s.seat}号
                      </button>
                    ))}
                    <button
                      type="button"
                      disabled={busy}
                      className="ww-btn"
                      onClick={() =>
                        void runAction("cast_night_action", {
                          actionType: "pass",
                          targetSeat: 0,
                        })
                      }
                    >
                      空刀
                    </button>
                  </div>
                </div>
              )}
            </>
          )}

          {/* Seer Check Panel */}
          {me.role === "SEER" && (
            <>
              {me.alive && me.pendingAction === "cast_night_action" ? (
                <div>
                  <div style={{ fontSize: "11.5px", fontWeight: 600 }}>
                    🔮 选择一名存活玩家查验阵营：
                  </div>
                  <div className="ww-target-grid">
                    {aliveSeats
                      .filter((s) => !s.isMe)
                      .map((s) => (
                        <button
                          key={s.seat}
                          type="button"
                          disabled={busy}
                          className="ww-btn ww-btn-primary"
                          onClick={() =>
                            void runAction("cast_night_action", {
                              actionType: "check",
                              targetSeat: s.seat,
                            })
                          }
                        >
                          查验 {s.seat}号
                        </button>
                      ))}
                  </div>
                </div>
              ) : (
                <div style={{ fontSize: "12px", color: "var(--text-muted)" }}>
                  🔮 本夜查验已完成，请静候天亮后再择机跳身份或报查验。
                </div>
              )}

              {priv?.seerChecks && priv.seerChecks.length > 0 && (
                <div className="ww-log-item">
                  <b>历史查验记录：</b>
                  {priv.seerChecks.map((r) => (
                    <div key={r.round}>
                      第{r.round}夜：{r.targetSeat}号（{r.displayName}）→{" "}
                      <b style={{ color: r.alignment === "WOLF" ? "var(--danger)" : "var(--success)" }}>
                        {r.alignment === "WOLF" ? "狼人（查杀）" : "好人（金水）"}
                      </b>
                    </div>
                  ))}
                </div>
              )}
            </>
          )}

          {/* Witch Potion Panel */}
          {me.role === "WITCH" && priv?.witchState && (
            <div>
              <div className="ww-btn-row" style={{ marginBottom: "6px" }}>
                <span className="ww-mini-tag">
                  🧪 解药：{priv.witchState.antidoteLeft ? "可用" : "已用尽"}
                </span>
                <span className="ww-mini-tag">
                  ☠️ 毒药：{priv.witchState.poisonLeft ? "可用" : "已用尽"}
                </span>
              </div>

              {me.alive && priv.witchState.canActNow ? (
                <div style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
                  {priv.witchState.antidoteLeft && (
                    <div className="ww-log-item">
                      {priv.witchState.tonightAttackedSeat &&
                      priv.witchState.tonightAttackedSeat > 0 ? (
                        <div
                          style={{
                            display: "flex",
                            alignItems: "center",
                            justifyContent: "space-between",
                            gap: "6px",
                          }}
                        >
                          <span>
                            今夜 <b>{priv.witchState.tonightAttackedSeat}号</b> 遇袭倒牌！
                          </span>
                          <button
                            type="button"
                            disabled={busy}
                            className="ww-btn ww-btn-primary"
                            onClick={() =>
                              void runAction("cast_night_action", { actionType: "save" })
                            }
                          >
                            使用解药救人
                          </button>
                        </div>
                      ) : (
                        <span>今夜狼人选择空刀，无人遇袭。</span>
                      )}
                    </div>
                  )}

                  {priv.witchState.poisonLeft && (
                    <div>
                      <div style={{ fontSize: "11.5px", fontWeight: 600 }}>
                        或使用毒药毒杀一名存活玩家：
                      </div>
                      <div className="ww-target-grid">
                        {aliveSeats
                          .filter((s) => !s.isMe)
                          .map((s) => (
                            <button
                              key={s.seat}
                              type="button"
                              disabled={busy}
                              className="ww-btn ww-btn-danger"
                              onClick={() =>
                                void runAction("cast_night_action", {
                                  actionType: "poison",
                                  targetSeat: s.seat,
                                })
                              }
                            >
                              毒 {s.seat}号
                            </button>
                          ))}
                      </div>
                    </div>
                  )}

                  <button
                    type="button"
                    disabled={busy}
                    className="ww-btn ww-btn-full"
                    onClick={() =>
                      void runAction("cast_night_action", { actionType: "pass" })
                    }
                  >
                    本夜不使用药水（跳过）
                  </button>
                </div>
              ) : (
                <div style={{ fontSize: "12px", color: "var(--text-muted)" }}>
                  {me.actionHint || "请等待狼人锁定目标或天亮…"}
                </div>
              )}
            </div>
          )}

          {/* Villager / Hunter / Idle Night Note */}
          {(me.role === "VILLAGER" || me.role === "HUNTER" || !me.alive) && (
            <div style={{ fontSize: "12px", color: "var(--text-muted)" }}>
              {me.actionHint || "天黑闭眼中，请耐心等待神狼行动结束进入黎明。"}
            </div>
          )}
        </section>
      )}

      {/* HUNTER REVENGE SHOT (Triggered in DAY_ANNOUNCE or DAY_VOTE) */}
      {priv?.hunterCanShoot && (
        <section className="ww-card">
          <div className="ww-section-title">
            <span>🔫 猎人临终开枪技能触发！</span>
          </div>
          <div style={{ fontSize: "12px", marginBottom: "6px" }}>
            请选择一名存活玩家开枪带走，或点击【压枪不开枪】：
          </div>
          <div className="ww-target-grid">
            {aliveSeats
              .filter((s) => s.seat !== me.seat)
              .map((s) => (
                <button
                  key={s.seat}
                  type="button"
                  disabled={busy}
                  className="ww-btn ww-btn-danger"
                  onClick={() => void runAction("hunter_shoot", { targetSeat: s.seat })}
                >
                  带走 {s.seat}号
                </button>
              ))}
            <button
              type="button"
              disabled={busy}
              className="ww-btn"
              onClick={() => void runAction("hunter_shoot", { targetSeat: 0 })}
            >
              压枪不开枪
            </button>
          </div>
        </section>
      )}

      {/* DAY_SPEECH: Strict Single-Speaker Mic Control */}
      {phase === "DAY_SPEECH" && view.speech && (
        <section className="ww-card">
          <div className="ww-section-title">
            <span>🎙️ 白天单人麦序台</span>
            <span>
              进度：{view.speech.spokenSeats.length}/{view.speech.speakingOrder.length}
            </span>
          </div>
          <div style={{ fontSize: "11.5px", color: "var(--text-muted)", marginBottom: "8px" }}>
            发言顺序：{view.speech.speakingOrder.map((n) => `${n}号`).join(" → ")}
          </div>

          {isMyMic ? (
            <div style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
              <input
                className="ww-input"
                placeholder="可选：填写一句话站边/归票摘要（记录至时间线）"
                value={speechNote}
                maxLength={80}
                onChange={(e) => setSpeechNote(e.target.value)}
              />
              <button
                type="button"
                disabled={busy}
                className="ww-btn ww-btn-primary ww-btn-full"
                onClick={() => {
                  const summary = speechNote.trim();
                  setSpeechNote("");
                  void runAction("finish_speech", summary ? { summary } : {});
                }}
              >
                ✅ 结束本轮发言（交麦给下一位）
              </button>
            </div>
          ) : (
            <div style={{ fontSize: "12px", color: "var(--text-muted)" }}>
              当前由 <b>{view.speech.currentSpeakerSeat}号</b> 在左侧群聊发言，请待其交麦后再发言。
            </div>
          )}
        </section>
      )}

      {/* DAY_VOTE: Secret Ballot Console */}
      {phase === "DAY_VOTE" && !priv?.hunterCanShoot && (
        <section className="ww-card">
          <div className="ww-section-title">
            <span>🗳️ 白天放逐暗票台</span>
            {priv?.mySubmittedVote !== undefined && (
              <span>
                已选：{priv.mySubmittedVote === 0 ? "弃票" : `${priv.mySubmittedVote}号`}
              </span>
            )}
          </div>

          {me.seated && me.alive ? (
            <>
              <div style={{ fontSize: "11.5px", color: "var(--text-muted)" }}>
                全员投完或倒计时结束前选票严格保密，最高票出局（平票无人出局）：
              </div>
              <div className="ww-target-grid">
                {aliveSeats.map((s) => (
                  <button
                    key={s.seat}
                    type="button"
                    disabled={busy}
                    className={`ww-btn ${
                      priv?.mySubmittedVote === s.seat ? "ww-btn-selected" : ""
                    }`}
                    onClick={() => void runAction("submit_vote", { targetSeat: s.seat })}
                  >
                    投 {s.seat}号
                  </button>
                ))}
                <button
                  type="button"
                  disabled={busy}
                  className={`ww-btn ${
                    priv?.mySubmittedVote === 0 ? "ww-btn-selected" : ""
                  }`}
                  onClick={() => void runAction("submit_vote", { targetSeat: 0 })}
                >
                  弃票
                </button>
              </div>
            </>
          ) : (
            <div style={{ fontSize: "12px", color: "var(--text-muted)" }}>
              正在等待存活玩家完成暗票投递…
            </div>
          )}
        </section>
      )}

      {/* GAME_OVER: Result & Play Again */}
      {phase === "GAME_OVER" && (
        <section className="ww-card">
          <div className="ww-section-title">
            <span>
              {view.winner === "GOOD" ? "🎉 好人阵营获胜！" : "🐺 狼人阵营获胜！"}
            </span>
          </div>
          <div style={{ fontSize: "12px", marginBottom: "8px" }}>{view.winReason}</div>
          <button
            type="button"
            disabled={busy}
            className="ww-btn ww-btn-primary ww-btn-full"
            onClick={() => void runAction("join_lobby", { preset })}
          >
            🔄 再来一局（重置大厅并入座）
          </button>
        </section>
      )}

      {/* 6. Public Timeline & Historical Vote Tally */}
      <section className="ww-card">
        <div className="ww-tabs">
          <button
            type="button"
            className={`ww-tab ${activeTab === "events" ? "active" : ""}`}
            onClick={() => setActiveTab("events")}
          >
            📜 牌局记录 ({view.publicEvents?.length ?? 0})
          </button>
          <button
            type="button"
            className={`ww-tab ${activeTab === "votes" ? "active" : ""}`}
            onClick={() => setActiveTab("votes")}
          >
            🗳️ 历史票型 ({view.voteHistory?.length ?? 0})
          </button>
        </div>

        {activeTab === "events" ? (
          <div className="ww-log-list">
            {!view.publicEvents || view.publicEvents.length === 0 ? (
              <div>暂无公共记录</div>
            ) : (
              [...view.publicEvents].reverse().map((ev) => (
                <div key={ev.seq} className="ww-log-item">
                  {ev.summary}
                </div>
              ))
            )}
          </div>
        ) : (
          <div className="ww-log-list">
            {!view.voteHistory || view.voteHistory.length === 0 ? (
              <div>尚未产生公示票型（投票进行中暗票严格保密）</div>
            ) : (
              [...view.voteHistory].reverse().map((vh) => (
                <div key={vh.round} className="ww-log-item">
                  <div>
                    <b>第{vh.round}天：</b>
                    {vh.summary}
                  </div>
                  <div style={{ fontSize: "11px", marginTop: "2px" }}>
                    票型明细：
                    {Object.entries(vh.votesBySeat)
                      .map(([voter, target]) =>
                        Number(target) === 0 ? `${voter}号→弃票` : `${voter}号→${target}号`,
                      )
                      .join("，")}
                  </div>
                </div>
              ))
            )}
          </div>
        )}
      </section>
    </main>
  );
}
