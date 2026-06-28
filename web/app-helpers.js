export function normalizeBaseUrl(value) {
  const trimmed = String(value || "").trim().replace(/\/+$/, "");
  return trimmed || "http://localhost:8080";
}

export function buildWsUrl(baseUrl, token) {
  const normalized = normalizeBaseUrl(baseUrl);
  const wsBase = normalized.replace(/^https:/, "wss:").replace(/^http:/, "ws:");
  return `${wsBase}/v1/ws?token=${encodeURIComponent(token)}`;
}

export function normalizeChatTarget(item, source) {
  if (source === "friend") {
    const id = Number(item.userID);
    return {
      type: "private",
      id,
      title: item.nickname || `用户 #${id}`,
      online: Boolean(item.online),
    };
  }

  if (source === "group") {
    const id = Number(item.id);
    return {
      type: "group",
      id,
      title: item.name || `群 #${id}`,
    };
  }

  const type = item.targetType;
  const id = Number(item.targetID);
  return {
    type,
    id,
    title: item.name || `${type} #${id}`,
  };
}

export function buildMessagePayload(target, content, idFactory = defaultClientMsgID) {
	return {
		type: "message",
		clientMsgID: idFactory(),
		targetType: target.type,
		targetID: target.id,
		content: String(content || "").trim(),
	};
}

export function createLocalMessage(payload, senderID, createdAt = new Date().toISOString()) {
	return {
		id: payload.clientMsgID,
		clientMsgID: payload.clientMsgID,
		senderID,
		content: payload.content,
		createdAt,
		local: true,
		status: "sending",
	};
}

export function applyMessageAck(messages, ack) {
	const clientMsgID = String(ack?.clientMsgID || "");
	if (!clientMsgID) {
		return messages;
	}
	return messages.map((message) => {
		if (!messageMatchesClientID(message, clientMsgID)) {
			return message;
		}
		return {
			...message,
			id: Number(ack.messageID || message.id),
			createdAt: ack.createdAt || message.createdAt,
			local: false,
			status: "sent",
		};
	});
}

export function applyMessageFailure(messages, clientMsgID) {
	const id = String(clientMsgID || "");
	if (!id) {
		return messages;
	}
	return messages.map((message) => {
		if (!messageMatchesClientID(message, id)) {
			return message;
		}
		return {
			...message,
			status: "failed",
		};
	});
}

export function applyMessageRead(messages, event, currentUserID) {
	const messageID = Number(event?.messageID || 0);
	const readerID = Number(event?.readerID || 0);
	if (!messageID || !readerID || readerID === Number(currentUserID)) {
		return messages;
	}
	return messages.map((message) => {
		const id = Number(message.id || 0);
		if (!id || id > messageID || Number(message.senderID) !== Number(currentUserID) || message.status === "failed") {
			return message;
		}
		return {
			...message,
			status: "read",
		};
	});
}

export function buildFileMessageContent(file) {
  return JSON.stringify({
    kind: "file",
    id: Number(file?.id || 0),
    filename: String(file?.filename || "file").trim() || "file",
    url: String(file?.url || "").trim(),
    size: Number(file?.size || 0),
    contentType: String(file?.contentType || "").trim(),
    sha256: String(file?.sha256 || "").trim(),
    purpose: String(file?.purpose || "").trim(),
  });
}

export function parseFileMessage(content) {
  const text = String(content || "").trim();
  if (!text) {
    return null;
  }

  const structured = parseStructuredFileMessage(text);
  if (structured) {
    return structured;
  }

  return parseLegacyFileMessage(text);
}

export function formatFileSize(size) {
  const bytes = Number(size || 0);
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return "";
  }
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  if (bytes < 1024 * 1024) {
    return `${formatSizeNumber(bytes / 1024)} KB`;
  }
  return `${formatSizeNumber(bytes / 1024 / 1024)} MB`;
}

export function fileIconLabel(file) {
  const filename = String(file?.filename || "").toLowerCase();
  const contentType = String(file?.contentType || "").toLowerCase();
  if (contentType.includes("pdf") || filename.endsWith(".pdf")) return "PDF";
  if (contentType.includes("word") || /\.(doc|docx)$/.test(filename)) return "DOC";
  if (contentType.startsWith("image/") || /\.(png|jpe?g|gif|webp)$/.test(filename)) return "IMG";
  if (filename.endsWith(".zip")) return "ZIP";
  if (filename.endsWith(".txt")) return "TXT";
  return "FILE";
}

export function messagePreview(content) {
  const file = parseFileMessage(content);
  if (file) {
    const filename = file.filename || "文件";
    return `[文件] ${filename}`;
  }
  return String(content || "");
}

export function buildAddFriendPayload(email) {
  return {
    friendEmail: String(email || "").trim(),
  };
}

export function buildUpdateAvatarPayload(avatarUrl) {
  return {
    avatar: String(avatarUrl || "").trim(),
  };
}

export function resolveAssetUrl(baseUrl, url) {
  const value = String(url || "").trim();
  if (!value) {
    return "";
  }
  if (/^https?:\/\//i.test(value)) {
    return value;
  }
  const normalizedBase = normalizeBaseUrl(baseUrl);
  return `${normalizedBase}/${value.replace(/^\/+/, "")}`;
}

export function buildGroupReviewPayload(requestID, status) {
  return {
    requestID: Number(requestID),
    status,
  };
}

export function buildMemberRolePayload(groupID, userID, role) {
  return {
    groupID: Number(groupID),
    userID: Number(userID),
    role: Number(role),
  };
}

export function sortMessagesAscending(messages) {
  return [...messages].sort((a, b) => {
    const timeA = Date.parse(a.createdAt || "") || 0;
    const timeB = Date.parse(b.createdAt || "") || 0;
    if (timeA !== timeB) {
      return timeA - timeB;
    }
    return numericMessageID(a.id) - numericMessageID(b.id);
  });
}

export function decodeTokenUserID(token) {
  try {
    const [, payload] = String(token || "").split(".");
    if (!payload) {
      return 0;
    }
    const normalized = payload.replace(/-/g, "+").replace(/_/g, "/");
    const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "=");
    const json = JSON.parse(atob(padded));
    return Number(json.user_id || 0);
  } catch {
    return 0;
  }
}

export function isOwnMessage(message, currentUserID) {
  if (message.local) {
    return true;
  }
  return Number(message.senderID) === Number(currentUserID);
}

function defaultClientMsgID() {
  return `web-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function messageMatchesClientID(message, clientMsgID) {
	return String(message.clientMsgID || message.id || "") === clientMsgID;
}

function numericMessageID(id) {
	const value = Number(id || 0);
	return Number.isFinite(value) ? value : 0;
}

function parseStructuredFileMessage(text) {
  try {
    const data = JSON.parse(text);
    if (!data || data.kind !== "file" || !data.url) {
      return null;
    }
    return normalizeFileMessage(data);
  } catch {
    return null;
  }
}

function parseLegacyFileMessage(text) {
  const match = text.match(/^(.+?):\s*(https?:\/\/\S+\/uploads\/\S+|\/uploads\/\S+)$/);
  if (!match) {
    return null;
  }
  return normalizeFileMessage({
    filename: match[1],
    url: match[2],
  });
}

function normalizeFileMessage(file) {
  const url = String(file.url || "").trim();
  if (!url) {
    return null;
  }
  return {
    id: Number(file.id || 0),
    filename: String(file.filename || "file").trim() || "file",
    url,
    size: Number(file.size || 0),
    contentType: String(file.contentType || "").trim(),
    sha256: String(file.sha256 || "").trim(),
    purpose: String(file.purpose || "").trim(),
  };
}

function formatSizeNumber(value) {
  return Number.isInteger(value) ? String(value) : value.toFixed(1).replace(/\.0$/, "");
}
