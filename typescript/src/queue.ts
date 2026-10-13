import { validateEvent, type PatternaClient } from "./client.js";
import { assertIdentityAllowed, assertMode } from "./consent.js";
import { ApiError, ConsentError, ErrorCode, PatternaError } from "./errors.js";
import type { Event, Mode } from "./types.js";

/**
 * The subset of React Native's AsyncStorage the queue needs. localStorage, MMKV or a file can be
 * wrapped the same way. Values are JSON strings.
 */
export interface QueueStorage {
  getItem(key: string): Promise<string | null>;
  setItem(key: string, value: string): Promise<void>;
}

export type DropReason = "overflow" | "consent" | "rejected" | "invalid";

export interface EventQueueOptions {
  client: Pick<PatternaClient, "ingestEvents">;
  storage?: QueueStorage;
  /** Default `patterna.events.v1`. */
  storageKey?: string;
  /** Oldest events are dropped beyond this. Default 1000. */
  maxSize?: number;
  /** Events per ingest call, at most 500. Default 100. */
  batchSize?: number;
  /** Events that will never be sent, and why. For metrics; never log the events themselves. */
  onDrop?: (reason: DropReason, count: number, error?: PatternaError) => void;
  /** Storage failures. The queue keeps working in memory. */
  onStorageError?: (error: unknown) => void;
}

export interface FlushResult {
  sent: number;
  dropped: number;
  remaining: number;
  /** Set when the flush stopped early on a transient failure; the events stay queued. */
  error?: PatternaError;
}

/**
 * A bounded, persistent outbox for events produced while offline or in bursts. Consent first:
 * nothing is accepted until `setMode` was called, and in `non_profiled` mode events must carry a
 * session id and no user id. Switching to `non_profiled` purges queued events that name the user.
 */
export class EventQueue {
  private events: Event[] = [];
  private currentMode: Mode | undefined;
  private flushing: Promise<FlushResult> | undefined;
  private restoring: Promise<number> | undefined;
  private writeChain: Promise<void> = Promise.resolve();
  private readonly key: string;
  private readonly maxSize: number;
  private readonly batchSize: number;

  constructor(private readonly opts: EventQueueOptions) {
    this.key = opts.storageKey ?? "patterna.events.v1";
    this.maxSize = Math.max(1, opts.maxSize ?? 1000);
    this.batchSize = Math.min(500, Math.max(1, opts.batchSize ?? 100));
  }

  get size(): number {
    return this.events.length;
  }

  get mode(): Mode | undefined {
    return this.currentMode;
  }

  /** Sets the user's consent mode. Call it before `enqueue`, and again whenever it changes. */
  async setMode(mode: Mode): Promise<void> {
    assertMode(mode, "queue.setMode");
    this.currentMode = mode;
    await this.restore();
    if (mode === "non_profiled") {
      const before = this.events.length;
      this.events = this.events.filter((e) => !e.user_id);
      const purged = before - this.events.length;
      if (purged > 0) {
        this.opts.onDrop?.("consent", purged);
        await this.persist();
      }
    }
  }

  /**
   * Loads events persisted by an earlier run and returns how many were restored. Runs once; every
   * mutation awaits it first, so an event queued at startup can never overwrite the stored queue.
   * Call it early (app start) to get the count; calling it is optional.
   */
  restore(): Promise<number> {
    this.restoring ??= this.doRestore();
    return this.restoring;
  }

  private async doRestore(): Promise<number> {
    if (!this.opts.storage) return 0;
    let raw: string | null;
    try {
      raw = await this.opts.storage.getItem(this.key);
    } catch (err) {
      this.opts.onStorageError?.(err);
      return 0;
    }
    if (!raw) return 0;
    let stored: unknown;
    try {
      stored = JSON.parse(raw);
    } catch (err) {
      // A corrupt blob must not wedge the app; start empty.
      this.opts.onStorageError?.(err);
      return 0;
    }
    if (!Array.isArray(stored)) return 0;
    const restored = stored.filter(
      (e): e is Event => typeof e === "object" && e !== null && typeof (e as Event).event_id === "string",
    );
    const known = new Set(this.events.map((e) => e.event_id));
    let merged = [...restored.filter((e) => !known.has(e.event_id)), ...this.events];
    if (this.currentMode === "non_profiled") {
      const before = merged.length;
      merged = merged.filter((e) => !e.user_id);
      if (merged.length < before) this.opts.onDrop?.("consent", before - merged.length);
    }
    this.events = merged;
    this.trim();
    await this.persist();
    return restored.length;
  }

  /** Validates, consent-checks and queues one event. Throws ConsentError or ValidationError. */
  async enqueue(event: Event): Promise<void> {
    const op = "queue.enqueue";
    if (this.currentMode === undefined) {
      throw new ConsentError({
        code: ErrorCode.ConsentRequired,
        op,
        message: "set the consent mode with setMode() before queueing events",
      });
    }
    assertIdentityAllowed(this.currentMode, event, op);
    validateEvent(event, op);
    await this.restore();
    // event_id is the idempotency key; a second copy would only cost bandwidth.
    if (this.events.some((e) => e.event_id === event.event_id)) return;
    this.events.push(event);
    this.trim();
    await this.persist();
  }

  /** Sends queued events in batches. Concurrent calls share one flush. */
  flush(): Promise<FlushResult> {
    this.flushing ??= this.doFlush().finally(() => {
      this.flushing = undefined;
    });
    return this.flushing;
  }

  /** Drops everything, e.g. on logout or account deletion. */
  async clear(): Promise<void> {
    await this.restore();
    this.events = [];
    await this.persist();
  }

  private async doFlush(): Promise<FlushResult> {
    await this.restore();
    let sent = 0;
    let dropped = 0;
    while (this.events.length > 0) {
      const batch = this.events.slice(0, this.batchSize);
      try {
        const res = await this.opts.client.ingestEvents(batch);
        const rejected = res.rejected?.length ?? 0;
        sent += batch.length - rejected;
        if (rejected > 0) {
          dropped += rejected;
          this.opts.onDrop?.("rejected", rejected);
        }
        this.remove(batch);
        await this.persist();
      } catch (err) {
        if (!(err instanceof PatternaError)) throw err;
        if (err.transient) {
          return { sent, dropped, remaining: this.events.length, error: err };
        }
        if (err instanceof ApiError && (err.status === 401 || err.status === 403)) {
          // A key problem, not an event problem: keep everything for when it is fixed.
          throw err;
        }
        // 400/422 or a local validation error: this batch will never be accepted.
        dropped += batch.length;
        this.opts.onDrop?.("invalid", batch.length, err);
        this.remove(batch);
        await this.persist();
      }
    }
    return { sent, dropped, remaining: 0 };
  }

  private remove(batch: Event[]): void {
    const gone = new Set(batch);
    this.events = this.events.filter((e) => !gone.has(e));
  }

  private trim(): void {
    const over = this.events.length - this.maxSize;
    if (over > 0) {
      this.events.splice(0, over);
      this.opts.onDrop?.("overflow", over);
    }
  }

  private persist(): Promise<void> {
    const storage = this.opts.storage;
    if (!storage) return Promise.resolve();
    // Serialize writes so a slow earlier write never overwrites a newer snapshot.
    const snapshot = JSON.stringify(this.events);
    this.writeChain = this.writeChain
      .then(() => storage.setItem(this.key, snapshot))
      .catch((err: unknown) => this.opts.onStorageError?.(err));
    return this.writeChain;
  }
}
