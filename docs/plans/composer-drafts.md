# Keeping the composer's draft per session

**Status:** steps 1–2 built on main 2026-11-14; tests pass, not yet checked in a browser, not pushed.

## Problem

Switching sessions in the chat view (a nav click, back/forward) runs `resetSession`, which calls `setPrompt("")` and `attachments.clear()`. A half-written message and its images are gone, and going back to the session doesn't bring them back. A page reload loses them too. A message held for a session that is still starting (`heldUntilStart`) is dropped the same way.

Two smaller bugs sit in the same code:

- `submitPrompt` clears the box after `await sendPrompt(...)`. If you switch session while the request is in flight, it clears the *new* session's box and images.
- `attachments.js` deletes a removed image under `getWindowID()`, the window shown *now*, not the one the image was uploaded to, and an upload that fails after a switch reports its error in the other session's error line.

## Design

A draft is `{text, attachments}` per window ID, kept in two places:

- **In memory** (a `Map` in `chat.js`): the live attachment items, with their `File`, blob URL and in-flight upload. Switching away detaches them from the strip without aborting the upload or revoking the URL; switching back puts them back. This is what makes a switch lossless, uploads still running included.
- **In localStorage** (`mo.composerDraft.<windowID>` → `{text, attachments: [{id, name}], at}`): what a reload needs. Only uploaded images (`state: "ready"`) are stored, by id. After a reload their thumbnails come from a new `GET /api/sessions/{windowID}/attachments/{id}`, which serves the stored file (`AttachmentStore.Path`, so only that window's ids, never a path). An id that has expired (24 h, `attachmentTTL`) answers 404 and its chip is dropped. Drafts older than 24 h are ignored and removed, since their images are gone and the window id may since belong to another session (tmux numbers windows per server).

A draft is written on every input and every attachment change, and removed when the message is sent or the box is emptied with no images left. Sending clears the draft of the window it was sent to, whatever is shown by then.

Pure code (no DOM) goes in `static/draft-model.js`, Node-tested: reading and writing the stored form (`loadDraft`, `saveDraft`, `dropDraft` over an injected storage and clock, all wrapped in try/catch since storage can throw), and the 24 h expiry.

## Steps

### Step 1: in-memory drafts across switches

- `attachments.js`: each item remembers its `windowID`; deletes and error reports use it (an error from a detached item isn't shown). New `detach()` returns the items without aborting or revoking, and `restore(items)` shows them again (re-rendering an upload that finished meanwhile).
- `chat.js`: `resetSession` stashes the old window's `{text, items}` (text = the box, or `heldUntilStart` if a message was held) before clearing, and restores the new window's draft after.
- `submitPrompt` and the held-message send capture the window id and only clear the box if it's still shown; otherwise they drop that window's stashed draft.

### Step 2: drafts survive a reload

- Server: `GET /api/sessions/{windowID}/attachments/{id}` (`handlers_attachments.go`): the file from `AttachmentStore.Path`, `Content-Type` from its extension (the store only keeps the four sniffed types), `X-Content-Type-Options: nosniff`, `Cache-Control: private, max-age=86400`. Not gated on a live session, like DELETE. Tests: served bytes and type, another window's id, a malformed id and an unknown id are 404.
- `draft-model.js` + `jstests/draft_model.test.js`: round trip, expiry, only ready attachments stored, storage that throws.
- `attachments.js`: `restoreStored([{id, name}])` adds ready items whose `url` is the GET endpoint; a thumbnail that fails to load drops its chip.
- `chat.js`: save on `input` and on attachment changes (`onChange`); on `resetSession`, the in-memory draft wins, else the stored one.
