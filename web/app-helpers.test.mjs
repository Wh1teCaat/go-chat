import assert from "node:assert/strict";
import test from "node:test";

import {
  buildAddFriendPayload,
  buildGroupReviewPayload,
  buildFileMessageContent,
  createLocalMessage,
  buildMessagePayload,
  buildMemberRolePayload,
  buildUpdateAvatarPayload,
  fileIconLabel,
  formatFileSize,
  buildWsUrl,
  applyMessageAck,
  applyMessageFailure,
  applyMessageRead,
  decodeTokenUserID,
  isOwnMessage,
  messagePreview,
  normalizeBaseUrl,
  normalizeChatTarget,
  parseFileMessage,
  resolveAssetUrl,
  sortMessagesAscending,
} from "./app-helpers.js";

test("normalizeBaseUrl trims spaces and trailing slashes", () => {
  assert.equal(normalizeBaseUrl(" http://localhost:8080/// "), "http://localhost:8080");
  assert.equal(normalizeBaseUrl(""), "http://localhost:8080");
});

test("buildWsUrl converts http base URL and appends token", () => {
  assert.equal(
    buildWsUrl("http://localhost:8080", "abc 123"),
    "ws://localhost:8080/v1/ws?token=abc%20123",
  );
  assert.equal(
    buildWsUrl("https://api.example.com/", "token"),
    "wss://api.example.com/v1/ws?token=token",
  );
});

test("normalizeChatTarget supports friends, groups, and sessions", () => {
  assert.deepEqual(normalizeChatTarget({ userID: 2, nickname: "Bob", online: true }, "friend"), {
    type: "private",
    id: 2,
    title: "Bob",
    online: true,
  });
  assert.deepEqual(normalizeChatTarget({ id: 7, name: "Dev" }, "group"), {
    type: "group",
    id: 7,
    title: "Dev",
  });
  assert.deepEqual(normalizeChatTarget({ targetType: "private", targetID: 3, name: "" }, "session"), {
    type: "private",
    id: 3,
    title: "private #3",
  });
});

test("buildMessagePayload creates websocket message body", () => {
  const payload = buildMessagePayload(
    { type: "group", id: 5 },
    " hello ",
    () => "fixed-id",
  );

  assert.deepEqual(payload, {
    type: "message",
    clientMsgID: "fixed-id",
    targetType: "group",
    targetID: 5,
    content: "hello",
  });
});

test("message status helpers track local send lifecycle", () => {
  const payload = buildMessagePayload(
    { type: "private", id: 2 },
    "hello",
    () => "client-1",
  );
  const local = createLocalMessage(payload, 1, "2026-06-23T10:00:00Z");

  assert.deepEqual(local, {
    id: "client-1",
    clientMsgID: "client-1",
    senderID: 1,
    content: "hello",
    createdAt: "2026-06-23T10:00:00Z",
    local: true,
    status: "sending",
  });

  const acked = applyMessageAck([local], {
    clientMsgID: "client-1",
    messageID: 9,
    createdAt: "2026-06-23T10:00:01Z",
  });
  assert.deepEqual(acked[0], {
    id: 9,
    clientMsgID: "client-1",
    senderID: 1,
    content: "hello",
    createdAt: "2026-06-23T10:00:01Z",
    local: false,
    status: "sent",
  });

  const read = applyMessageRead(acked, {
    messageID: 9,
    readerID: 2,
  }, 1);
  assert.equal(read[0].status, "read");

  const failed = applyMessageFailure([local], "client-1");
  assert.equal(failed[0].status, "failed");
});

test("buildFileMessageContent serializes uploaded file metadata", () => {
	const content = buildFileMessageContent({
		id: 12,
		filename: "1.pdf",
		url: "/v1/file/12/download",
		size: 1024,
		contentType: "application/pdf",
		sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
    purpose: "chat_file",
  });

	assert.equal(content, JSON.stringify({
		kind: "file",
		id: 12,
		filename: "1.pdf",
		url: "/v1/file/12/download",
		size: 1024,
    contentType: "application/pdf",
    sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
    purpose: "chat_file",
  }));
});

test("parseFileMessage supports structured and legacy file messages", () => {
	assert.deepEqual(parseFileMessage(JSON.stringify({
		kind: "file",
		id: 12,
		filename: "1.pdf",
		url: "/v1/file/12/download",
		size: 1024,
		contentType: "application/pdf",
		sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	})), {
		id: 12,
		filename: "1.pdf",
		url: "/v1/file/12/download",
		size: 1024,
    contentType: "application/pdf",
    sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
    purpose: "",
  });

	assert.deepEqual(parseFileMessage("1.pdf: http://localhost:8080/uploads/2026/06/15/file.pdf"), {
		id: 0,
		filename: "1.pdf",
    url: "http://localhost:8080/uploads/2026/06/15/file.pdf",
    size: 0,
    contentType: "",
    sha256: "",
    purpose: "",
  });

  assert.equal(parseFileMessage("plain text"), null);
});

test("messagePreview formats file messages for session list", () => {
  assert.equal(messagePreview(JSON.stringify({
    kind: "file",
    id: 12,
    filename: "report.pdf",
    url: "/v1/file/12/download",
  })), "[文件] report.pdf");

  assert.equal(messagePreview("hello"), "hello");
});

test("file display helpers format size and icon labels", () => {
  assert.equal(formatFileSize(0), "");
  assert.equal(formatFileSize(1024), "1 KB");
  assert.equal(formatFileSize(1536), "1.5 KB");
  assert.equal(formatFileSize(2 * 1024 * 1024), "2 MB");
  assert.equal(fileIconLabel({ filename: "report.pdf", contentType: "application/pdf" }), "PDF");
  assert.equal(fileIconLabel({ filename: "report.docx", contentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document" }), "DOC");
  assert.equal(fileIconLabel({ filename: "photo.png", contentType: "image/png" }), "IMG");
  assert.equal(fileIconLabel({ filename: "archive.zip", contentType: "" }), "ZIP");
});

test("buildAddFriendPayload uses email only", () => {
  assert.deepEqual(buildAddFriendPayload(" bob@example.com "), {
    friendEmail: "bob@example.com",
  });
});

test("buildUpdateAvatarPayload trims avatar URL", () => {
  assert.deepEqual(buildUpdateAvatarPayload(" /uploads/avatar.png "), {
    avatar: "/uploads/avatar.png",
  });
});

test("resolveAssetUrl resolves backend relative upload URLs", () => {
  assert.equal(
    resolveAssetUrl("http://localhost:8080/", "/uploads/avatar.png"),
    "http://localhost:8080/uploads/avatar.png",
  );
  assert.equal(
    resolveAssetUrl("http://localhost:8080", "https://cdn.example.com/avatar.png"),
    "https://cdn.example.com/avatar.png",
  );
  assert.equal(resolveAssetUrl("http://localhost:8080", ""), "");
});

test("buildGroupReviewPayload creates review body", () => {
  assert.deepEqual(buildGroupReviewPayload(9, "approved"), {
    requestID: 9,
    status: "approved",
  });
});

test("buildMemberRolePayload creates member role body", () => {
  assert.deepEqual(buildMemberRolePayload(3, 8, 1), {
    groupID: 3,
    userID: 8,
    role: 1,
  });
});

test("sortMessagesAscending orders oldest first", () => {
  const input = [
    { id: 3, createdAt: "2026-06-10T10:00:03Z" },
    { id: 1, createdAt: "2026-06-10T10:00:01Z" },
    { id: 2, createdAt: "2026-06-10T10:00:02Z" },
  ];

  assert.deepEqual(sortMessagesAscending(input).map((item) => item.id), [1, 2, 3]);
});

test("decodeTokenUserID reads jwt user_id without verifying signature", () => {
  const payload = Buffer.from(JSON.stringify({ user_id: 42 })).toString("base64url");
  assert.equal(decodeTokenUserID(`header.${payload}.signature`), 42);
  assert.equal(decodeTokenUserID("bad-token"), 0);
});

test("isOwnMessage compares numeric sender ID to current user ID", () => {
  assert.equal(isOwnMessage({ senderID: 42 }, 42), true);
  assert.equal(isOwnMessage({ senderID: "42" }, 42), true);
  assert.equal(isOwnMessage({ senderID: 7 }, 42), false);
  assert.equal(isOwnMessage({ local: true }, 42), true);
});
