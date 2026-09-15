import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { applyMessageAck, applyMessageFailure, isOwnMessage } from "./app-helpers.js";

const source = readFileSync(new URL("./app.js", import.meta.url), "utf8");
function setup() {
  const timers = new Map();
  const sent = [];
  let id = 0;
  const state = { currentUserID: 1, messages: [{ id: "abc", clientMsgID: "abc", status: "sending" }], pendingMessageTimers: new Map(), ws: { readyState: 1, send: (data) => sent.push(JSON.parse(data)) } };
  const context = vm.createContext({ state, WebSocket: { OPEN: 1 }, MESSAGE_ACK_TIMEOUT_MS: 10000, MESSAGE_MAX_RETRIES: 3,
    window: { setTimeout(fn, delay) { assert.equal(delay, 10000); timers.set(++id, fn); return id; }, clearTimeout(key) { timers.delete(key); } },
    applyMessageAck, applyMessageFailure, isOwnMessage, sortMessagesAscending: (x) => x,
    renderMessages() {}, refreshSessions() {}, log() {}, messageMatchesTarget: () => false,
  });
  vm.runInContext(source.slice(source.indexOf("function handleWsPayload("), source.indexOf("async function markVisibleMessagesRead(")), context);
  const payload = { clientMsgID: "abc", content: "hello", targetType: "private", targetID: 2 };
  context.trackPendingMessage(payload);
  return { context, state, sent, timers, payload, tick() { const [key, fn] = timers.entries().next().value; timers.delete(key); fn(); } };
}

test("ACK timeout retries original payload three times, then fails", () => {
  const h = setup();
  for (let i = 0; i < 3; i++) { h.tick(); assert.equal(h.state.messages[0].status, "sending"); }
  assert.deepEqual(h.sent, [h.payload, h.payload, h.payload]);
  h.tick();
  assert.equal(h.sent.length, 3);
  assert.equal(h.state.messages[0].status, "failed");
  assert.equal(h.timers.size, 0);
  assert.equal(h.state.pendingMessageTimers.size, 0);
});

test("ACK after retry cancels timeout and confirms original message", () => {
  const h = setup(); h.tick();
  h.context.handleWsPayload({ type: "message_ack", data: { clientMsgID: "abc", messageID: 100 } });
  assert.equal(h.timers.size, 0);
  assert.equal(h.state.messages[0].status, "sent");
  assert.equal(h.state.messages[0].id, 100);
});

test("own message push cancels retry even outside current conversation", () => {
  const h = setup();
  h.context.handleWsPayload({ type: "message", data: { senderID: 1, clientMsgID: "abc", id: 100 } });
  assert.equal(h.timers.size, 0);
});

test("explicit server error cancels retries", () => {
  const h = setup();
  h.context.handleWsPayload({ type: "error", data: { clientMsgID: "abc" } });
  assert.equal(h.timers.size, 0);
  assert.equal(h.state.messages[0].status, "failed");
});

for (const failure of ["closed", "throw", "disconnect"]) {
  test(`${failure} stops pending retries`, () => {
    const h = setup();
    if (failure === "closed") h.state.ws = null;
    if (failure === "throw") h.state.ws.send = () => { throw new Error("closed"); };
    if (failure === "disconnect") h.context.failPendingMessages(); else h.tick();
    assert.equal(h.timers.size, 0);
    assert.equal(h.sent.length, 0);
    assert.equal(h.state.messages[0].status, "failed");
  });
}
