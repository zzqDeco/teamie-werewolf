import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { TeamieHostBridge } from "./bridge";

describe("TeamieHostBridge postMessage protocol compliance", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("posts teamie:mcp-app:ready, locks hostOrigin on first init, rejects spoofed source/origin, and never touches localStorage", () => {
    const parentPostMessage = vi.fn();
    const fakeParent = { postMessage: parentPostMessage } as unknown as Window;
    const states: ReturnType<TeamieHostBridge["getState"]>[] = [];

    const storageSetSpy = vi.fn();
    Object.defineProperty(window, "localStorage", {
      value: { setItem: storageSetSpy, getItem: vi.fn(), removeItem: vi.fn(), clear: vi.fn(), length: 0 },
      configurable: true,
    });

    const nowMs = Date.parse("2026-10-30T14:00:00Z");
    const bridge = new TeamieHostBridge({
      win: window,
      parentWin: fakeParent,
      now: () => nowMs,
      onUpdate: (st) => states.push(st),
    });

    bridge.start();
    expect(parentPostMessage).toHaveBeenCalledWith(
      { type: "teamie:mcp-app:ready" },
      "*",
    );

    // 1. Ignore message NOT from window.parent
    window.dispatchEvent(
      new MessageEvent("message", {
        source: window,
        origin: "https://evil.example.com",
        data: {
          type: "teamie:mcp-app:init",
          payload: {
            appId: "app-1",
            token: "stolen-token",
            expiresAt: "2026-10-30T15:00:00Z",
            workspaceId: "ws-1",
            conversationId: "conv-1",
            user: { id: "u-1", displayName: "Evil" },
          },
        },
      }),
    );
    expect(states).toHaveLength(0);

    // 2. Accept valid first init from window.parent and lock hostOrigin
    window.dispatchEvent(
      new MessageEvent("message", {
        source: fakeParent,
        origin: "https://120.25.210.202",
        data: {
          type: "teamie:mcp-app:init",
          payload: {
            appId: "app-1",
            slug: "werewolf",
            name: "狼人杀裁判台",
            webUrl: "https://120.25.210.202:9443/",
            token: "jwt-token-v1",
            expiresAt: "2026-10-30T15:00:00Z",
            workspaceId: "ws-1",
            conversationId: "conv-1",
            user: { id: "u-1", displayName: "Alice" },
          },
        },
      }),
    );

    expect(states).toHaveLength(1);
    expect(states[0].hostOrigin).toBe("https://120.25.210.202");
    expect(states[0].context?.token).toBe("jwt-token-v1");
    expect(storageSetSpy).not.toHaveBeenCalled();

    // 3. Reject subsequent message from a different origin even if source were spoofed
    window.dispatchEvent(
      new MessageEvent("message", {
        source: fakeParent,
        origin: "https://other-origin.example.com",
        data: {
          type: "teamie:mcp-app:init",
          payload: {
            appId: "app-1",
            token: "jwt-token-hijack",
            expiresAt: "2026-10-30T15:00:00Z",
            workspaceId: "ws-1",
            conversationId: "conv-1",
            user: { id: "u-1", displayName: "Alice" },
          },
        },
      }),
    );
    expect(bridge.getState().context?.token).toBe("jwt-token-v1");

    // 4. Advance clock to 55m01s (< 5 min before 1h expiry) -> auto refresh-token to locked hostOrigin
    vi.advanceTimersByTime(55 * 60 * 1000 + 1000);
    expect(parentPostMessage).toHaveBeenCalledWith(
      { type: "teamie:mcp-app:refresh-token" },
      "https://120.25.210.202",
    );

    bridge.stop();
  });
});
