import {
  applyMessageAck,
  applyMessageFailure,
  applyMessageRead,
  buildAddFriendPayload,
  buildFileMessageContent,
  buildGroupReviewPayload,
  createLocalMessage,
  buildMemberRolePayload,
  buildMessagePayload,
  buildUpdateAvatarPayload,
  fileIconLabel,
  formatFileSize,
  buildWsUrl,
  buildWsProtocols,
  decodeTokenUserID,
  isOwnMessage,
  latestServerMessageID,
  mergeIncomingMessage,
  messageMatchesTarget,
  messagePreview,
  normalizeBaseUrl,
  normalizeChatTarget,
  parseFileMessage,
  resolveAssetUrl,
  sortMessagesAscending,
} from "./app-helpers.js";

const state = {
  token: "",
  refreshToken: "",
  tokenExpireAt: 0,
  refreshExpireAt: 0,
  email: "",
  avatar: "",
  currentUserID: 0,
  ws: null,
  currentTarget: null,
  messages: [],
  sessions: [],
  friends: [],
  groups: [],
  pending: [],
  groupJoinRequests: [],
  groupMembers: [],
  pendingMessageTimers: new Map(),
  readWatermarks: new Map(),
  activeTab: "messages",
  searchQuery: "",
  activeUpload: null,
  refreshTimer: 0,
  refreshPromise: null,
  reconnectTimer: 0,
  reconnectAttempts: 0,
};

const MULTIPART_UPLOAD_THRESHOLD = 2 * 1024 * 1024;
const MULTIPART_CHUNK_SIZE = 2 * 1024 * 1024;
const MESSAGE_ACK_TIMEOUT_MS = 10000;

const els = {
  authView: document.querySelector("#authView"),
  chatView: document.querySelector("#chatView"),
  authTitle: document.querySelector("#authTitle"),
  baseUrl: document.querySelector("#baseUrl"),
  loginForm: document.querySelector("#loginForm"),
  registerForm: document.querySelector("#registerForm"),
  showRegisterLink: document.querySelector("#showRegisterLink"),
  showLoginLink: document.querySelector("#showLoginLink"),
  registerResult: document.querySelector("#registerResult"),
  accountEmail: document.querySelector("#accountEmail"),
  wsState: document.querySelector("#wsState"),
  userAvatar: document.querySelector("#userAvatar"),
  avatarFileInput: document.querySelector("#avatarFileInput"),
  logoutBtn: document.querySelector("#logoutBtn"),
  searchInput: document.querySelector("#searchInput"),
  addFriendForm: document.querySelector("#addFriendForm"),
  createGroupForm: document.querySelector("#createGroupForm"),
  joinGroupForm: document.querySelector("#joinGroupForm"),
  sessionList: document.querySelector("#sessionList"),
  pendingList: document.querySelector("#pendingList"),
  friendList: document.querySelector("#friendList"),
  groupList: document.querySelector("#groupList"),
  refreshGroupJoinRequestsBtn: document.querySelector("#refreshGroupJoinRequestsBtn"),
  groupJoinRequestList: document.querySelector("#groupJoinRequestList"),
  groupMembersBtn: document.querySelector("#groupMembersBtn"),
  closeMemberPanelBtn: document.querySelector("#closeMemberPanelBtn"),
  memberPanel: document.querySelector("#memberPanel"),
  memberList: document.querySelector("#memberList"),
  chatTitle: document.querySelector("#chatTitle"),
  chatSubtitle: document.querySelector("#chatSubtitle"),
  messageList: document.querySelector("#messageList"),
  messageForm: document.querySelector("#messageForm"),
  messageInput: document.querySelector("#messageInput"),
  emojiBtn: document.querySelector("#emojiBtn"),
  imageBtn: document.querySelector("#imageBtn"),
  fileBtn: document.querySelector("#fileBtn"),
  imageFileInput: document.querySelector("#imageFileInput"),
  chatFileInput: document.querySelector("#chatFileInput"),
  uploadProgress: document.querySelector("#uploadProgress"),
  uploadFileName: document.querySelector("#uploadFileName"),
  uploadPercent: document.querySelector("#uploadPercent"),
  uploadBar: document.querySelector("#uploadBar"),
  cancelUploadBtn: document.querySelector("#cancelUploadBtn"),
  clearLogBtn: document.querySelector("#clearLogBtn"),
  logOutput: document.querySelector("#logOutput"),
};

init();

function init() {
  els.baseUrl.value = localStorage.getItem("chatBaseUrl") || "http://localhost:8080";
  state.token = localStorage.getItem("chatToken") || "";
  state.refreshToken = localStorage.getItem("chatRefreshToken") || "";
  state.tokenExpireAt = Number(localStorage.getItem("chatTokenExpireAt") || 0);
  state.refreshExpireAt = Number(localStorage.getItem("chatRefreshExpireAt") || 0);
  state.email = localStorage.getItem("chatEmail") || "";
  state.currentUserID = decodeTokenUserID(state.token);
  state.avatar = localStorage.getItem(avatarStorageKey()) || "";
  bindEvents();

  if (state.token) {
    showChat();
    scheduleTokenRefresh();
    refreshAll();
    connectWs();
    return;
  }
  showAuth("login");
}

function bindEvents() {
  els.showRegisterLink.addEventListener("click", (event) => {
    event.preventDefault();
    showAuth("register");
  });
  els.showLoginLink.addEventListener("click", (event) => {
    event.preventDefault();
    showAuth("login");
  });
  els.loginForm.addEventListener("submit", login);
  els.registerForm.addEventListener("submit", register);
  els.logoutBtn.addEventListener("click", logout);
  els.addFriendForm.addEventListener("submit", addFriend);
  els.createGroupForm.addEventListener("submit", createGroup);
  els.joinGroupForm.addEventListener("submit", joinGroup);
  els.refreshGroupJoinRequestsBtn.addEventListener("click", refreshGroupJoinRequests);
  els.groupMembersBtn.addEventListener("click", openMemberPanel);
  els.closeMemberPanelBtn.addEventListener("click", closeMemberPanel);
  els.userAvatar.addEventListener("click", () => els.avatarFileInput.click());
  els.avatarFileInput.addEventListener("change", updateAvatarFromInput);
  els.messageForm.addEventListener("submit", sendMessage);
  els.clearLogBtn.addEventListener("click", () => {
    els.logOutput.textContent = "";
  });
  els.messageInput.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      els.messageForm.requestSubmit();
    }
  });

  // Tab 切换
  document.querySelectorAll(".tab-btn").forEach((btn) => {
    btn.addEventListener("click", () => switchTab(btn.dataset.tab));
  });

  // 搜索过滤
  els.searchInput.addEventListener("input", () => {
    state.searchQuery = els.searchInput.value.trim().toLowerCase();
    filterCurrentList();
  });

  // 工具栏按钮（预留接口）
  els.emojiBtn.addEventListener("click", () => onEmoji());
  els.imageBtn.addEventListener("click", () => onImage());
  els.fileBtn.addEventListener("click", () => onFile());
  els.cancelUploadBtn.addEventListener("click", cancelActiveUpload);
  els.imageFileInput.addEventListener("change", (event) => uploadComposerFile(event, "chat_image"));
  els.chatFileInput.addEventListener("change", (event) => uploadComposerFile(event, "chat_file"));
}

// ====== Auth ======

async function register(event) {
  event.preventDefault();
  els.registerResult.textContent = "";
  const data = await apiPost("/v1/user/register", formToJson(els.registerForm), { auth: false });
  if (data && data.code === 0) {
    els.registerResult.textContent = "注册成功，请返回登录。";
    showAuth("login");
  }
}

async function login(event) {
  event.preventDefault();
  const body = formToJson(els.loginForm);
  const data = await apiPost("/v1/user/login", body, { auth: false });
  if (!data || data.code !== 0 || !data.data || !data.data.token) {
    return;
  }

  state.email = body.email;
  applyAuthTokens(data.data);
  state.avatar = localStorage.getItem(avatarStorageKey()) || "";
  localStorage.setItem("chatEmail", state.email);
  localStorage.setItem("chatBaseUrl", apiBase());
  showChat();
  await refreshAll();
  connectWs();
}

function logout() {
  revokeRefreshTokenOnServer();
  disconnectWs();
  clearTokenRefreshTimer();
  state.token = "";
  state.refreshToken = "";
  state.tokenExpireAt = 0;
  state.refreshExpireAt = 0;
  state.refreshPromise = null;
  state.email = "";
  state.avatar = "";
  state.currentUserID = 0;
  state.currentTarget = null;
  state.messages = [];
  state.pendingMessageTimers.forEach((timer) => window.clearTimeout(timer));
  state.pendingMessageTimers.clear();
  state.readWatermarks.clear();
  localStorage.removeItem("chatToken");
  localStorage.removeItem("chatRefreshToken");
  localStorage.removeItem("chatTokenExpireAt");
  localStorage.removeItem("chatRefreshExpireAt");
  localStorage.removeItem("chatEmail");
  showAuth("login");
}

// 登出时吊销服务端的 refresh token，失败也不阻塞本地登出（token 会随 TTL 过期）。
function revokeRefreshTokenOnServer() {
  if (!state.refreshToken) {
    return;
  }
  fetch(`${apiBase()}/v1/user/logout`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ refreshToken: state.refreshToken }),
    keepalive: true,
  }).catch(() => {});
}

function applyAuthTokens(payload) {
  state.token = String(payload?.token || "");
  state.refreshToken = String(payload?.refresh_token || payload?.refreshToken || "");
  state.tokenExpireAt = Number(payload?.expire_at || payload?.expireAt || 0);
  state.refreshExpireAt = Number(payload?.refresh_expire_at || payload?.refreshExpireAt || 0);
  state.currentUserID = decodeTokenUserID(state.token);

  localStorage.setItem("chatToken", state.token);
  localStorage.setItem("chatRefreshToken", state.refreshToken);
  localStorage.setItem("chatTokenExpireAt", String(state.tokenExpireAt || 0));
  localStorage.setItem("chatRefreshExpireAt", String(state.refreshExpireAt || 0));
  scheduleTokenRefresh();
}

function clearTokenRefreshTimer() {
  if (state.refreshTimer) {
    window.clearTimeout(state.refreshTimer);
    state.refreshTimer = 0;
  }
}

function scheduleTokenRefresh() {
  clearTokenRefreshTimer();
  if (!state.token || !state.refreshToken || !state.tokenExpireAt) {
    return;
  }
  const refreshAt = Math.max(Date.now() + 5000, state.tokenExpireAt * 1000 - 60 * 1000);
  state.refreshTimer = window.setTimeout(() => {
    refreshAuthToken();
  }, Math.max(0, refreshAt - Date.now()));
}

async function refreshAuthToken() {
  if (!state.refreshToken) {
    return false;
  }
  if (state.refreshExpireAt && Date.now() >= state.refreshExpireAt * 1000) {
    logout();
    return false;
  }
  if (state.refreshPromise) {
    return state.refreshPromise;
  }

  state.refreshPromise = (async () => {
    try {
      const response = await fetch(`${apiBase()}/v1/user/refresh`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ refreshToken: state.refreshToken }),
      });
      const data = safeJson(await response.text());
      log(`REFRESH RESPONSE ${response.status}`, data);
      if (!data || data.code !== 0 || !data.data || !data.data.token) {
        logout();
        return false;
      }
      applyAuthTokens(data.data);
      return true;
    } catch (error) {
      log("REFRESH ERROR", error.message);
      return false;
    } finally {
      state.refreshPromise = null;
    }
  })();
  return state.refreshPromise;
}

// ====== 好友 / 群组 (不变) ======

async function addFriend(event) {
  event.preventDefault();
  const email = formToJson(els.addFriendForm).friendEmail;
  const body = buildAddFriendPayload(email);
  if (!body.friendEmail) {
    log("ADD FRIEND", "请输入好友邮箱");
    return;
  }
  await apiPost("/v1/friend/add", body);
  els.addFriendForm.reset();
}

async function createGroup(event) {
  event.preventDefault();
  const body = formToJson(els.createGroupForm);
  if (!body.name) {
    log("CREATE GROUP", "请输入群名称");
    return;
  }
  await apiPost("/v1/group/create", body);
  els.createGroupForm.reset();
  await refreshAll();
}

async function joinGroup(event) {
  event.preventDefault();
  const body = formToJson(els.joinGroupForm);
  if (!body.groupID) {
    log("JOIN GROUP", "请输入群 ID");
    return;
  }
  await apiPost("/v1/group/join", body);
  els.joinGroupForm.reset();
}

async function refreshAll() {
  await Promise.allSettled([refreshSessions(), refreshPending(), refreshFriends(), refreshGroups(), refreshGroupJoinRequests()]);
}

async function refreshSessions() {
  const data = await apiPost("/v1/message/sessions", {});
  state.sessions = unwrapArray(data);
  renderSessions();
}

async function refreshPending() {
  const data = await apiPost("/v1/friend/pending", {});
  state.pending = unwrapArray(data);
  renderPending();
}

async function refreshFriends() {
  const data = await apiPost("/v1/friend/list", {});
  state.friends = unwrapArray(data);
  renderMiniTargets(els.friendList, state.friends, "friend", "暂无好友");
}

async function refreshGroups() {
  const [mine, joined] = await Promise.all([
    apiPost("/v1/group/mine", {}),
    apiPost("/v1/group/joined", {}),
  ]);
  state.groups = uniqueBy([...unwrapArray(mine), ...unwrapArray(joined)], "id");
  renderMiniTargets(els.groupList, state.groups, "group", "暂无群");
}

async function refreshGroupJoinRequests() {
  const data = await apiPost("/v1/group/join-requests/reviewable", {});
  state.groupJoinRequests = unwrapArray(data);
  renderGroupJoinRequests();
}

async function acceptFriend(requestID) {
  await apiPost("/v1/friend/accept", { requestID });
  await refreshAll();
}

async function rejectFriend(requestID) {
  await apiPost("/v1/friend/reject", { requestID });
  await refreshPending();
}

async function reviewGroupJoinRequest(requestID, status) {
  await apiPost("/v1/group/join-review", buildGroupReviewPayload(requestID, status));
  await refreshGroupJoinRequests();
  await refreshGroups();
}

// ====== 会话打开 (不变) ======

async function openTarget(target) {
  state.currentTarget = target;
  state.messages = [];
  els.chatTitle.textContent = target.title;
  els.chatSubtitle.textContent = `${target.type === "group" ? "群聊" : "私聊"} · ${target.type === "group" ? `群 #${target.id}` : ""}`;
  els.groupMembersBtn.classList.toggle("hidden", target.type !== "group");
  if (target.type !== "group") {
    closeMemberPanel();
  }
  renderSessions();
  renderMiniTargets(els.friendList, state.friends, "friend", "暂无好友");
  renderMiniTargets(els.groupList, state.groups, "group", "暂无群");
  renderMessages();
  await loadMessages();
}

async function loadMessages() {
  if (!state.currentTarget) {
    return;
  }
  const data = await apiPost("/v1/message/list", {
    targetType: state.currentTarget.type,
    targetID: state.currentTarget.id,
    limit: 30,
  });
  state.messages = sortMessagesAscending(unwrapArray(data));
  renderMessages();
  markVisibleMessagesRead();
}

// ====== WebSocket (基本不变, 状态映射) ======

function connectWs() {
  if (!state.token) {
    log("WS", "未登录，不能连接 WebSocket");
    return;
  }
  if (state.ws && state.ws.readyState === WebSocket.OPEN) {
    return;
  }

  disconnectWs();
  // 所有回调都校验 state.ws === ws：主动断开或重建连接后，旧 socket 迟到的事件直接忽略，
  // 避免旧连接的 close 误清新连接、误触发重连。
  const ws = new WebSocket(buildWsUrl(apiBase()), buildWsProtocols(state.token));
  state.ws = ws;
  updateWsStatus("connecting");

  ws.addEventListener("open", () => {
    if (state.ws !== ws) {
      return;
    }
    updateWsStatus("online");
    const isReconnect = state.reconnectAttempts > 0;
    state.reconnectAttempts = 0;
    log("WS OPEN", "连接成功");
    if (isReconnect) {
      resyncAfterReconnect();
    }
  });
  ws.addEventListener("message", (event) => {
    if (state.ws !== ws) {
      return;
    }
    const payload = safeJson(event.data);
    log("WS MESSAGE", payload);
    handleWsPayload(payload);
  });
  ws.addEventListener("error", () => {
    if (state.ws !== ws) {
      return;
    }
    updateWsStatus("offline");
    log("WS ERROR", "连接错误");
  });
  ws.addEventListener("close", () => {
    if (state.ws !== ws) {
      return;
    }
    updateWsStatus("offline");
    state.ws = null;
    failPendingMessages();
    scheduleWsReconnect();
  });
}

// 断线自动重连：指数退避（1s 起步，封顶 30s）。重连成功后由 open 回调补拉断线期间的消息。
function scheduleWsReconnect() {
  if (!state.token || state.reconnectTimer) {
    return;
  }
  const delay = Math.min(30000, 1000 * 2 ** state.reconnectAttempts);
  state.reconnectAttempts += 1;
  log("WS RECONNECT", `${delay}ms 后尝试第 ${state.reconnectAttempts} 次重连`);
  state.reconnectTimer = window.setTimeout(() => {
    state.reconnectTimer = 0;
    connectWs();
  }, delay);
}

// 重连成功后的增量同步：会话列表全量刷新（拿最新未读数），
// 当前会话按本地最大服务端消息 ID 增量补拉，避免整页重新加载。
async function resyncAfterReconnect() {
  refreshSessions();
  if (!state.currentTarget) {
    return;
  }
  const afterID = latestServerMessageID(state.messages);
  if (!afterID) {
    await loadMessages();
    return;
  }
  const data = await apiPost("/v1/message/list", {
    targetType: state.currentTarget.type,
    targetID: state.currentTarget.id,
    afterMessageID: afterID,
    limit: 100,
  });
  const missed = unwrapArray(data);
  if (!missed.length) {
    return;
  }
  let messages = state.messages;
  for (const msg of missed) {
    messages = mergeIncomingMessage(messages, msg).messages;
  }
  state.messages = sortMessagesAscending(messages);
  renderMessages();
  markVisibleMessagesRead();
}

function disconnectWs() {
  if (state.reconnectTimer) {
    window.clearTimeout(state.reconnectTimer);
    state.reconnectTimer = 0;
  }
  state.reconnectAttempts = 0;
  if (state.ws) {
    // 先把 state.ws 置空再关闭：回调里的 state.ws === ws 守卫会忽略这个 socket 的后续事件。
    const ws = state.ws;
    state.ws = null;
    try {
      ws.close();
    } catch {
      // 连接可能已经关闭。
    }
  }
  updateWsStatus("offline");
}

function sendMessage(event) {
  event.preventDefault();
  const content = els.messageInput.value.trim();
  if (!content) {
    return;
  }
  if (!sendMessageContent(content)) {
    return;
  }
  els.messageInput.value = "";
}

function sendMessageContent(content) {
  if (!state.currentTarget) {
    log("SEND", "请先选择会话");
    return false;
  }
  if (!state.ws || state.ws.readyState !== WebSocket.OPEN) {
    log("SEND", "WebSocket 未连接");
    return false;
  }
  const payload = buildMessagePayload(state.currentTarget, content);
  state.messages.push(createLocalMessage(payload, state.currentUserID));
  state.messages = sortMessagesAscending(state.messages);
  renderMessages();
  trackPendingMessage(payload.clientMsgID);
  try {
    state.ws.send(JSON.stringify(payload));
  } catch (error) {
    markMessageFailed(payload.clientMsgID);
    log("WS SEND ERROR", error.message);
    return false;
  }
  log("WS SEND", payload);
  return true;
}

function handleWsPayload(payload) {
  if (payload && payload.type === "group_join_request") {
    log("GROUP JOIN REQUEST", payload.data || {});
    refreshGroupJoinRequests();
    return;
  }
  if (payload && payload.type === "friend_request") {
    log("FRIEND REQUEST", payload.data || {});
    refreshPending();
    return;
  }
  if (payload && payload.type === "message_ack") {
    clearPendingMessageTimer(payload.data?.clientMsgID);
    state.messages = sortMessagesAscending(applyMessageAck(state.messages, payload.data || {}));
    renderMessages();
    refreshSessions();
    return;
  }
  if (payload && payload.type === "message_read") {
    if (messageReadMatchesCurrentTarget(payload.data || {})) {
      state.messages = applyMessageRead(state.messages, payload.data || {}, state.currentUserID);
      renderMessages();
    }
    refreshSessions();
    return;
  }
  if (payload && payload.type === "error") {
    const clientMsgID = payload.data?.clientMsgID;
    if (clientMsgID) {
      markMessageFailed(clientMsgID);
    }
    log("WS ERROR MESSAGE", payload.data || {});
    return;
  }
  if (!payload || payload.type !== "message" || !payload.data) {
    return;
  }
  const msg = payload.data;
  // 推送消息带接收端视角的 targetType/targetID；不属于当前打开的会话就只刷新会话列表，
  // 未读数和最后一条消息由会话接口给出，消息本体等切换会话时再拉。
  if (!messageMatchesTarget(msg, state.currentTarget)) {
    refreshSessions();
    return;
  }
  // 自己其他设备发的消息、或 ACK 与推送并发到达时按 id/clientMsgID 去重合并。
  state.messages = sortMessagesAscending(mergeIncomingMessage(state.messages, msg).messages);
  renderMessages();
  markVisibleMessagesRead();
  refreshSessions();
}

function trackPendingMessage(clientMsgID) {
  clearPendingMessageTimer(clientMsgID);
  const timer = window.setTimeout(() => {
    markMessageFailed(clientMsgID);
  }, MESSAGE_ACK_TIMEOUT_MS);
  state.pendingMessageTimers.set(clientMsgID, timer);
}

function clearPendingMessageTimer(clientMsgID) {
  if (!clientMsgID) {
    return;
  }
  const timer = state.pendingMessageTimers.get(clientMsgID);
  if (timer) {
    window.clearTimeout(timer);
    state.pendingMessageTimers.delete(clientMsgID);
  }
}

function markMessageFailed(clientMsgID) {
  clearPendingMessageTimer(clientMsgID);
  state.messages = applyMessageFailure(state.messages, clientMsgID);
  renderMessages();
}

function failPendingMessages() {
  const ids = [...state.pendingMessageTimers.keys()];
  ids.forEach((clientMsgID) => markMessageFailed(clientMsgID));
}

async function markVisibleMessagesRead() {
  if (!state.currentTarget) {
    return;
  }
  const latest = [...state.messages]
    .reverse()
    .find((msg) => !isOwnMessage(msg, state.currentUserID) && Number(msg.id) > 0);
  if (!latest) {
    return;
  }

  const key = targetKey(state.currentTarget);
  const latestID = Number(latest.id);
  if ((state.readWatermarks.get(key) || 0) >= latestID) {
    return;
  }

  const data = await apiPost("/v1/message/read", {
    targetType: state.currentTarget.type,
    targetID: state.currentTarget.id,
    messageID: latestID,
  });
  if (data && data.code === 0) {
    state.readWatermarks.set(key, latestID);
  }
}

function messageReadMatchesCurrentTarget(event) {
  return Boolean(
    state.currentTarget &&
    event &&
    event.targetType === state.currentTarget.type &&
    Number(event.targetID) === Number(state.currentTarget.id),
  );
}

function targetKey(target) {
  return `${target.type}:${target.id}`;
}

// ====== 群成员管理 (不变) ======

async function apiPost(path, body, options = {}) {
  const headers = { "Content-Type": "application/json" };
  if (options.auth !== false && state.token) {
    headers.Authorization = `Bearer ${state.token}`;
  }

  const url = `${apiBase()}${path}`;
  log("REQUEST", { url, body });

  try {
    const response = await fetch(url, {
      method: "POST",
      headers,
      body: JSON.stringify(body),
      signal: options.signal,
    });
    const data = safeJson(await response.text());
    log(`RESPONSE ${response.status}`, data);
    if (response.status === 401 && options.auth !== false && !options.skipRefresh) {
      const refreshed = await refreshAuthToken();
      if (refreshed) {
        return apiPost(path, body, { ...options, skipRefresh: true });
      }
      logout();
    }
    return data;
  } catch (error) {
    log("FETCH ERROR", error.message);
    return null;
  }
}

async function apiUploadFile(file, purpose, options = {}) {
  const sha256 = options.sha256 || await calculateFileSHA256(file, options.signal);
  if (!sha256) {
    return null;
  }
  const form = new FormData();
  form.append("file", file);
  form.append("sha256", sha256);
  if (purpose) {
    form.append("purpose", purpose);
  }

  const headers = {};
  if (state.token) {
    headers.Authorization = `Bearer ${state.token}`;
  }

  const url = `${apiBase()}/v1/file/upload`;
  log("UPLOAD REQUEST", { url, filename: file.name, purpose });

  const data = await apiUploadForm(url, form, {
    method: "POST",
    headers,
    signal: options.signal,
    onProgress: options.onProgress,
    logPrefix: "UPLOAD",
  });
  if (!data || data.code !== 0 || !data.data) {
    return null;
  }
  if (!uploadResponseMatchesSHA256(data.data, sha256)) {
    return null;
  }
  return data.data;
}

async function apiUploadFileSmart(file, purpose, options = {}) {
  log("UPLOAD", "正在计算文件校验值");
  const sha256 = await calculateFileSHA256(file, options.signal);
  if (!sha256) {
    return null;
  }
  if (file.size > MULTIPART_UPLOAD_THRESHOLD && purpose !== "avatar") {
    return apiMultipartUploadFile(file, purpose, { ...options, sha256 });
  }
  return apiUploadFile(file, purpose, { ...options, sha256 });
}

async function apiMultipartUploadFile(file, purpose, options = {}) {
  const sha256 = options.sha256 || await calculateFileSHA256(file, options.signal);
  if (!sha256) {
    return null;
  }
  const storageKey = multipartUploadStorageKey(file, purpose, sha256);
  let uploadID = localStorage.getItem(storageKey) || "";
  let session = uploadID ? await apiMultipartUploadStatus(uploadID, options) : null;
  if (!session || session.status !== "pending") {
    localStorage.removeItem(storageKey);
    const created = await apiPost("/v1/file/upload/init", {
      filename: file.name,
      size: file.size,
      contentType: file.type || "application/octet-stream",
      purpose,
      chunkSize: MULTIPART_CHUNK_SIZE,
      sha256,
    }, { signal: options.signal });
    if (!created || created.code !== 0 || !created.data) {
      return null;
    }
    session = created.data;
    uploadID = session.uploadID;
    localStorage.setItem(storageKey, uploadID);
  }

  const uploaded = new Set((session.uploadedChunks || []).map((index) => Number(index)));
  let uploadedBytes = uploadedBytesFromChunks(uploaded, session, file.size);
  options.onProgress?.(uploadedBytes, file.size);
  for (let index = 0; index < session.totalChunks; index++) {
    if (options.signal?.aborted) {
      return null;
    }
    if (uploaded.has(index)) {
      continue;
    }
    const start = index * session.chunkSize;
    const end = Math.min(start + session.chunkSize, file.size);
    const chunk = file.slice(start, end);
    const chunkBase = uploadedBytes;
    const ok = await apiUploadMultipartChunk(uploadID, index, chunk, {
      signal: options.signal,
      onProgress: (loaded) => {
        options.onProgress?.(chunkBase + loaded, file.size);
      },
    });
    if (!ok) {
      if (options.signal?.aborted) {
        return null;
      }
      log("UPLOAD", `分片 ${index + 1}/${session.totalChunks} 上传失败，稍后可重选文件续传`);
      return null;
    }
    uploadedBytes += chunk.size;
    options.onProgress?.(uploadedBytes, file.size);
    log("UPLOAD", `分片 ${index + 1}/${session.totalChunks} 已上传`);
  }

  const completed = await apiPost(`/v1/file/upload/complete/${encodeURIComponent(uploadID)}`, {}, { signal: options.signal });
  if (!completed || completed.code !== 0 || !completed.data) {
    return null;
  }
  if (!uploadResponseMatchesSHA256(completed.data, sha256)) {
    return null;
  }
  localStorage.removeItem(storageKey);
  options.onProgress?.(file.size, file.size);
  return completed.data;
}

async function apiMultipartUploadStatus(uploadID, options = {}) {
  const headers = {};
  if (state.token) {
    headers.Authorization = `Bearer ${state.token}`;
  }
  try {
    const response = await fetch(`${apiBase()}/v1/file/upload/status/${encodeURIComponent(uploadID)}`, {
      headers,
      signal: options.signal,
    });
    const data = safeJson(await response.text());
    log(`UPLOAD STATUS ${response.status}`, data);
    if (response.status === 401 && !options.skipRefresh) {
      const refreshed = await refreshAuthToken();
      if (refreshed) {
        return apiMultipartUploadStatus(uploadID, { ...options, skipRefresh: true });
      }
      logout();
    }
    if (!data || data.code !== 0 || !data.data) {
      return null;
    }
    return data.data;
  } catch (error) {
    log("UPLOAD STATUS ERROR", error.message);
    return null;
  }
}

async function apiUploadMultipartChunk(uploadID, index, chunk, options = {}) {
  const form = new FormData();
  form.append("chunk", chunk, "chunk.part");
  const headers = {};
  if (state.token) {
    headers.Authorization = `Bearer ${state.token}`;
  }
  const data = await apiUploadForm(`${apiBase()}/v1/file/upload/chunks/${encodeURIComponent(uploadID)}/${index}`, form, {
    method: "PUT",
    headers,
    signal: options.signal,
    onProgress: options.onProgress,
    logPrefix: "UPLOAD CHUNK",
  });
  return Boolean(data && data.code === 0);
}

function apiUploadForm(url, form, options = {}) {
  return new Promise((resolve) => {
    const xhr = new XMLHttpRequest();
    xhr.open(options.method || "POST", url);
    Object.entries(options.headers || {}).forEach(([key, value]) => xhr.setRequestHeader(key, value));

    xhr.upload.addEventListener("progress", (event) => {
      if (event.lengthComputable) {
        options.onProgress?.(event.loaded, event.total);
      }
    });

    xhr.addEventListener("load", async () => {
      const data = safeJson(xhr.responseText);
      log(`${options.logPrefix || "UPLOAD"} RESPONSE ${xhr.status}`, data);
      if (xhr.status === 401 && options.auth !== false && !options.skipRefresh) {
        const refreshed = await refreshAuthToken();
        if (refreshed) {
          resolve(apiUploadForm(url, form, { ...options, skipRefresh: true }));
          return;
        }
        logout();
      }
      resolve(data);
    });
    xhr.addEventListener("error", () => {
      log(`${options.logPrefix || "UPLOAD"} ERROR`, "network error");
      resolve(null);
    });
    xhr.addEventListener("abort", () => {
      log(`${options.logPrefix || "UPLOAD"} CANCELED`, "upload aborted");
      resolve(null);
    });

    if (options.signal) {
      if (options.signal.aborted) {
        xhr.abort();
        resolve(null);
        return;
      }
      options.signal.addEventListener("abort", () => xhr.abort(), { once: true });
    }

    xhr.send(form);
  });
}

function uploadedBytesFromChunks(uploaded, session, fileSize) {
  let total = 0;
  uploaded.forEach((index) => {
    const start = index * session.chunkSize;
    if (start < fileSize) {
      total += Math.min(session.chunkSize, fileSize - start);
    }
  });
  return total;
}

async function calculateFileSHA256(file, signal) {
  if (!window.crypto || !window.crypto.subtle) {
    log("UPLOAD", "当前浏览器不支持 SHA-256 校验");
    return "";
  }
  if (signal?.aborted) {
    return "";
  }
  try {
    const buffer = await file.arrayBuffer();
    if (signal?.aborted) {
      return "";
    }
    const digest = await window.crypto.subtle.digest("SHA-256", buffer);
    return Array.from(new Uint8Array(digest))
      .map((byte) => byte.toString(16).padStart(2, "0"))
      .join("");
  } catch (error) {
    log("UPLOAD", `文件校验值计算失败：${error.message}`);
    return "";
  }
}

function uploadResponseMatchesSHA256(uploaded, expectedSHA256) {
  const actual = String(uploaded?.sha256 || "").trim().toLowerCase();
  const expected = String(expectedSHA256 || "").trim().toLowerCase();
  if (!actual || !expected || actual === expected) {
    return true;
  }
  log("UPLOAD", "上传校验失败：服务端返回的 SHA-256 和本地文件不一致");
  return false;
}

function multipartUploadStorageKey(file, purpose, sha256) {
  return [
    "multipart-upload",
    state.currentUserID || 0,
    purpose || "",
    file.name,
    file.size,
    file.lastModified || 0,
    sha256 || "",
  ].join(":");
}

async function updateAvatarFromInput(event) {
  const file = event.target.files && event.target.files[0];
  event.target.value = "";
  if (!file) {
    return;
  }

  const uploaded = await apiUploadFile(file, "avatar");
  if (!uploaded || !uploaded.url) {
    return;
  }

  const body = buildUpdateAvatarPayload(uploaded.url);
  const updated = await apiPost("/v1/user/update", body);
  if (!updated || updated.code !== 0) {
    return;
  }

  state.avatar = body.avatar;
  localStorage.setItem(avatarStorageKey(), state.avatar);
  renderUserAvatar();
  log("AVATAR", "头像已更新");
}

async function openMemberPanel() {
  if (!state.currentTarget || state.currentTarget.type !== "group") {
    return;
  }
  els.memberPanel.classList.remove("hidden");
  await refreshGroupMembers();
}

function closeMemberPanel() {
  els.memberPanel.classList.add("hidden");
}

async function refreshGroupMembers() {
  if (!state.currentTarget || state.currentTarget.type !== "group") {
    return;
  }
  const data = await apiPost("/v1/group/member/list", { groupID: state.currentTarget.id });
  state.groupMembers = unwrapArray(data);
  renderGroupMembers();
}

async function setMemberRole(userID, role) {
  await apiPost("/v1/group/member/role", buildMemberRolePayload(state.currentTarget.id, userID, role));
  await refreshGroupMembers();
}

async function removeGroupMember(userID) {
  await apiPost("/v1/group/member/remove", { groupID: state.currentTarget.id, userID });
  await refreshGroupMembers();
}

async function transferGroupOwner(userID) {
  await apiPost("/v1/group/transfer-owner", { groupID: state.currentTarget.id, toUserID: userID });
  await refreshGroupMembers();
  await refreshGroups();
}

// ====== 视图切换 ======

function showAuth(mode) {
  els.authView.classList.remove("hidden");
  els.chatView.classList.add("hidden");
  els.loginForm.classList.toggle("hidden", mode !== "login");
  els.registerForm.classList.toggle("hidden", mode !== "register");
  els.authTitle.textContent = mode === "register" ? "注册" : "登录";
}

function showChat() {
  els.authView.classList.add("hidden");
  els.chatView.classList.remove("hidden");
  els.accountEmail.textContent = state.email || "已登录";
  renderUserAvatar();
}

function switchTab(tab) {
  state.activeTab = tab;

  document.querySelectorAll(".tab-btn").forEach((btn) => {
    btn.classList.toggle("active", btn.dataset.tab === tab);
  });
  document.querySelectorAll(".tab-content").forEach((content) => {
    content.classList.toggle("active", content.id === `${tab}Tab`);
  });

  // 切换时刷新数据
  if (tab === "friends") {
    refreshPending();
    refreshFriends();
  } else if (tab === "groups") {
    refreshGroups();
    refreshGroupJoinRequests();
  } else {
    refreshSessions();
  }

  state.searchQuery = "";
  els.searchInput.value = "";
}

function filterCurrentList() {
  if (state.activeTab === "messages") {
    renderSessions();
  } else if (state.activeTab === "friends") {
    renderMiniTargets(els.friendList, state.friends, "friend", "暂无好友");
  } else if (state.activeTab === "groups") {
    renderMiniTargets(els.groupList, state.groups, "group", "暂无群");
  }
}

// ====== 渲染 ======

function renderUserAvatar() {
  const avatarUrl = resolveAssetUrl(apiBase(), state.avatar);
  els.userAvatar.classList.toggle("has-image", Boolean(avatarUrl));
  els.userAvatar.style.backgroundColor = avatarUrl ? "#fff" : avatarColor(state.currentUserID);
  els.userAvatar.style.backgroundImage = avatarUrl ? `url("${avatarUrl.replace(/"/g, "%22")}")` : "";
  els.userAvatar.textContent = avatarUrl ? "" : avatarInitial(state.email || state.currentUserID);
}

function renderSessions() {
  let items = state.sessions;
  if (state.searchQuery) {
    items = items.filter((item) => {
      const target = normalizeChatTarget(item, "session");
      return (target.title || "").toLowerCase().includes(state.searchQuery);
    });
  }

  if (!items.length) {
    els.sessionList.className = "session-list empty";
    els.sessionList.textContent = state.searchQuery ? "无匹配结果" : "暂无会话";
    return;
  }
  els.sessionList.className = "session-list";
  els.sessionList.textContent = "";
  items.forEach((item) => {
    const target = normalizeChatTarget(item, "session");
    const row = document.createElement("button");
    row.type = "button";
    row.className = "session-item";
    if (isActiveTarget(target)) {
      row.classList.add("active");
    }

    const last = item.lastMessage ? messagePreview(item.lastMessage.content) : "暂无消息";
    const time = item.lastMessage ? formatListTime(item.lastMessage.createdAt) : "";

    row.innerHTML =
      `${avatarHTML(target.id, target.title, "sm")}` +
      `<div class="session-item-content">` +
        `<div class="session-item-top">` +
          `<span class="session-item-name">${escapeHTML(target.title)}</span>` +
          `${time ? `<span class="session-item-time">${escapeHTML(time)}</span>` : ""}` +
        `</div>` +
        `<span class="session-item-preview">${escapeHTML(last)}</span>` +
      `</div>`;
    row.addEventListener("click", () => openTarget(target));
    els.sessionList.append(row);
  });
}

function renderPending() {
  if (!state.pending.length) {
    els.pendingList.className = "mini-list empty";
    els.pendingList.textContent = "暂无申请";
    return;
  }
  els.pendingList.className = "mini-list";
  els.pendingList.textContent = "";
  state.pending.forEach((item) => {
    const row = document.createElement("div");
    row.className = "mini-item";
    const name = item.nickname || item.requesterEmail || `申请 #${item.requestID}`;
    row.innerHTML =
      `${avatarHTML(item.requestID || 0, name, "xs")}` +
      `<div style="flex:1;min-width:0">` +
        `<div style="font-size:14px">${escapeHTML(name)}</div>` +
        `<div style="color:#999;font-size:12px">${escapeHTML(item.requesterEmail || "")}</div>` +
      `</div>`;

    const actions = document.createElement("div");
    actions.className = "pending-actions";
    const accept = document.createElement("button");
    accept.type = "button";
    accept.textContent = "接受";
    accept.addEventListener("click", () => acceptFriend(item.requestID));
    const reject = document.createElement("button");
    reject.type = "button";
    reject.textContent = "拒绝";
    reject.addEventListener("click", () => rejectFriend(item.requestID));
    actions.append(accept, reject);
    row.append(actions);
    els.pendingList.append(row);
  });
}

function renderGroupJoinRequests() {
  const pendingRequests = state.groupJoinRequests.filter((item) => item.status === "pending");
  if (!pendingRequests.length) {
    els.groupJoinRequestList.className = "mini-list empty";
    els.groupJoinRequestList.textContent = "暂无申请";
    return;
  }
  els.groupJoinRequestList.className = "mini-list";
  els.groupJoinRequestList.textContent = "";
  pendingRequests.forEach((item) => {
    const row = document.createElement("div");
    row.className = "mini-item";
    row.innerHTML =
      `<strong>用户 #${escapeHTML(String(item.userID))}</strong>` +
      `<span style="color:#999;font-size:12px">申请加入群 #${escapeHTML(String(item.groupID))}</span>`;

    const actions = document.createElement("div");
    actions.className = "pending-actions";
    const approve = document.createElement("button");
    approve.type = "button";
    approve.textContent = "批准";
    approve.addEventListener("click", () => reviewGroupJoinRequest(item.id, "approved"));
    const reject = document.createElement("button");
    reject.type = "button";
    reject.textContent = "拒绝";
    reject.addEventListener("click", () => reviewGroupJoinRequest(item.id, "rejected"));
    actions.append(approve, reject);
    row.append(actions);
    els.groupJoinRequestList.append(row);
  });
}

function renderGroupMembers() {
  if (!state.groupMembers.length) {
    els.memberList.className = "member-list empty";
    els.memberList.textContent = "暂无成员";
    return;
  }

  const current = state.groupMembers.find((item) => Number(item.userID) === Number(state.currentUserID));
  const currentIsOwner = current && Number(current.role) === 2;
  els.memberList.className = "member-list";
  els.memberList.textContent = "";

  state.groupMembers.forEach((member) => {
    const row = document.createElement("div");
    row.className = "member-item";
    row.innerHTML = `<strong>用户 #${escapeHTML(String(member.userID))}</strong><span>${roleLabel(member.role)} · ${escapeHTML(member.joinedAt || "")}</span>`;

    if (currentIsOwner && Number(member.userID) !== Number(state.currentUserID) && Number(member.role) !== 2) {
      const actions = document.createElement("div");
      actions.className = "member-actions";

      const roleButton = document.createElement("button");
      roleButton.type = "button";
      if (Number(member.role) === 1) {
        roleButton.textContent = "取消管理员";
        roleButton.addEventListener("click", () => setMemberRole(member.userID, 0));
      } else {
        roleButton.textContent = "设为管理员";
        roleButton.addEventListener("click", () => setMemberRole(member.userID, 1));
      }

      const removeButton = document.createElement("button");
      removeButton.type = "button";
      removeButton.textContent = "移除";
      removeButton.addEventListener("click", () => removeGroupMember(member.userID));

      const transferButton = document.createElement("button");
      transferButton.type = "button";
      transferButton.textContent = "转让群主";
      transferButton.addEventListener("click", () => transferGroupOwner(member.userID));

      actions.append(roleButton, removeButton, transferButton);
      row.append(actions);
    }

    els.memberList.append(row);
  });
}

function renderMiniTargets(container, items, source, emptyText) {
  let filtered = items;
  if (state.searchQuery) {
    filtered = items.filter((item) => {
      const target = normalizeChatTarget(item, source);
      return (target.title || "").toLowerCase().includes(state.searchQuery);
    });
  }

  if (!filtered.length) {
    container.className = "mini-list empty";
    container.textContent = state.searchQuery ? "无匹配结果" : emptyText;
    return;
  }
  container.className = "mini-list";
  container.textContent = "";
  filtered.forEach((item) => {
    const target = normalizeChatTarget(item, source);
    const row = document.createElement("button");
    row.type = "button";
    row.className = "mini-item";
    if (isActiveTarget(target)) {
      row.classList.add("active");
    }
    row.innerHTML =
      `${avatarHTML(target.id, target.title, "sm")}` +
      `<div style="flex:1;min-width:0;text-align:left">` +
        `<div style="font-size:14px">${escapeHTML(target.title)}</div>` +
        `${miniTargetMetaHTML(source, target)}` +
      `</div>`;
    row.addEventListener("click", () => openTarget(target));
    container.append(row);
  });
}

function miniTargetMetaHTML(source, target) {
  if (source !== "friend") {
    return '<div class="mini-meta">群组</div>';
  }
  const cls = target.online ? "online" : "offline";
  const text = target.online ? "在线" : "离线";
  return `<div class="mini-meta ${cls}">${text}</div>`;
}

function renderMessages() {
  els.messageList.textContent = "";
  if (!state.currentTarget) {
    els.messageList.innerHTML = '<div class="empty-state">左侧选择会话后开始聊天。</div>';
    return;
  }
  if (!state.messages.length) {
    els.messageList.innerHTML = '<div class="empty-state">暂无消息。</div>';
    return;
  }

  state.messages.forEach((msg, index) => {
    // 时间分隔
    if (shouldShowSeparator(state.messages[index - 1], msg)) {
      const sep = document.createElement("div");
      sep.className = "time-separator";
      sep.innerHTML = `<span>${escapeHTML(formatTimeLabel(msg.createdAt))}</span>`;
      els.messageList.append(sep);
    }

    const own = isOwnMessage(msg, state.currentUserID);
    const row = document.createElement("div");
    row.className = `message-row${own ? " self" : " other"}`;

    // 对方消息：左侧显示头像
    if (!own) {
      const senderName = `用户 #${msg.senderID || "?"}`;
      row.innerHTML += avatarHTML(msg.senderID || 0, senderName, "xs");
    }

    const bubble = document.createElement("div");
    bubble.className = "message-bubble";
    const fileMessage = parseFileMessage(msg.content);
    if (fileMessage) {
      bubble.classList.add("file-message-bubble");
      bubble.append(createFileCard(fileMessage));
      const meta = document.createElement("div");
      meta.className = "message-meta";
      meta.classList.toggle("failed", msg.status === "failed");
      meta.textContent = messageMetaText(msg, own);
      bubble.append(meta);
    } else {
      bubble.innerHTML =
        `<div>${escapeHTML(msg.content || "")}</div>` +
        `<div class="message-meta${msg.status === "failed" ? " failed" : ""}">${escapeHTML(messageMetaText(msg, own))}</div>`;
    }
    row.append(bubble);

    els.messageList.append(row);
  });
  els.messageList.scrollTop = els.messageList.scrollHeight;
}

function messageMetaText(msg, own) {
  const parts = [formatMessageTime(msg.createdAt)];
  if (own) {
    const status = messageStatusText(msg.status);
    if (status) {
      parts.push(status);
    }
  }
  return parts.filter(Boolean).join(" · ");
}

function messageStatusText(status) {
  if (status === "sending") return "发送中";
  if (status === "sent") return "已发送";
  if (status === "read") return "已读";
  if (status === "failed") return "发送失败";
  return "";
}

// ====== 工具栏回调（预留接口） ======

function onEmoji() {
  log("TOOLBAR", "表情按钮 - 功能开发中");
}

function onImage() {
  if (!state.currentTarget) {
    log("UPLOAD", "请先选择会话");
    return;
  }
  els.imageFileInput.click();
}

function onFile() {
  if (!state.currentTarget) {
    log("UPLOAD", "请先选择会话");
    return;
  }
  els.chatFileInput.click();
}

async function uploadComposerFile(event, purpose) {
  const file = event.target.files && event.target.files[0];
  event.target.value = "";
  if (!file) {
    return;
  }
  if (state.activeUpload) {
    log("UPLOAD", "当前已有文件正在上传");
    return;
  }

  const upload = beginUploadProgress(file);
  try {
    const uploaded = await apiUploadFileSmart(file, purpose, {
      signal: upload.controller.signal,
      onProgress: updateUploadProgress,
    });
    if (!uploaded || !uploaded.url) {
      if (!upload.controller.signal.aborted) {
        finishUploadProgress();
      }
      return;
    }

    const text = buildFileMessageContent({
      id: uploaded.id || 0,
      filename: uploaded.filename || file.name,
      url: uploaded.url,
      size: uploaded.size || file.size || 0,
      contentType: uploaded.contentType || file.type || "",
      sha256: uploaded.sha256 || "",
      purpose,
    });
    if (sendMessageContent(text)) {
      log("UPLOAD", `${uploaded.filename || file.name} 已上传并发送`);
    }
  } finally {
    finishUploadProgress();
  }
}

function beginUploadProgress(file) {
  const controller = new AbortController();
  state.activeUpload = { controller };
  els.uploadFileName.textContent = file.name || "file";
  updateUploadProgress(0, file.size || 0);
  els.uploadProgress.classList.remove("hidden");
  els.cancelUploadBtn.disabled = false;
  return state.activeUpload;
}

function updateUploadProgress(loaded, total) {
  const percent = total > 0 ? Math.min(100, Math.round((loaded / total) * 100)) : 0;
  els.uploadPercent.textContent = `${percent}%`;
  els.uploadBar.style.width = `${percent}%`;
}

function cancelActiveUpload() {
  if (!state.activeUpload) {
    return;
  }
  els.cancelUploadBtn.disabled = true;
  state.activeUpload.controller.abort();
  log("UPLOAD", "上传已取消");
}

function finishUploadProgress() {
  state.activeUpload = null;
  els.uploadProgress.classList.add("hidden");
  els.uploadFileName.textContent = "";
  els.uploadPercent.textContent = "0%";
  els.uploadBar.style.width = "0%";
  els.cancelUploadBtn.disabled = false;
}

function createFileCard(file) {
  const card = document.createElement("div");
  card.className = "file-card";

  const icon = document.createElement("div");
  icon.className = "file-card-icon";
  icon.textContent = fileIconLabel(file);

  const info = document.createElement("div");
  info.className = "file-card-info";

  const name = document.createElement("div");
  name.className = "file-card-name";
  name.textContent = file.filename;

  const meta = document.createElement("div");
  meta.className = "file-card-meta";
  const size = formatFileSize(file.size);
  meta.textContent = [file.contentType || "", size].filter(Boolean).join(" · ") || "文件";

  info.append(name, meta);

  const download = document.createElement("button");
  download.type = "button";
  download.className = "file-card-download";
  download.textContent = "下载";
  download.addEventListener("click", () => downloadFile(file));

  card.append(icon, info, download);
  return card;
}

async function downloadFile(file) {
  const url = file.id
    ? `${apiBase()}/v1/file/${encodeURIComponent(file.id)}/download`
    : resolveAssetUrl(apiBase(), file.url);
  try {
    const response = await fetchDownload(url, Boolean(file.id));
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    const blob = await response.blob();
    const objectURL = URL.createObjectURL(blob);
    triggerDownload(objectURL, file.filename);
    URL.revokeObjectURL(objectURL);
  } catch (error) {
    log("DOWNLOAD ERROR", error.message);
    triggerDownload(url, file.filename);
  }
}

async function fetchDownload(url, authRequired, retried = false) {
  const options = authRequired && state.token
    ? { headers: { Authorization: `Bearer ${state.token}` } }
    : {};
  const response = await fetch(url, options);
  if (response.status === 401 && authRequired && !retried) {
    const refreshed = await refreshAuthToken();
    if (refreshed) {
      return fetchDownload(url, authRequired, true);
    }
  }
  return response;
}

function triggerDownload(url, filename) {
  const link = document.createElement("a");
  link.href = url;
  link.download = filename || "file";
  link.target = "_blank";
  link.rel = "noopener";
  document.body.append(link);
  link.click();
  link.remove();
}

// ====== 头像工具 ======

function avatarColor(id) {
  const hue = ((Number(id) || 0) * 137 + 180) % 360;
  return `hsl(${hue}, 55%, 50%)`;
}

function avatarInitial(name) {
  const s = String(name || "?").trim();
  return s ? s[0].toUpperCase() : "?";
}

function avatarHTML(id, name, size) {
  const cls = size === "sm" ? "avatar-sm" : size === "xs" ? "avatar-xs" : "";
  return `<span class="avatar ${cls}" style="background:${avatarColor(id)}">${escapeHTML(avatarInitial(name))}</span>`;
}

// ====== 时间工具 ======

function shouldShowSeparator(prev, curr) {
  if (!prev) return true;
  const gap = (Date.parse(curr.createdAt) || 0) - (Date.parse(prev.createdAt) || 0);
  return gap > 5 * 60 * 1000;
}

function formatMessageTime(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  return d.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });
}

function formatListTime(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const now = new Date();
  const isToday = d.toDateString() === now.toDateString();
  const time = d.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });
  if (isToday) return time;
  if (d.getFullYear() === now.getFullYear()) {
    return `${d.getMonth() + 1}/${d.getDate()}`;
  }
  return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()}`;
}

function formatTimeLabel(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const now = new Date();
  const time = d.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });
  const isToday = d.toDateString() === now.toDateString();
  if (isToday) return `今天 ${time}`;
  const yesterday = new Date(now);
  yesterday.setDate(yesterday.getDate() - 1);
  if (d.toDateString() === yesterday.toDateString()) return `昨天 ${time}`;
  return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()} ${time}`;
}

// ====== 辅助函数 ======

function isActiveTarget(target) {
  return state.currentTarget && state.currentTarget.type === target.type && state.currentTarget.id === target.id;
}

function formToJson(form) {
  const body = {};
  form.querySelectorAll("input[name], textarea[name]").forEach((field) => {
    const value = field.value.trim();
    if (value === "") {
      return;
    }
    body[field.name] = field.type === "number" ? Number(value) : value;
  });
  return body;
}

function avatarStorageKey() {
  return `chatAvatar:${state.currentUserID || state.email || "anonymous"}`;
}

function unwrapArray(response) {
  if (!response || response.code !== 0 || !Array.isArray(response.data)) {
    return [];
  }
  return response.data;
}

function uniqueBy(items, key) {
  const seen = new Set();
  return items.filter((item) => {
    if (seen.has(item[key])) {
      return false;
    }
    seen.add(item[key]);
    return true;
  });
}

function apiBase() {
  return normalizeBaseUrl(els.baseUrl.value);
}

function updateWsStatus(status) {
  els.wsState.className = `user-status ${status}`;
  if (status === "online") {
    els.wsState.textContent = "在线";
  } else if (status === "connecting") {
    els.wsState.textContent = "连接中";
  } else {
    els.wsState.textContent = "离线";
  }
}

function safeJson(value) {
  try {
    return JSON.parse(value);
  } catch {
    return value;
  }
}

function log(title, payload) {
  const time = new Date().toLocaleTimeString();
  const body = typeof payload === "string" ? payload : JSON.stringify(payload, null, 2);
  els.logOutput.textContent = `[${time}] ${title}\n${body}\n\n${els.logOutput.textContent}`;
}

function escapeHTML(value) {
  return String(value).replace(/[&<>"']/g, (char) => {
    const entities = {
      "&": "&amp;",
      "<": "&lt;",
      ">": "&gt;",
      '"': "&quot;",
      "'": "&#039;",
    };
    return entities[char];
  });
}

function roleLabel(role) {
  switch (Number(role)) {
    case 2:
      return "群主";
    case 1:
      return "管理员";
    default:
      return "成员";
  }
}
