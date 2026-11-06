import { useCallback, useEffect, useRef, useState } from "react";
import { WerewolfEmblem } from "./icons";
import type { Preset } from "./types";

interface DevPlayerSession {
  slot: number;
  userId: string;
  displayName: string;
  token: string;
  expiresAt: string;
}

interface DevTokensResponse {
  appId: string;
  workspaceId: string;
  conversationId: string;
  preset: Preset;
  players: DevPlayerSession[];
}

/**
 * Local-only multi-perspective debugging workbench (/dev, active strictly when DEV_SIMULATOR=true).
 * Renders 6 (or 9) real child <iframe src="/"> instances side-by-side and drives them through
 * the authentic Teamie postMessage protocol (ready -> init -> refresh-token).
 */
export function DevSimulator() {
  const [preset, setPreset] = useState<Preset>("6p");
  const [convId, setConvId] = useState("00000000-0000-4000-8000-devconvroom001");
  const [data, setData] = useState<DevTokensResponse | null>(null);
  const [error, setError] = useState("");
  const iframeRefs = useRef<Record<number, HTMLIFrameElement | null>>({});

  const loadSessions = useCallback(
    async (nextConvId: string, nextPreset: Preset, quickAction = "") => {
      setError("");
      try {
        const res = await fetch("/api/dev/tokens", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            conversationId: nextConvId,
            preset: nextPreset,
            quickAction,
          }),
        });
        if (!res.ok) {
          throw new Error(
            res.status === 404
              ? "当前未开启 DEV_SIMULATOR=true，联调台在生产环境已安全关闭。"
              : `请求联调 Token 失败 (${res.status})`,
          );
        }
        const json = (await res.json()) as DevTokensResponse;
        setData(json);
      } catch (e: unknown) {
        setError(e instanceof Error ? e.message : "加载联调台失败");
      }
    },
    [],
  );

  useEffect(() => {
    void loadSessions(convId, preset);
  }, [convId, preset, loadSessions]);

  const sendInitToSlot = useCallback(
    (slot: number, sessionData: DevTokensResponse | null) => {
      if (!sessionData) return;
      const player = sessionData.players.find((p) => p.slot === slot);
      const win = iframeRefs.current[slot]?.contentWindow;
      if (!player || !win) return;

      win.postMessage(
        {
          type: "teamie:mcp-app:init",
          payload: {
            appId: sessionData.appId,
            slug: "werewolf",
            name: "狼人杀裁判台",
            webUrl: `${window.location.origin}/`,
            mcpUrl: `${window.location.origin}/mcp`,
            token: player.token,
            expiresAt: player.expiresAt,
            workspaceId: sessionData.workspaceId,
            conversationId: sessionData.conversationId,
            user: {
              id: player.userId,
              displayName: player.displayName,
            },
          },
        },
        window.location.origin,
      );
    },
    [],
  );

  // Push refreshed tokens to all mounted iframes without reloading them
  useEffect(() => {
    if (!data) return;
    for (const p of data.players) {
      sendInitToSlot(p.slot, data);
    }
  }, [data, sendInitToSlot]);

  // Listen for teamie:mcp-app:ready and teamie:mcp-app:refresh-token from child iframes
  useEffect(() => {
    const onMessage = (event: MessageEvent) => {
      if (event.origin !== window.location.origin) return;
      const msgType = (event.data as { type?: string } | null)?.type;
      if (msgType !== "teamie:mcp-app:ready" && msgType !== "teamie:mcp-app:refresh-token") {
        return;
      }
      if (!data) return;
      for (const p of data.players) {
        if (iframeRefs.current[p.slot]?.contentWindow === event.source) {
          if (msgType === "teamie:mcp-app:ready") {
            sendInitToSlot(p.slot, data);
          } else {
            void loadSessions(convId, preset);
          }
          break;
        }
      }
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [data, convId, preset, loadSessions, sendInitToSlot]);

  return (
    <div className="dev-shell">
      <header className="dev-toolbar">
        <div style={{ display: "flex", alignItems: "center", gap: "10px" }}>
          <div className="ww-emblem-medallion">
            <WerewolfEmblem className="ww-emblem-svg" />
          </div>
          <div>
            <strong style={{ fontSize: "15px", color: "var(--gold-bright)" }}>
              狼人杀裁判台 · 多视角暗金迷雾联调台（/dev）
            </strong>
            <div style={{ fontSize: "12px", color: "var(--text-muted)" }}>
              房间 ID：<code>{convId}</code> · 每个抽屉均为真实{" "}
              <code>&lt;iframe sandbox&gt;</code> + <code>postMessage</code> 注入独立 HS256 Token
            </div>
          </div>
        </div>

        <div className="ww-btn-row" style={{ flexWrap: "wrap" }}>
          <button
            type="button"
            className={`ww-btn ${preset === "6p" ? "ww-btn-active" : ""}`}
            onClick={() => setPreset("6p")}
          >
            6人标准局
          </button>
          <button
            type="button"
            className={`ww-btn ${preset === "9p" ? "ww-btn-active" : ""}`}
            onClick={() => setPreset("9p")}
          >
            9人猎人局
          </button>
          <button
            type="button"
            className="ww-btn"
            onClick={() => void loadSessions(convId, preset, "seat_all")}
          >
            全员入座
          </button>
          <button
            type="button"
            className="ww-btn ww-btn-gold"
            onClick={() => void loadSessions(convId, preset, "start_game")}
          >
            ✦ 满员发牌开局
          </button>
          <button
            type="button"
            className="ww-btn"
            onClick={() =>
              setConvId(
                `00000000-0000-4000-8000-dev${Math.random().toString(16).slice(2, 11).padEnd(9, "0")}`,
              )
            }
          >
            新建测试房间
          </button>
        </div>
      </header>

      {error && (
        <div className="ww-toast err" style={{ marginBottom: "12px" }}>
          <span>{error}</span>
        </div>
      )}

      {data && (
        <div className="dev-frames-grid">
          {data.players.map((p) => (
            <div key={`${convId}-${p.slot}`} className="dev-frame-wrap">
              <div className="dev-frame-header">
                <span>
                  视角 #{p.slot} · {p.displayName}
                </span>
                <code style={{ fontSize: "11px", fontWeight: 400, color: "var(--text-muted)" }}>
                  sub={p.userId.slice(-8)}
                </code>
              </div>
              <iframe
                ref={(el) => {
                  iframeRefs.current[p.slot] = el;
                }}
                title={`模拟抽屉 ${p.displayName}`}
                src="/"
                sandbox="allow-scripts allow-forms allow-same-origin"
                className="dev-iframe"
                onLoad={() => sendInitToSlot(p.slot, data)}
              />
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
