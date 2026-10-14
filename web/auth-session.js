export const AUTH_STORAGE_KEY = "chatAuthSession";
const LEGACY_KEYS = ["chatToken", "chatRefreshToken", "chatTokenExpireAt", "chatRefreshExpireAt", "chatEmail"];

export function createAuthSession({
  storage, getBaseUrl, onChange = () => {}, onError = () => {},
  fetcher = globalThis.fetch, crypto = globalThis.crypto, locks = globalThis.navigator?.locks,
  sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  setTimer = setTimeout, clearTimer = clearTimeout,
  now = Date.now, timeoutMs = 8000, maxRetries = 3,
}) {
  let running = null;
  let generation = 0;
  const withStateLock = (fn) => locks?.request
    ? locks.request("go-chat:auth-state", fn)
    : Promise.resolve(fn());
  const randomID = () => Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, "0")).join("");
  function read() {
    const raw = storage.getItem(AUTH_STORAGE_KEY);
    if (raw) {
      try {
        const data = JSON.parse(raw);
        return data?.id && data?.token && data?.refreshToken ? data : null;
      } catch { return null; }
    }
    const token = storage.getItem("chatToken");
    const refreshToken = storage.getItem("chatRefreshToken");
    if (!token || !refreshToken) return null;
    const record = { id: randomID(), token, refreshToken,
      tokenExpireAt: Number(storage.getItem("chatTokenExpireAt") || 0),
      refreshExpireAt: Number(storage.getItem("chatRefreshExpireAt") || 0),
      email: storage.getItem("chatEmail") || "", baseUrl: getBaseUrl(), pending: null };
    save(record);
    return record;
  }
  function save(record) {
    // One storage operation publishes credentials and pending-operation state.
    storage.setItem(AUTH_STORAGE_KEY, JSON.stringify(record));
    LEGACY_KEYS.forEach((key) => storage.removeItem(key));
  }
  function credentials(payload) {
    const token = String(payload?.token || "");
    const refreshToken = String(payload?.refresh_token || payload?.refreshToken || "");
    const tokenExpireAt = Number(payload?.expire_at || payload?.expireAt || 0);
    const refreshExpireAt = Number(payload?.refresh_expire_at || payload?.refreshExpireAt || 0);
    if (!token || !refreshToken || !tokenExpireAt || !refreshExpireAt) throw new Error("Invalid token response");
    return { token, refreshToken, tokenExpireAt, refreshExpireAt };
  }
  function login(payload, email) {
    generation++;
    const record = { ...credentials(payload), id: randomID(), email, baseUrl: getBaseUrl(), pending: null };
    return withStateLock(() => {
      save(record);
      onChange(record);
    });
  }
  async function logout({ revoke = true, expected = null } = {}) {
    const selected = read();
    if (expected && (currentOperation(expected) !== "current")) return;
    generation++;
    if (!expected) onChange(null); // Close local UI without waiting for another tab.
    const record = await withStateLock(() => {
      const current = read();
      if (expected && (!current || current.id !== expected.id || current.refreshToken !== expected.refreshToken)) return null;
      if (selected && current && current.id !== selected.id) return null;
      storage.removeItem(AUTH_STORAGE_KEY);
      LEGACY_KEYS.forEach((key) => storage.removeItem(key));
      onChange(null);
      return current || selected;
    });
    if (revoke && record) {
      try {
        Promise.resolve(fetcher(record.baseUrl + "/v1/user/logout", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ refreshToken: record.refreshToken }), keepalive: true,
        })).catch(() => {});
      } catch { /* Local logout is immediate even when the network is down. */ }
    }
  }
  function currentOperation(snapshot) {
    if (snapshot.generation !== generation) return "gone";
    const current = read();
    if (!current || current.id !== snapshot.id || current.baseUrl !== snapshot.baseUrl) return "gone";
    if (current.refreshToken !== snapshot.refreshToken) { onChange(current); return "updated"; }
    return "current";
  }
  async function execute(snapshot) {
    const progress = currentOperation(snapshot);
    if (progress !== "current") return progress === "updated";
    if (snapshot.baseUrl !== getBaseUrl()) return false;
    const prepared = await withStateLock(() => {
      const progress = currentOperation(snapshot);
      if (progress !== "current") return progress;
      let record = read();
      if (!record.pending || !/^[0-9a-f]{32}$/.test(record.pending.requestID)) {
        record = { ...record, pending: { requestID: randomID(), startedAt: now() } };
        save(record); // Persist before any request can reach the server.
      }
      return record;
    });
    if (typeof prepared === "string") return prepared === "updated";
    const record = prepared;
    const requestID = record.pending.requestID;
    for (let attempt = 0; attempt <= maxRetries; attempt++) {
      const progress = currentOperation(snapshot);
      if (progress !== "current") return progress === "updated";
      if (record.refreshExpireAt && now() >= record.refreshExpireAt * 1000) {
        await logout({ revoke: false, expected: snapshot }); return false;
      }
      const controller = new AbortController();
      const timer = setTimer(() => controller.abort(), timeoutMs);
      let retry = true;
      try {
        const response = await fetcher(snapshot.baseUrl + "/v1/user/refresh", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ refreshToken: snapshot.refreshToken, requestID }),
          signal: controller.signal,
        });
        const text = await response.text();
        const progress = currentOperation(snapshot);
        if (progress !== "current") return progress === "updated";
        if (response.status === 401) { await logout({ revoke: false, expected: snapshot }); return false; }
        if (response.status >= 400 && response.status < 500 && response.status !== 429) {
          onError(new Error("Refresh request rejected (" + response.status + ")"));
          retry = false;
        } else if (response.status >= 200 && response.status < 300) {
          const data = JSON.parse(text);
          if (data?.code !== 0) throw new Error("Invalid refresh response");
          const pair = credentials(data.data);
          return await withStateLock(() => {
            const progress = currentOperation(snapshot);
            if (progress !== "current") return progress === "updated";
            const updated = { ...read(), ...pair, pending: null };
            save(updated);
            onChange(updated);
            return true;
          });
        }
      } catch (error) {
        const progress = currentOperation(snapshot);
        if (progress !== "current") return progress === "updated";
        if (attempt === maxRetries) onError(error);
      } finally {
        clearTimer(timer);
      }
      if (!retry || attempt === maxRetries) return false;
      await sleep(1000 * 2 ** attempt);
    }
    return false;
  }
  function refresh() {
    const current = read();
    if (!current) return Promise.resolve(false);
    const snapshot = { ...current, generation };
    if (running?.id === snapshot.id && running.generation === generation) return running.promise;
    const run = () => execute(snapshot);
    const job = { id: snapshot.id, generation };
    job.promise = Promise.resolve().then(() =>
      locks?.request ? locks.request("go-chat:refresh:" + snapshot.id, run) : run()
    ).catch((error) => { onError(error); return false; }).finally(() => {
      if (running === job) running = null;
    });
    running = job;
    return job.promise;
  }
  return { read, login, logout, refresh, sync() { onChange(read()); } };
}

export function redactAuthSecrets(value) {
  if (Array.isArray(value)) return value.map(redactAuthSecrets);
  if (!value || typeof value !== "object") return value;
  return Object.fromEntries(Object.entries(value).map(([key, item]) =>
    [key, /password|token|authorization|encryptedResult/i.test(key) ? "[redacted]" : redactAuthSecrets(item)]));
}
