import { isCallerBug, PatternaError } from "./errors.js";

export type FallbackResult<T, F> =
  | { source: "patterna"; value: T }
  | { source: "fallback"; value: F; error: PatternaError };

export interface WithFallbackOptions {
  /**
   * `unavailable` (default): fall back on timeouts, network errors, 5xx and 429, and on every
   * other API error too, so a screen never goes blank because of Patterna. Consent and validation
   * errors, `pii_rejected` and `mode_conflict` are always rethrown: they are bugs in the caller,
   * and a consent bug is a privacy bug.
   * `timeout`: fall back only when the budget ran out; rethrow everything else.
   */
  on?: "unavailable" | "timeout";
  /** Called with the error each time the fallback is used, for your metrics. */
  onFallback?: (error: PatternaError) => void;
}

/**
 * Runs `fn` (usually `client.recommend(...)`) and returns your own list when Patterna does not
 * answer in time. The result says which one you got, so you only report interactions for a real
 * slate (a fallback has no request id).
 *
 *   const r = await withFallback(() => client.recommend(req), () => newestArticleIds);
 *   const ids = r.source === "patterna" ? r.value.items.map((i) => i.item_id) : r.value;
 */
export async function withFallback<T, F>(
  fn: () => Promise<T>,
  fallback: () => F | Promise<F>,
  options: WithFallbackOptions = {},
): Promise<FallbackResult<T, F>> {
  try {
    return { source: "patterna", value: await fn() };
  } catch (err) {
    if (!(err instanceof PatternaError)) throw err;
    if (isCallerBug(err)) throw err;
    const mode = options.on ?? "unavailable";
    if (mode === "timeout" && err.code !== "timeout") throw err;
    options.onFallback?.(err);
    return { source: "fallback", value: await fallback(), error: err };
  }
}
