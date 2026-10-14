import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { webcrypto } from "node:crypto";
import { createAuthSession } from "./auth-session.js";

const source = readFileSync(new URL("./app.js", import.meta.url), "utf8");
const between = (start, end) => source.slice(source.indexOf(start), source.indexOf(end));
const oldPair = { token: "access", refresh_token: "refresh", expire_at: 900, refresh_expire_at: 9000 };
const response = (status) => ({ status, text: async () => JSON.stringify({ code: status === 200 ? 0 : 1 }) });

test("page initialization immediately resumes a persisted refresh", () => {
  let resumed = 0;
  const state = {};
  const context = vm.createContext({
    state, AUTH_STORAGE_KEY: "chatAuthSession",
    els: { baseUrl: {} }, localStorage: { getItem: () => null },
    authSession: { read: () => ({ id: "session", ...oldPair, token: oldPair.token, refreshToken: oldPair.refresh_token, pending: { requestID: "pending" } }) },
    window: { addEventListener() {} }, decodeTokenUserID: () => 42, avatarStorageKey: () => "avatar",
    bindEvents() {}, showChat() {}, showAuth() {}, scheduleTokenRefresh() {},
    refreshAuthToken() { resumed++; }, refreshAll() {}, connectWs() {},
  });
  vm.runInContext(between("function init()", "function bindEvents()"), context);
  context.init();
  assert.equal(resumed, 1);
});

for (const caller of ["apiPost", "apiMultipartUploadStatus", "apiUploadForm"]) {
  test(caller + " keeps the session when an API 401 is followed by refresh 503", async () => {
    const values = new Map();
    const storage = { getItem: (key) => values.get(key) ?? null,
      setItem: (key, value) => values.set(key, value), removeItem: (key) => values.delete(key) };
    const state = { token: "", refreshTimer: 0 };
    let logoutCalls = 0, refreshCalls = 0;
    const manager = createAuthSession({
      storage, getBaseUrl: () => "http://chat", crypto: webcrypto, locks: null,
      now: () => 1000, maxRetries: 0,
      onChange(record) { state.token = record?.token || ""; state.refreshToken = record?.refreshToken || ""; state.tokenExpireAt = record?.tokenExpireAt || 0; },
      fetcher: async () => { refreshCalls++; return response(503); },
    });
    await manager.login(oldPair, "alice");
    class XHR {
      status = 401;
      responseText = JSON.stringify({ code: 1 });
      listeners = {};
      upload = { addEventListener() {} };
      open() {}
      setRequestHeader() {}
      addEventListener(name, fn) { this.listeners[name] = fn; }
      send() { queueMicrotask(() => this.listeners.load()); }
    }
    const context = vm.createContext({
      state, authSession: manager, Date, XMLHttpRequest: XHR,
      window: { setTimeout: () => 1, clearTimeout() {} },
      apiBase: () => "http://chat", log() {}, safeJson: JSON.parse,
      fetch: async () => response(401),
      logout() { logoutCalls++; },
    });
    vm.runInContext(between("function clearTokenRefreshTimer()", "// ====== 好友"), context);
    vm.runInContext(between("async function apiPost(", "async function apiUploadFile("), context);
    vm.runInContext(between("async function apiMultipartUploadStatus(", "async function apiUploadMultipartChunk("), context);
    vm.runInContext(between("function apiUploadForm(", "function uploadedBytesFromChunks("), context);
    if (caller === "apiPost") await context.apiPost("/test", {});
    if (caller === "apiMultipartUploadStatus") await context.apiMultipartUploadStatus("upload");
    if (caller === "apiUploadForm") await context.apiUploadForm("/upload", {});
    assert.equal(refreshCalls, 1);
    assert.equal(logoutCalls, 0);
    assert.equal(state.token, oldPair.token);
    assert.ok(manager.read().pending);
    assert.equal(state.refreshTimer, 1);
  });
}
