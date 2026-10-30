import type { ActionResponse, PlayerView } from "./types";

export class UnauthorizedError extends Error {
  constructor(message = "登录凭证已过期，正在静默刷新…") {
    super(message);
    this.name = "UnauthorizedError";
  }
}

function makeIdempotencyKey(prefix: string): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return `${prefix}-${crypto.randomUUID()}`;
  }
  return `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

export async function fetchMyView(
  token: string,
  onUnauthorized: () => void,
): Promise<PlayerView> {
  const res = await fetch("/api/view", {
    method: "GET",
    headers: {
      Authorization: `Bearer ${token}`,
      Accept: "application/json",
    },
  });
  if (res.status === 401) {
    onUnauthorized();
    throw new UnauthorizedError();
  }
  if (!res.ok) {
    const errBody = (await res.json().catch(() => ({}))) as { message?: string };
    throw new Error(errBody.message || `获取房间状态失败 (${res.status})`);
  }
  return (await res.json()) as PlayerView;
}

export async function postGameAction(
  token: string,
  actionName: string,
  body: Record<string, unknown>,
  onUnauthorized: () => void,
): Promise<ActionResponse> {
  const idemKey = makeIdempotencyKey(actionName);
  const res = await fetch(`/api/action/${actionName}`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
      "Idempotency-Key": idemKey,
    },
    body: JSON.stringify(body),
  });
  if (res.status === 401) {
    onUnauthorized();
    throw new UnauthorizedError();
  }
  const payload = (await res.json().catch(() => ({}))) as ActionResponse & {
    message?: string;
  };
  if (!res.ok) {
    throw new Error(payload.message || `操作失败 (${res.status})`);
  }
  return payload;
}

/**
 * Connects to /api/stream using fetch + ReadableStream so the Context Token is sent
 * strictly in the "Authorization: Bearer <token>" header (never leaked in URL query params).
 */
export function subscribeViewStream(opts: {
  token: string;
  onView: (view: PlayerView) => void;
  onUnauthorized: () => void;
}): () => void {
  const controller = new AbortController();
  let stopped = false;

  const run = async () => {
    while (!stopped && !controller.signal.aborted) {
      try {
        const res = await fetch("/api/stream", {
          method: "GET",
          headers: {
            Authorization: `Bearer ${opts.token}`,
            Accept: "text/event-stream",
          },
          signal: controller.signal,
        });
        if (res.status === 401) {
          opts.onUnauthorized();
          return;
        }
        if (!res.ok || !res.body) {
          await sleep(2000);
          continue;
        }

        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";

        while (!stopped) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });

          let boundary = buffer.indexOf("\n\n");
          while (boundary !== -1) {
            const rawEvent = buffer.slice(0, boundary);
            buffer = buffer.slice(boundary + 2);
            boundary = buffer.indexOf("\n\n");

            const lines = rawEvent.split("\n");
            let dataStr = "";
            for (const line of lines) {
              if (line.startsWith("data:")) {
                dataStr += line.slice(5).trim();
              }
            }
            if (dataStr) {
              try {
                const view = JSON.parse(dataStr) as PlayerView;
                opts.onView(view);
              } catch {
                // Ignore malformed keepalive or partial comment
              }
            }
          }
        }
      } catch {
        if (stopped || controller.signal.aborted) return;
      }
      await sleep(1500);
    }
  };

  void run();

  return () => {
    stopped = true;
    controller.abort();
  };
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
