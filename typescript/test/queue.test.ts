import { describe, expect, it, vi } from "vitest";
import { ConsentError, ErrorCode, EventQueue, ValidationError, type QueueStorage } from "../src/index.js";
import { client, event, mockFetch } from "./helpers.js";

/** In-memory AsyncStorage stand-in that records every write. */
function memoryStorage(initial: Record<string, string> = {}) {
  const data = new Map(Object.entries(initial));
  const writes: string[] = [];
  const storage: QueueStorage = {
    getItem: async (k) => data.get(k) ?? null,
    setItem: async (k, v) => {
      writes.push(v);
      data.set(k, v);
    },
  };
  return { storage, data, writes };
}

const ok = (n: number) => ({ status: 202, body: { accepted: n, duplicates: 0, rejected: [] } });

describe("EventQueue consent", () => {
  it("refuses events until a mode is set", async () => {
    const q = new EventQueue({ client: client(mockFetch(ok(1)).fetch) });
    const err = await q.enqueue(event(1)).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ConsentError);
    expect(err).toMatchObject({ code: ErrorCode.ConsentRequired });
    expect(q.size).toBe(0);
  });

  it("throws on a user id in non_profiled mode", async () => {
    const q = new EventQueue({ client: client(mockFetch(ok(1)).fetch) });
    await q.setMode("non_profiled");
    await expect(q.enqueue(event(1, { user_id: "u_1" }))).rejects.toMatchObject({ code: ErrorCode.ConsentViolation });
    await q.enqueue(event(2));
    expect(q.size).toBe(1);
  });

  it("purges queued events that name the user when consent drops to non_profiled", async () => {
    const onDrop = vi.fn();
    const mem = memoryStorage();
    const q = new EventQueue({ client: client(mockFetch(ok(1)).fetch), storage: mem.storage, onDrop });
    await q.setMode("personalized");
    await q.enqueue(event(1, { user_id: "u_1" }));
    await q.enqueue(event(2));
    await q.setMode("non_profiled");
    expect(q.size).toBe(1);
    expect(onDrop).toHaveBeenCalledWith("consent", 1);
    expect(mem.data.get("patterna.events.v1")).not.toContain("u_1");
  });

  it("validates events on the way in", async () => {
    const q = new EventQueue({ client: client(mockFetch(ok(1)).fetch) });
    await q.setMode("cohort");
    await expect(q.enqueue(event(1, { context: { email: "x" } }))).rejects.toBeInstanceOf(ValidationError);
  });
});

describe("EventQueue bounds and persistence", () => {
  it("drops the oldest events beyond maxSize", async () => {
    const onDrop = vi.fn();
    const q = new EventQueue({ client: client(mockFetch(ok(1)).fetch), maxSize: 3, onDrop });
    await q.setMode("cohort");
    for (let i = 1; i <= 5; i++) await q.enqueue(event(i));
    expect(q.size).toBe(3);
    expect(onDrop).toHaveBeenCalledWith("overflow", 1);
    const m = mockFetch(ok(3));
    const q2 = q as unknown as { opts: { client: unknown } };
    q2.opts.client = client(m.fetch);
    await q.flush();
    const sent = (m.calls[0]!.body as { events: { event_id: string }[] }).events.map((e) => e.event_id);
    expect(sent).toEqual(["ev_3", "ev_4", "ev_5"]);
  });

  it("persists every change and restores it in a new process", async () => {
    const mem = memoryStorage();
    const first = new EventQueue({ client: client(mockFetch(ok(1)).fetch), storage: mem.storage });
    await first.setMode("cohort");
    await first.enqueue(event(1));
    await first.enqueue(event(2));
    expect(mem.writes).toHaveLength(2);

    const m = mockFetch(ok(3));
    const second = new EventQueue({ client: client(m.fetch), storage: mem.storage });
    await second.setMode("cohort");
    await second.enqueue(event(3)); // queued at startup: must not overwrite the stored queue
    expect(await second.restore()).toBe(2);
    expect(second.size).toBe(3);
    const res = await second.flush();
    expect(res).toEqual({ sent: 3, dropped: 0, remaining: 0 });
    const sent = (m.calls[0]!.body as { events: { event_id: string }[] }).events.map((e) => e.event_id);
    expect(sent).toEqual(["ev_1", "ev_2", "ev_3"]);
    expect(mem.data.get("patterna.events.v1")).toBe("[]");
  });

  it("deduplicates restored events by event_id and drops named events when restoring into non_profiled", async () => {
    const stored = JSON.stringify([event(1), event(2, { user_id: "u_9" }), { junk: true }]);
    const mem = memoryStorage({ "patterna.events.v1": stored });
    const q = new EventQueue({ client: client(mockFetch(ok(1)).fetch), storage: mem.storage });
    await q.setMode("non_profiled");
    await q.enqueue(event(1));
    await q.restore();
    expect(q.size).toBe(1);
  });

  it("survives a corrupt blob and storage failures", async () => {
    const onStorageError = vi.fn();
    const broken: QueueStorage = {
      getItem: async () => "{not json",
      setItem: async () => {
        throw new Error("disk full");
      },
    };
    const q = new EventQueue({ client: client(mockFetch(ok(1)).fetch), storage: broken, onStorageError });
    await q.setMode("cohort");
    expect(await q.restore()).toBe(0);
    await q.enqueue(event(1));
    expect(q.size).toBe(1);
    expect(onStorageError).toHaveBeenCalledTimes(2);
  });
});

describe("EventQueue flush", () => {
  it("sends in batches", async () => {
    const m = mockFetch(ok(2), ok(2), ok(1));
    const q = new EventQueue({ client: client(m.fetch), batchSize: 2 });
    await q.setMode("cohort");
    for (let i = 1; i <= 5; i++) await q.enqueue(event(i));
    expect(await q.flush()).toEqual({ sent: 5, dropped: 0, remaining: 0 });
    expect(m.calls).toHaveLength(3);
  });

  it("keeps events on a transient failure and sends them on the next flush", async () => {
    const m = mockFetch({ networkError: "offline" }, { networkError: "offline" }, ok(2));
    const q = new EventQueue({ client: client(m.fetch) });
    await q.setMode("cohort");
    await q.enqueue(event(1));
    await q.enqueue(event(2));
    const first = await q.flush();
    expect(first).toMatchObject({ sent: 0, remaining: 2 });
    expect(first.error?.code).toBe(ErrorCode.Network);
    expect(await q.flush()).toEqual({ sent: 2, dropped: 0, remaining: 0 });
  });

  it("drops a batch the API rejects as invalid and reports it", async () => {
    const onDrop = vi.fn();
    const m = mockFetch({ status: 422, body: { code: "pii_rejected", message: "x", request_id: "r" } }, ok(1));
    const q = new EventQueue({ client: client(m.fetch), batchSize: 1, onDrop });
    await q.setMode("cohort");
    await q.enqueue(event(1));
    await q.enqueue(event(2));
    expect(await q.flush()).toEqual({ sent: 1, dropped: 1, remaining: 0 });
    expect(onDrop).toHaveBeenCalledWith("invalid", 1, expect.objectContaining({ code: "pii_rejected" }));
  });

  it("counts per-event rejections from a 202", async () => {
    const onDrop = vi.fn();
    const m = mockFetch({ status: 202, body: { accepted: 1, duplicates: 0, rejected: [{ event_id: "ev_2", reason: "unknown type" }] } });
    const q = new EventQueue({ client: client(m.fetch), onDrop });
    await q.setMode("cohort");
    await q.enqueue(event(1));
    await q.enqueue(event(2));
    expect(await q.flush()).toEqual({ sent: 1, dropped: 1, remaining: 0 });
    expect(onDrop).toHaveBeenCalledWith("rejected", 1);
  });

  it("keeps events and rethrows on an auth failure", async () => {
    const m = mockFetch({ status: 401, body: { code: "unauthorized", message: "x", request_id: "r" } });
    const q = new EventQueue({ client: client(m.fetch) });
    await q.setMode("cohort");
    await q.enqueue(event(1));
    await expect(q.flush()).rejects.toMatchObject({ code: "unauthorized" });
    expect(q.size).toBe(1);
  });

  it("shares one flush between concurrent callers", async () => {
    const m = mockFetch(ok(1));
    const q = new EventQueue({ client: client(m.fetch) });
    await q.setMode("cohort");
    await q.enqueue(event(1));
    const [a, b] = await Promise.all([q.flush(), q.flush()]);
    expect(a).toBe(b);
    expect(m.calls).toHaveLength(1);
  });

  it("clear() empties memory and storage", async () => {
    const mem = memoryStorage();
    const q = new EventQueue({ client: client(mockFetch(ok(1)).fetch), storage: mem.storage });
    await q.setMode("cohort");
    await q.enqueue(event(1));
    await q.clear();
    expect(q.size).toBe(0);
    expect(mem.data.get("patterna.events.v1")).toBe("[]");
  });
});
