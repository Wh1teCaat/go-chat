import test from "node:test";
import assert from "node:assert/strict";
import { webcrypto } from "node:crypto";
import { createAuthSession, AUTH_STORAGE_KEY, redactAuthSecrets } from "./auth-session.js";

const oldPair = { token: "access-old", refresh_token: "refresh-old", expire_at: 900, refresh_expire_at: 9000 };
const newPair = { token: "access-new", refresh_token: "refresh-new", expire_at: 1800, refresh_expire_at: 18000 };
const response = (status, data) => ({ status, text: async () => JSON.stringify(data) });
const success = () => response(200, { code: 0, data: newPair });
function makeStorage() {
  const values = new Map();
  return { getItem: (key) => values.get(key) ?? null, setItem: (key, value) => values.set(key, value), removeItem: (key) => values.delete(key) };
}
function setup(fetcher, extra = {}) {
  const storage = extra.storage || makeStorage();
  const changes = [], errors = [], delays = [], timers = new Map();
  let id = 0;
  const options = { storage, fetcher, crypto: webcrypto, getBaseUrl: () => "http://chat",
    now: () => 1000, locks: undefined,
    onChange: (record) => changes.push(record), onError: (error) => errors.push(error),
    sleep: async (ms) => { delays.push(ms); },
    setTimer: (fn) => { timers.set(++id, fn); return id; }, clearTimer: (key) => timers.delete(key),
    ...extra };
  const manager = createAuthSession(options);
  const ready = !manager.read() ? manager.login(oldPair, "alice") : Promise.resolve();
  return { manager, storage, changes, errors, delays, timers, options, ready };
}

test("lost response retries one persisted operation and stores the returned credentials", async () => {
  const sent = [];
  let h;
  h = setup(async (_url, options) => {
    const body = JSON.parse(options.body);
    const pending = JSON.parse(h.storage.getItem(AUTH_STORAGE_KEY)).pending;
    assert.equal(body.requestID, pending.requestID);
    sent.push(body);
    if (sent.length === 1) throw new Error("response lost after rotation");
    return success();
  });
  const a = h.manager.refresh(), b = h.manager.refresh();
  assert.equal(a, b);
  assert.equal(await a, true);
  assert.deepEqual(sent[0], sent[1]);
  assert.equal(h.manager.read().refreshToken, newPair.refresh_token);
  assert.equal(h.manager.read().pending, null);
  assert.equal(h.timers.size, 0);
});

test("reload and another transient failure reuse the same request ID", async () => {
  let first;
  const h = setup(async (_url, options) => { first = JSON.parse(options.body); throw new Error("offline"); }, { maxRetries: 0 });
  assert.equal(await h.manager.refresh(), false);
  assert.equal(h.manager.read().refreshToken, oldPair.refresh_token);
  const reloaded = setup(async (_url, options) => {
    assert.deepEqual(JSON.parse(options.body), first);
    return success();
  }, { storage: h.storage });
  assert.equal(await reloaded.manager.refresh(), true);
});

for (const status of [500, 503, 429]) {
  test(status + " preserves login and uses bounded backoff", async () => {
    const bodies = [];
    const h = setup(async (_url, options) => { bodies.push(options.body); return response(status, {}); });
    assert.equal(await h.manager.refresh(), false);
    assert.equal(bodies.length, 4);
    assert.equal(new Set(bodies).size, 1);
    assert.deepEqual(h.delays, [1000, 2000, 4000]);
    assert.equal(h.manager.read().refreshToken, oldPair.refresh_token);
  });
}

test("401 clears login without retrying or submitting a second refresh", async () => {
  let calls = 0;
  const h = setup(async () => { calls++; return response(401, { code: 1 }); });
  assert.equal(await h.manager.refresh(), false);
  assert.equal(calls, 1);
  assert.equal(h.manager.read(), null);
});

test("timeout aborts the request while preserving its ID for recovery", async () => {
  let h;
  const bodies = [];
  h = setup((_url, options) => {
    bodies.push(options.body);
    return new Promise((_resolve, reject) => {
      options.signal.addEventListener("abort", () => reject(new Error("timeout")), { once: true });
      queueMicrotask(() => [...h.timers.values()].forEach((fn) => fn()));
    });
  }, { maxRetries: 1 });
  assert.equal(await h.manager.refresh(), false);
  assert.equal(bodies[0], bodies[1]);
  assert.ok(h.manager.read().pending);
});

for (const action of ["logout", "new-login"]) {
  test("late response cannot undo " + action, async () => {
    let release, entered;
    const started = new Promise((resolve) => { entered = resolve; });
    const h = setup((url) => {
      if (url.endsWith("/logout")) return Promise.resolve(response(200, {}));
      entered();
      return new Promise((resolve) => { release = resolve; });
    });
    const refresh = h.manager.refresh();
    await started;
    if (action === "logout") h.manager.logout();
    else h.manager.login({ ...newPair, refresh_token: "other-account" }, "bob");
    release(success());
    assert.equal(await refresh, false);
    if (action === "logout") assert.equal(h.manager.read(), null);
    else assert.equal(h.manager.read().refreshToken, "other-account");
  });
}

test("shared browser lock lets a second tab adopt the first tab's result", async () => {
  const tails = new Map();
  const locks = { request(name, fn) {
    const next = (tails.get(name) || Promise.resolve()).then(fn);
    tails.set(name, next.catch(() => {}));
    return next;
  } };
  const storage = makeStorage();
  let calls = 0;
  const fetcher = async () => { calls++; return success(); };
  const a = setup(fetcher, { storage, locks });
  await a.ready;
  const b = setup(fetcher, { storage, locks });
  assert.deepEqual(await Promise.all([a.manager.refresh(), b.manager.refresh()]), [true, true]);
  assert.equal(calls, 1);
  assert.equal(b.manager.read().refreshToken, newPair.refresh_token);
});

function sharedLocks() {
  const tails = new Map();
  return { request(name, fn) {
    const next = (tails.get(name) || Promise.resolve()).then(fn);
    tails.set(name, next.catch(() => {}));
    return next;
  } };
}

for (const action of ["logout", "new-login"]) {
  test("another tab's " + action + " invalidates a pending refresh response", async () => {
    let release, entered;
    const started = new Promise((resolve) => { entered = resolve; });
    const storage = makeStorage(), locks = sharedLocks();
    const a = setup(() => {
      entered();
      return new Promise((resolve) => { release = resolve; });
    }, { storage, locks });
    await a.ready;
    const b = setup(async () => response(200, {}), { storage, locks });
    const refreshing = a.manager.refresh();
    await started;
    if (action === "logout") await b.manager.logout();
    else await b.manager.login({ ...newPair, refresh_token: "bob-refresh" }, "bob");
    release(success());
    assert.equal(await refreshing, false);
    if (action === "logout") assert.equal(a.manager.read(), null);
    else assert.equal(a.manager.read().refreshToken, "bob-refresh");
  });
}

test("legacy credentials migrate once and cannot reappear after logout", () => {
  const storage = makeStorage();
  storage.setItem("chatToken", oldPair.token);
  storage.setItem("chatRefreshToken", oldPair.refresh_token);
  storage.setItem("chatTokenExpireAt", "900");
  storage.setItem("chatRefreshExpireAt", "9000");
  const h = setup(async () => response(200, {}), { storage });
  assert.equal(storage.getItem("chatToken"), null);
  h.manager.logout();
  assert.equal(h.manager.read(), null);
});

test("logs redact bearer credentials and passwords inside request/response objects", () => {
  const clean = redactAuthSecrets({ data: { token: "secret", refresh_token: "secret2", expire_at: 10 }, body: { password: "pw" } });
  assert.equal(clean.data.token, "[redacted]");
  assert.equal(clean.data.refresh_token, "[redacted]");
  assert.equal(clean.body.password, "[redacted]");
  assert.equal(clean.data.expire_at, 10);
});
