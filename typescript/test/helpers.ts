import { PatternaClient, type PatternaClientOptions } from "../src/index.js";

export interface Call {
  url: string;
  method: string;
  headers: Record<string, string>;
  body: unknown;
}

type Reply =
  | { status: number; body?: unknown; headers?: Record<string, string> }
  | { networkError: string }
  | { hang: true };

/**
 * A fetch double: replies are consumed in order; the last one repeats. `hang` never answers and
 * rejects only when the request is aborted, like a real fetch does.
 */
export function mockFetch(...replies: Reply[]) {
  const calls: Call[] = [];
  let i = 0;
  const fetch = (url: string, init: RequestInit): Promise<Response> => {
    calls.push({
      url,
      method: init.method ?? "GET",
      headers: { ...(init.headers as Record<string, string>) },
      body: typeof init.body === "string" ? JSON.parse(init.body) : undefined,
    });
    const reply = replies[Math.min(i++, replies.length - 1)];
    if (!reply) throw new Error("mockFetch: no reply configured");
    if ("networkError" in reply) return Promise.reject(new TypeError(reply.networkError));
    if ("hang" in reply) {
      return new Promise((_, reject) => {
        const fail = () => reject(new DOMException("The operation was aborted.", "AbortError"));
        if (init.signal?.aborted) fail();
        init.signal?.addEventListener("abort", fail, { once: true });
      });
    }
    const text = reply.body === undefined ? "" : typeof reply.body === "string" ? reply.body : JSON.stringify(reply.body);
    return Promise.resolve(new Response(text === "" ? null : text, { status: reply.status, headers: reply.headers ?? {} }));
  };
  return { fetch, calls };
}

export const BASE = "https://api.test/v1";
// Dummy key strings; not secrets.
export const KEYS = { serve: "pk_serve_test", ingest: "pk_ingest_test" };

export function client(fetch: PatternaClientOptions["fetch"], extra: Partial<PatternaClientOptions> = {}) {
  return new PatternaClient({ baseUrl: BASE, keys: KEYS, fetch, retryDelayMs: 0, ...extra });
}

export const slate = {
  request_id: "req_1",
  surface: "home_articles",
  arm: "treatment",
  policy_version: "lin-1",
  items: [
    {
      item_id: "doc_1",
      kind: "document",
      position: 0,
      reason_key: "similar_to_read",
      reason: { nl: "Omdat je dit las", en: "Because you read this" },
    },
  ],
};

export function event(n: number, extra: Record<string, unknown> = {}) {
  return {
    event_id: `ev_${n}`,
    type: "item_viewed",
    occurred_at: "2026-10-13T10:00:00Z",
    source: "sdk" as const,
    session_id: "s_abc",
    props: { item_id: `doc_${n}` },
    ...extra,
  };
}
