// The composer's draft as kept in localStorage, so a page reload doesn't
// lose a half-written message or its images. chat.js keeps the live draft
// (uploads still running) in memory; this is only the stored form:
// mo.composerDraft.<windowID> -> {text, attachments: [{id, name}], at}.

// A draft older than this is dropped: its images have expired on the server
// (attachmentTTL), and the window id may belong to another session by now.
const DRAFT_TTL_MS = 24 * 60 * 60 * 1000;

const draftKey = (windowID) => `mo.composerDraft.${windowID}`;

// saveDraft stores text and the uploaded images ({id, name, state} items;
// only "ready" ones have an id to come back to). An empty draft is removed.
function saveDraft(storage, windowID, text, items, now = Date.now()) {
  const attachments = (items || [])
    .filter((i) => i.state === "ready" && i.id)
    .map((i) => ({ id: i.id, name: i.name || "" }));
  try {
    if (!(text || "").trim() && !attachments.length) storage.removeItem(draftKey(windowID));
    else storage.setItem(draftKey(windowID), JSON.stringify({ text: text || "", attachments, at: now }));
  } catch {
    // storage full or blocked: the in-memory draft still covers switches
  }
}

// loadDraft returns {text, attachments} or null for none, a malformed one or
// one past DRAFT_TTL_MS (which is removed).
function loadDraft(storage, windowID, now = Date.now()) {
  let d;
  try {
    d = JSON.parse(storage.getItem(draftKey(windowID)));
  } catch {
    return null;
  }
  if (!d || typeof d !== "object" || typeof d.text !== "string" || !Array.isArray(d.attachments)) return null;
  if (!(now - d.at < DRAFT_TTL_MS)) {
    dropDraft(storage, windowID);
    return null;
  }
  const attachments = d.attachments
    .filter((a) => a && typeof a.id === "string")
    .map((a) => ({ id: a.id, name: typeof a.name === "string" ? a.name : "" }));
  return { text: d.text, attachments };
}

function dropDraft(storage, windowID) {
  try {
    storage.removeItem(draftKey(windowID));
  } catch {
    // nothing to do
  }
}

if (typeof module === "object" && module.exports) {
  module.exports = { saveDraft, loadDraft, dropDraft, DRAFT_TTL_MS };
}
