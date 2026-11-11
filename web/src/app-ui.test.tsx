import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import * as api from "./api";
import type { PlayerView } from "./types";

(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;

function makeNightWolfView(): PlayerView {
  return {
    workspaceId: "ws-1",
    conversationId: "conv-1",
    preset: "6p",
    targetPlayers: 6,
    phase: "NIGHT",
    round: 2,
    eventSeq: 12,
    phaseDeadlineMs: Date.now() + 60_000,
    phaseTimeoutSec: 90,
    me: {
      userId: "u-1",
      displayName: "Alice",
      seated: true,
      seat: 1,
      role: "WEREWOLF",
      roleLabel: "狼人",
      alive: true,
      pendingAction: "cast_night_action",
    },
    seats: [
      { seat: 1, displayName: "Alice", isMe: true, alive: true, knownRole: "WEREWOLF" },
      { seat: 2, displayName: "Bob", isMe: false, alive: true, knownRole: "WEREWOLF" },
      { seat: 3, displayName: "Carol", isMe: false, alive: true },
      { seat: 4, displayName: "Dave", isMe: false, alive: false },
      { seat: 5, displayName: "Eve", isMe: false, alive: true },
      { seat: 6, displayName: "Frank", isMe: false, alive: true },
    ],
    publicChatPolicy: {
      allowed: false,
      reason: "天黑闭眼中，公屏禁止发言",
    },
    private: {
      wolfTeammateSeats: [1, 2],
      wolfCanVote: true,
      wolfIntentions: {},
      wolfChat: [
        {
          seq: 1,
          round: 2,
          seat: 2,
          displayName: "Bob",
          message: "今晚刀3号？",
          createdAt: "2026-11-06T06:00:00Z",
        },
      ],
    },
    publicEvents: [
      {
        seq: 11,
        round: 2,
        phase: "NIGHT",
        summary: "第2夜降临，全体闭眼",
        createdAt: "2026-11-06T06:00:00Z",
      },
    ],
    voteHistory: [
      {
        round: 1,
        votesBySeat: { "1": 4, "3": 4, "5": 2, "2": 0 },
        eliminatedSeat: 4,
        isTie: false,
        summary: "4号高票放逐出局",
      },
    ],
  };
}

describe("Dark Gothic Tarot App UI", () => {
  let container: HTMLDivElement;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    container.remove();
  });

  it("supports direct seat-card targeting, role card privacy flip, and grouped vote chronicle drawer", async () => {
    const fakeParent = { postMessage: vi.fn() } as unknown as Window;
    Object.defineProperty(window, "parent", {
      value: fakeParent,
      configurable: true,
    });

    const initialView = makeNightWolfView();
    vi.spyOn(api, "fetchMyView").mockResolvedValue(initialView);
    vi.spyOn(api, "subscribeViewStream").mockReturnValue(() => {});
    const postActionSpy = vi.spyOn(api, "postGameAction").mockResolvedValue({
      ok: true,
      action: "cast_night_action",
      message: "已记录袭击意向：3号",
      publicChatPolicy: initialView.publicChatPolicy,
      view: {
        ...initialView,
        private: {
          ...initialView.private,
          wolfIntentions: { "1": 3 },
        },
      },
    });

    const root = createRoot(container);
    await act(async () => {
      root.render(<App />);
    });

    // Before init: renders Tarot handshake waiting crest
    expect(container.textContent).toContain("狼人杀 · 暗金裁判台");

    // Dispatch valid host handshake
    await act(async () => {
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
              token: "jwt-tarot-test",
              expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
              workspaceId: "ws-1",
              conversationId: "conv-1",
              user: { id: "u-1", displayName: "Alice" },
            },
          },
        }),
      );
    });

    expect(container.textContent).toContain("我的底牌：狼人");
    expect(container.textContent).toContain("今晚刀3号？");
    expect(container.querySelector(".ww-astrolabe-svg")).not.toBeNull();

    // Open full-scale Tarot artwork zoom modal and dismiss via Escape key
    const zoomBtn = Array.from(container.querySelectorAll(".ww-ghost-chip")).find((b) =>
      b.textContent?.includes("立绘"),
    ) as HTMLButtonElement;
    expect(zoomBtn).toBeDefined();
    await act(async () => {
      zoomBtn.click();
    });
    expect(container.querySelector(".ww-tarot-modal")).not.toBeNull();
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    });
    expect(container.querySelector(".ww-tarot-modal")).toBeNull();

    // Flip 3D Tarot identity card to conceal role
    const card3dInner = container.querySelector(".ww-card-3d-inner") as HTMLElement;
    expect(card3dInner.classList.contains("is-flipped")).toBe(false);
    const roleBtn = container.querySelector(".ww-tarot-id") as HTMLButtonElement;
    await act(async () => {
      roleBtn.click();
    });
    expect(container.textContent).toContain("命运底牌已暗置");
    expect(card3dInner.classList.contains("is-flipped")).toBe(true);

    // Click Seat #3 (Carol) directly on the seat grid to aim
    const seatButtons = Array.from(
      container.querySelectorAll(".ww-seat-card"),
    ) as HTMLButtonElement[];
    const seat4DeadBtn = seatButtons.find((b) => b.textContent?.includes("Dave"));
    expect(seat4DeadBtn).toBeDefined();
    expect(seat4DeadBtn!.querySelector(".ww-seat-seal")?.textContent).toBe("4");
    expect(seat4DeadBtn!.textContent).toContain("✕ 已出局");

    const seat3Btn = seatButtons.find((b) => b.textContent?.includes("Carol"));
    expect(seat3Btn).toBeDefined();

    await act(async () => {
      seat3Btn!.click();
    });
    expect(seat3Btn!.textContent).toContain("已瞄准");

    // Confirm night kill via single primary crimson CTA
    const confirmKillBtn = Array.from(container.querySelectorAll("button")).find((b) =>
      b.textContent?.includes("锁定袭击 3号"),
    ) as HTMLButtonElement;
    expect(confirmKillBtn).toBeDefined();

    await act(async () => {
      confirmKillBtn.click();
    });
    expect(postActionSpy).toHaveBeenCalledWith(
      "jwt-tarot-test",
      "cast_night_action",
      { actionType: "kill", targetSeat: 3 },
      expect.any(Function),
    );

    // Open collapsible bottom chronicle dock and switch to visual vote tally
    const dockBtn = container.querySelector(".ww-chronicle-trigger") as HTMLButtonElement;
    await act(async () => {
      dockBtn.click();
    });
    const votesTabBtn = Array.from(container.querySelectorAll(".ww-seg-item")).find((b) =>
      b.textContent?.includes("历史票型"),
    ) as HTMLButtonElement;
    await act(async () => {
      votesTabBtn.click();
    });
    expect(container.textContent).toContain("1号、3号");
    expect(container.textContent).toContain("4号 (2票·出局)");

    await act(async () => {
      root.unmount();
    });
  });
});
