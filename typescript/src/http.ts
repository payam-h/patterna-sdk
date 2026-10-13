import {
  ApiError,
  ErrorCode,
  NetworkError,
  PatternaError,
  TimeoutError,
  ValidationError,
} from "./errors.js";

/** Which key a call uses. `users` covers consent mode, profile and deletion. */
export type Scope = "serve" | "ingest" | "users" | "admin";

export type FetchLike = (input: string, init: RequestInit) => Promise<Response>;

export interface TransportConfig {
  baseUrl: string;
  keys: { serve?: string; ingest?: string; admin?: string };
  /**
   * Extra headers per request, e.g. the signed-in user's session token when the SDK talks to the
   * customer's own proxy instead of Patterna. May be async (token refresh).
   */
  headers?: () => Record<string, string> | Promise<Record<string, string>>;
  fetch: FetchLike;
  retryDelayMs: number;
  /** The caller's trace id for every call (`X-Trace-Id`), unless the call names one. */
  traceId?: () => string | undefined;
}

export interface RequestSpec {
  op: string;
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  path: string;
  query?: Record<string, string>;
  body?: unknown;
  scope: Scope;
  timeoutMs: number;
  /** Safe to send twice. Only idempotent calls are retried. */
  idempotent: boolean;
  signal?: AbortSignal;
  /** Sent as X-Request-Id; minted when absent. */
  requestId?: string;
  traceId?: string;
  /** Sent as X-Deadline-Ms (recommend). */
  deadlineMs?: number;
  /** Sends X-Patterna-Tester: 1 (recommend). */
  tester?: boolean;
}

export interface HttpResult<T> {
  data: T;
  status: number;
  requestId: string | undefined;
}

const REQUEST_ID_HEADER = "x-request-id";
const MAX_HEADER_ID = 64;

/** A random UUIDv4, the form the engine mints itself. */
export function newRequestId(): string {
  const c = globalThis.crypto as Crypto | undefined;
  if (c?.randomUUID) return c.randomUUID();
  const b = new Uint8Array(16);
  if (c?.getRandomValues) c.getRandomValues(b);
  else for (let i = 0; i < 16; i++) b[i] = Math.floor(Math.random() * 256);
  b[6] = ((b[6] ?? 0) & 0x0f) | 0x40;
  b[8] = ((b[8] ?? 0) & 0x3f) | 0x80;
  const h = Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

/** Trims an id and drops it when it cannot travel as a header value. */
export function cleanHeaderId(id: string | undefined): string | undefined {
  const t = id?.trim();
  if (!t || t.length > MAX_HEADER_ID || !/^[\x21-\x7e]+$/.test(t)) return undefined;
  return t;
}

export class Transport {
  constructor(private readonly cfg: TransportConfig) {}

  async send<T>(spec: RequestSpec): Promise<HttpResult<T>> {
    spec = { ...spec, requestId: cleanHeaderId(spec.requestId) ?? newRequestId() };
    const url = this.url(spec);
    const headers = await this.headers(spec);
    const body = spec.body === undefined ? undefined : JSON.stringify(spec.body);

    // One retry, only for idempotent calls, only for failures of the service (5xx) or the network.
    // Timeouts are not retried: the budget already ran out, and a second wait doubles the latency
    // the caller is trying to bound.
    const attempts = spec.idempotent ? 2 : 1;
    let lastError: PatternaError | undefined;
    for (let attempt = 1; attempt <= attempts; attempt++) {
      if (attempt > 1 && this.cfg.retryDelayMs > 0) {
        await sleep(this.cfg.retryDelayMs, spec.signal);
      }
      try {
        return await this.once<T>(spec, url, headers, body);
      } catch (err) {
        const e = err as PatternaError;
        lastError = e;
        const retryable = e instanceof NetworkError || (e instanceof ApiError && (e.status ?? 0) >= 500);
        if (!retryable) throw e;
      }
    }
    throw lastError as PatternaError;
  }

  private async once<T>(
    spec: RequestSpec,
    url: string,
    headers: Record<string, string>,
    body: string | undefined,
  ): Promise<HttpResult<T>> {
    // AbortSignal.timeout and AbortSignal.any are missing on Hermes; wire it by hand.
    const controller = new AbortController();
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, spec.timeoutMs);
    const onCallerAbort = () => controller.abort();
    if (spec.signal) {
      if (spec.signal.aborted) controller.abort();
      else spec.signal.addEventListener("abort", onCallerAbort, { once: true });
    }

    let res: Response;
    let text: string;
    try {
      const init: RequestInit = { method: spec.method, headers, signal: controller.signal };
      if (body !== undefined) init.body = body;
      res = await this.cfg.fetch(url, init);
      text = await res.text();
    } catch (cause) {
      const requestId = spec.requestId as string;
      if (timedOut) {
        throw new TimeoutError({
          code: ErrorCode.Timeout,
          op: spec.op,
          message: `${spec.op} timed out after ${spec.timeoutMs} ms`,
          requestId,
          cause,
        });
      }
      if (spec.signal?.aborted) {
        throw new PatternaError({ code: ErrorCode.Aborted, op: spec.op, message: `${spec.op} aborted`, requestId, cause });
      }
      throw new NetworkError({
        code: ErrorCode.Network,
        op: spec.op,
        message: `${spec.op} failed: ${cause instanceof Error ? cause.message : String(cause)}`,
        requestId,
        cause,
      });
    } finally {
      clearTimeout(timer);
      spec.signal?.removeEventListener("abort", onCallerAbort);
    }

    const headerRequestId = res.headers.get(REQUEST_ID_HEADER) ?? spec.requestId;
    const parsed = parseJson(text);

    if (!res.ok) {
      const errBody = isRecord(parsed) ? parsed : {};
      const code = typeof errBody.code === "string" ? errBody.code : `http_${res.status}`;
      const requestId = typeof errBody.request_id === "string" ? errBody.request_id : headerRequestId;
      const message = typeof errBody.message === "string" ? errBody.message : `HTTP ${res.status}`;
      const init = { code, op: spec.op, status: res.status, message: `${spec.op}: ${message}` };
      throw new ApiError({
        ...init,
        ...(requestId === undefined ? {} : { requestId }),
        ...(isRecord(errBody.details) ? { details: errBody.details } : {}),
      });
    }

    if (text !== "" && parsed === INVALID) {
      throw new PatternaError({
        code: ErrorCode.BadResponse,
        op: spec.op,
        status: res.status,
        message: `${spec.op}: response is not JSON`,
        ...(headerRequestId === undefined ? {} : { requestId: headerRequestId }),
      });
    }
    return { data: (text === "" ? undefined : parsed) as T, status: res.status, requestId: headerRequestId };
  }

  private url(spec: RequestSpec): string {
    const base = this.cfg.baseUrl.replace(/\/+$/, "");
    const qs = spec.query ? new URLSearchParams(spec.query).toString() : "";
    return `${base}${spec.path}${qs ? `?${qs}` : ""}`;
  }

  private async headers(spec: RequestSpec): Promise<Record<string, string>> {
    const h: Record<string, string> = { accept: "application/json" };
    if (spec.body !== undefined) h["content-type"] = "application/json";
    const key = this.keyFor(spec.scope);
    if (key) h.authorization = `Bearer ${key}`;
    if (!key && (spec.scope === "admin" || !this.cfg.headers)) {
      throw new ValidationError({
        code: ErrorCode.NotConfigured,
        op: spec.op,
        message:
          spec.scope === "admin"
            ? "no admin key configured"
            : `no ${spec.scope === "users" ? "admin or ingest" : spec.scope} key configured and no headers() provider`,
      });
    }
    h["x-request-id"] = spec.requestId as string;
    const trace = cleanHeaderId(spec.traceId) ?? cleanHeaderId(this.cfg.traceId?.());
    if (trace) h["x-trace-id"] = trace;
    if (spec.deadlineMs !== undefined) h["x-deadline-ms"] = String(Math.max(1, Math.floor(spec.deadlineMs)));
    if (spec.tester) h["x-patterna-tester"] = "1";
    const extra = this.cfg.headers ? await this.cfg.headers() : {};
    Object.assign(h, extra);
    return h;
  }

  private keyFor(scope: Scope): string | undefined {
    const { keys } = this.cfg;
    switch (scope) {
      case "serve":
        return keys.serve;
      case "ingest":
        return keys.ingest;
      case "admin":
        return keys.admin;
      case "users":
        // The contract tags /users/* as admin. A customer backend should not need an admin key
        // for consent and deletion; until the engine settles it (docs/ISSUES.md) we use the admin
        // key when one is configured and fall back to the ingest key.
        return keys.admin ?? keys.ingest;
    }
  }
}

const INVALID = Symbol("invalid-json");

function parseJson(text: string): unknown {
  if (text === "") return undefined;
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return INVALID;
  }
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const t = setTimeout(resolve, ms);
    signal?.addEventListener(
      "abort",
      () => {
        clearTimeout(t);
        resolve();
      },
      { once: true },
    );
  });
}
