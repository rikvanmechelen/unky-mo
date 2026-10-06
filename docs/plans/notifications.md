# Web Push notifications

**Status:** steps 1–4 built on main, 2026-10-05 (`0cd9730`, `6d39f4a`, `9472fe6`, `fe050f8`), installed. Step 5 (end to end) is open: the emulator run stopped at the dashboard's basic-auth login, so nothing has been checked on a device yet. CLAUDE.md isn't updated yet either.

## Goal

Get a notification on the phone (and desktop Chrome) when a session **needs input** (status `question` or `permission`), and optionally when it's **done** (`active` → `idle`), while walking around the building on the same network. Tapping it opens that session's chat view, where the question and permission banners already answer it.

Out of scope for now: answering from the notification itself (action buttons), e-mail/ntfy, notifying when the dashboard can't be reached.

## How Web Push fits

```
  TUI ──writes──▶ /tmp/unky-mo-state.json ──polled 1s──▶ mo web (notifier)
                                                            │ encrypted POST (RFC 8291 + VAPID)
                                                            ▼
                                    Google FCM / Apple / Mozilla push service
                                                            │
                                                            ▼
                                          phone / desktop browser's service worker
                                                            │ showNotification
                                                            ▼
                                     tap → /chat?window=@N (needs the LAN + trusted CA)
```

- The browser subscribes once (`PushManager.subscribe` with our VAPID public key) and posts the subscription (endpoint + `p256dh` + `auth`) to `mo web`, which keeps it.
- `mo web` watches the shared state file, as it already does for every page. The TUI owns `status.Manager`, and `mo web` still never touches it or the hook socket. A status change becomes a small JSON payload, encrypted for each subscription and POSTed to its endpoint.
- The local CA only matters for the page and the service worker. The push hop uses the push services' public certificates (verified: the phone trusts the CA after a Chrome restart, see the CA download in `03d09f3`).
- Android Chrome works from a normal tab. Desktop Chrome works while Chrome runs (no tab needed). iOS needs the dashboard added to the Home Screen (iOS 16.4+), hence a manifest.

## Decisions

- **Stdlib only.** Encryption (`aes128gcm`, RFC 8188/8291) and VAPID (RFC 8292, ES256 JWT) are ~200 lines over `crypto/ecdh`, `crypto/ecdsa`, `crypto/hkdf`, `crypto/aes`. Same choice as the local CA. Checked against RFC 8291's Appendix A vectors.
- **Endpoint allowlist.** The server only POSTs to `https` endpoints on known push hosts (`fcm.googleapis.com`, `*.push.services.mozilla.com`, `*.push.apple.com`, `*.notify.windows.com`). Otherwise an authenticated browser could make `mo web` POST to any internal URL.
- **VAPID `sub`** is the project URL (`https://github.com/rvanmech/unky-mo`), not a personal e-mail: it goes to Google/Apple. Apple rejects tokens whose `sub` isn't a `mailto:` or `https:` URL.
- **Payload** is built server-side and stays small (title, body, tag, url, window): it's end-to-end encrypted, but kept to what the notification shows. Under 3 KB.
- **Push headers:** `Topic` per window (an undelivered "needs you" for the same window is replaced, not stacked), `Urgency: high` for needs-input, `normal` for done, `TTL` 1 h for needs-input, 10 min for done.
- **Per-device choices,** stored with the subscription: needs input (default on), done (default off).
- **Don't buzz the device you're looking at:** a page showing a session's chat, visible and focused, sends a presence heartbeat (`POST /api/push/presence {endpoint, window}`, every 15 s and on visibility changes). The notifier skips that subscription for that window while the heartbeat is fresh (30 s). Server-side because iOS forbids pushes that show nothing, and Chrome punishes them.
- **Restarts don't notify.** The notifier's first read only primes the last-known statuses. A new status must be seen on two consecutive reads (~1–2 s) before it notifies, which rides out the TUI rewriting rows during a restart.
- **Storage:** `<config dir>/push/vapid.pem` (the private key, 0600, created once; a new key invalidates every subscription) and `<config dir>/push/subscriptions.json` (0600). Subscriptions the push service answers 404/410 are dropped.
- **Config:** `[web] disable_push` turns the whole thing off (routes 404, notifier not started), like `disable_bash_diffs`.
- **Basic auth** wraps the new routes like everything else. The service worker script is fetched with the page's credentials; whether an iOS Home Screen app keeps a basic-auth login is a risk to probe in step 5.

## Notification content

| Event | Title | Body |
|---|---|---|
| question | `<project> needs you` | the first question's text (AskUserQuestion), cut to ~180 chars |
| permission | `<project> needs permission` | Bash: the command · Edit/Write: `Edit <file name>` · ExitPlanMode: `Approve the plan` · WebFetch: the URL's host · else the tool name |
| done | `<project> is done` | `<window> · <branch>` |

`<project>` is the row's `name` (plus `/ <branch>` for a worktree row). The tag is `mo-<windowID>`, so a newer notification for a window replaces the older one on the device too. A permission or question whose pending tool/input changes while the status stays the same (the next prompt in a row) notifies again.

## Steps

1. **`internal/webpush`:** VAPID keys (create/load), the RFC 8291 encryption, the VAPID JWT, `Send` over an injectable HTTP client, the endpoint allowlist. Tests against the RFC vectors and a fake push service.
2. **Server side:** the subscription store, `GET /api/push` (public key, whether enabled), `POST/DELETE /api/push/subscriptions`, `POST /api/push/test`, `POST /api/push/presence`, `[web] disable_push`, wired in `cmd/mo/web.go`. The service worker (`sw.js`) and web app manifest + PNG icons served from the root.
3. **The notifier:** the pure transition rules (prime, two-read confirmation, needs-input, done, changed pending content) and message formatting, tested; the 1 s state-file loop in `mo web`, sending to the subscriptions that want the event, skipping present devices, pruning gone ones.
4. **Browser:** the "Notifications" dialog in the dashboard header and chat nav (state of this device, the two choices, Enable/Disable, Send test; iOS outside the Home Screen gets the Add to Home Screen steps), the service worker's `push` and `notificationclick` handlers, the presence heartbeat.
5. **End to end:** Android Chrome on the emulator, desktop Chrome, an iPhone if one's available (Home Screen app + basic auth). CLAUDE.md.

## Step 1 in detail: `internal/webpush`

A package with no knowledge of sessions or HTTP routes, only "send this payload to this subscription".

- **Keys:** `LoadOrCreateKeys(path) (*Keys, created bool, error)` reads a PEM `EC PRIVATE KEY` (P-256), or creates one (dir 0700, file 0600, written to a temp file and renamed). A file that exists but doesn't parse is an error, never overwritten: replacing the key silently would invalidate every device's subscription. `Keys.PublicKey()` is the uncompressed point, base64url without padding: what the browser passes as `applicationServerKey`.
- **Subscription:** the browser's `PushSubscription.toJSON()` shape, `{endpoint, keys: {p256dh, auth}}`. `Validate` checks the endpoint (below), that `p256dh` decodes to a 65-byte point on P-256 and `auth` to 16 bytes.
- **Endpoint allowlist** (`CheckEndpoint`): `https`, no user info, port empty or 443, host equal to or under `fcm.googleapis.com`, `push.services.mozilla.com`, `push.apple.com`, `notify.windows.com`. Anything else is refused before a request is built.
- **Encryption** (`encrypt(plaintext, uaPublic, auth, asPrivate, salt)`, RFC 8291 §3.4 + RFC 8188): ECDH(as, ua) → `PRK_key = HKDF-Extract(auth, ecdh)` → `IKM = HKDF-Expand(PRK_key, "WebPush: info\0" ‖ ua_public ‖ as_public, 32)` → `PRK = HKDF-Extract(salt, IKM)` → CEK (16 bytes, `"Content-Encoding: aes128gcm\0"`) and nonce (12 bytes, `"Content-Encoding: nonce\0"`). One record: `plaintext ‖ 0x02`, sealed with AES-128-GCM. Body = `salt(16) ‖ rs=4096 (uint32 BE) ‖ 65 ‖ as_public ‖ ciphertext`. The exported path makes a fresh ephemeral key and random salt per message. Payloads over 3072 bytes are refused (one record holds 4079; the cap leaves room).
- **VAPID** (RFC 8292): an ES256 JWT `{aud: <endpoint's scheme://host>, exp: now+12h, sub: "https://github.com/rvanmech/unky-mo"}`, signature as raw `r ‖ s`. Header `Authorization: vapid t=<jwt>, k=<public key>`.
- **Send:** `Sender{Keys, Client (Do(*http.Request)), Now}`. `Send(ctx, sub, payload, Options{TTL, Urgency, Topic})` POSTs with `Content-Encoding: aes128gcm`, `TTL` (seconds), `Urgency` (`very-low|low|normal|high`), `Topic` (≤32 base64url characters, else refused). 2xx is success. 404 and 410 return `ErrGone` (drop the subscription). Anything else is an error carrying the status and the start of the body.
- **Tests:** the full RFC 8291 Appendix A example (fixed keys and salt → exactly the published header and ciphertext); a round trip through a test-only decryptor with a fresh key; the JWT's signature verifying against the public key and its claims; `Send` against a fake client (headers, decryptable body, 201, 404/410 → `ErrGone`, 400 → error, refused endpoint or topic → no request); key create/reload/corrupt-file; the allowlist table.

## Step 2 in detail: the server side

Built after step 1. **Change from the overview:** the service worker and the manifest move to step 4, next to the browser code that uses them; this step is only the Go side.

- **`internal/web/push.go`, `PushService`** (a concrete type in `Deps.Push`, like `AttachmentStore`; nil when `[web] disable_push`): owns the VAPID keys (`<config dir>/push/vapid.pem`), the subscription store and a `webpush.Sender`. `NewPushService(dir, client webpush.Doer, now)`.
  - **Store:** `<dir>/subscriptions.json` (0600, written to a temp file and renamed, under a mutex), a list of `{subscription, events: {input, done}, label, created}` keyed by endpoint. Re-subscribing the same endpoint replaces its entry. At most 32 entries (a 33rd is a 409): devices, not users, so a small cap bounds the file.
  - **Presence:** in memory, endpoint → window → last heartbeat. `Present(endpoint, window)` is true for 30 s after a heartbeat.
  - **`Deliver(ctx, entry, msg)`:** marshals the message (`{title, body, tag, url, window, kind}`), sends it with the options of its kind, and removes the entry on `webpush.ErrGone`. Step 3's notifier and the test endpoint both use it.
- **Routes** (`handlers_push.go`, all JSON; with `Deps.Push` nil every one is a 404 except `GET /api/push`, which says `{enabled: false}`):
  - `GET /api/push` → `{enabled, publicKey}`.
  - `GET /api/push/subscriptions?endpoint=` → `{subscribed, events, label}` for that endpoint: the dialog's state for this device.
  - `POST /api/push/subscriptions` `{subscription, events, label}`: body capped at 8 KB, `Subscription.Validate` (allowlist, key sizes) → 400 otherwise, label control-stripped and cut to 80 characters. 204.
  - `DELETE /api/push/subscriptions` `{endpoint}` → 204 (also for an unknown endpoint).
  - `POST /api/push/test` `{endpoint}`: sends "Notifications work" to that subscription only. 404 for an unknown endpoint, 410 if the push service says it's gone (and it's removed), 502 with the service's message for other failures.
  - `POST /api/push/presence` `{endpoint, window}`: records a heartbeat (window `""` clears that endpoint's presence). Unknown endpoints are ignored (204), so a page that isn't subscribed can heartbeat harmlessly.
- **Config:** `[web] disable_push` (`WebConfig.DisablePush`). `cmd/mo/web.go` builds the service unless it's set; a key or store that can't be loaded is a startup error, like the attachment store.
- **Tests** (`handlers_push_test.go`, `push_test.go`, a temp dir and a fake `Doer`): subscribe/lookup/replace/unsubscribe, the store's file mode and reload, bad subscriptions refused (unknown host, bad keys, oversized body), the cap, the test push reaching the fake push service and decrypting to the expected JSON, a 410 removing the entry, presence expiring after 30 s with an injected clock, and `disable_push` → 404s.
- **Built as planned,** plus one change to step 1: the test-only decryptor became `webpush.Decrypt`, so the web tests can read what the fake push service received.

## Step 3 in detail: the notifier

- **`internal/web/notifier.go`, pure part: `notifyTracker.observe(rows, now) []notifyEvent`.** Rows without a window id or session id are skipped. A row is keyed by window id + session id, so a new session in the same window starts fresh. Its observation is `(status, sig)`, where `sig` hashes `pending_tool` + `pending_input`.
  - **Priming:** the first observe records every row as confirmed and returns nothing. A key seen for the first time later (a new session) is recorded the same way: a session that starts doesn't notify.
  - **Confirmation:** an observation that differs from the confirmed one becomes a candidate; seen again on the next read it's confirmed, and the transition is judged. A different observation in between restarts the candidate. So a flicker of one read never notifies.
  - **Events:** confirmed into `question` or `permission` from any other status, or the same status with a different `sig` (the next prompt), → `input`. Confirmed `active` → `idle` after at least 15 s of `active` (`doneMinActive`; shorter turns are you watching) → `done`. Nothing else notifies (e.g. `permission` → `idle` is a denial, not done).
  - **Forgetting:** a key missing for 2 minutes is dropped. Missing for less (the TUI rewriting rows during a restart), it comes back with its confirmed state, so a restart doesn't notify.
- **Formatting: `pushMessageFor(event, row) PushMessage`.** The project label is the row's `name`, or `<parent> <name>` for a worktree row (`unky-mo @small-tasks`). Titles and bodies follow the "Notification content" table. The body is read from `pending_input`: AskUserQuestion's first `questions[].question`; Bash's `command`; Edit/MultiEdit/Write/NotebookEdit's file's base name (`Edit notifier.go`); ExitPlanMode → `Approve the plan`; WebFetch → the URL's host; WebSearch → the query; another tool → its name; nothing pending → `Waiting for your answer` / `Waiting for permission`. Whitespace is collapsed and the body cut to 180 characters with `…`. Done's body is `<window name> · <branch>` (the branch from `fillBranches`). Tag `mo-<window id>`, URL `/chat?window=<window id>`.
- **Loop: `Server.RunNotifier(ctx, interval)`,** started by `cmd/mo/web.go` in a goroutine when `Deps.Push` is set, every second. Each tick (`notifyTick`) reads the state file (an error skips the tick), fills branches, observes, and for each event sends to every device that wants that kind (`events.input`/`events.done`) and isn't present on that window, each send in its own goroutine with a 20 s timeout. Failures are logged to stderr (the `mo-web` tmux pane); gone devices are already removed by `Deliver`.
- **Tests:** the tracker over scripted read sequences (priming, the two-read rule, a one-read flicker, input from idle and active, a new prompt in the same status, done after ≥15 s and not before, permission → idle silent, a new session silent, a row missing 30 s vs 3 minutes); the formatter per tool, the worktree label, truncation; `notifyTick` with a fake `StateReader` and the fake push service: only devices wanting the kind get it, a present device is skipped, and the decrypted payload is the formatted message.
- **Built as planned.**

## Step 4 in detail: the browser

- **`static/sw.js`** (served from the root, so its scope is the whole site; no fetch handler, nothing cached):
  - `install` → `skipWaiting`, `activate` → `clients.claim`, so a new `mo` binary's worker takes over at once.
  - `push`: parse the JSON (a malformed one still shows "unky-mo" so iOS never sees a push without a notification) and `showNotification(title, {body, tag, renotify: true, icon, badge, data: {url, window}})`. `renotify` makes a replacing notification buzz again.
  - `notificationclick`: close it; focus a window already on that URL, else navigate an open dashboard window there and focus it, else `openWindow(url)`.
- **Manifest and icons:** `manifest.json` (`name`/`short_name` "Unky Mo", `start_url` "/", `display: standalone`, black theme), PNG icons rendered once from the favicon's design with `rsvg-convert` and committed: `icons/icon-192.png`, `icons/icon-512.png`, `icons/apple-touch-icon.png` (180), and `icons/badge.png` (96, white M on transparent: Android's status-bar badge). `<link rel="manifest">`, `<link rel="apple-touch-icon">` and `<meta name="theme-color">` on the dashboard and chat pages. Plain `.json`, since Go's `mime` table has no `.webmanifest`.
- **`static/notify.js`** (dashboard and chat pages, after `actions.js`; uses its `dialogNode`):
  - **Support:** service worker + `PushManager` + `Notification` + a secure context. An iPhone/iPad Safari tab (no `PushManager` until the page is a Home Screen app) gets the Add to Home Screen steps instead. `GET /api/push` `{enabled: false}` hides the button entirely.
  - It registers `/sw.js` on load when supported, so the subscription and the presence heartbeat work on every visit.
  - **"Notifications" button** (`data-notify-settings`) next to Restart mo in the dashboard header and the chat nav. Its dialog shows this device's state (on / off / blocked in the browser's settings / not supported / Add to Home Screen first), two checkboxes ("A session needs input", on by default; "A session is done, after turns over 15 s", off by default), and Turn on / Save, Turn off, Send test.
  - **Turn on:** `Notification.requestPermission()` (from the click) → `pushManager.subscribe({userVisibleOnly: true, applicationServerKey})`. An existing subscription made with a different key (the server's key was recreated) is unsubscribed first. Then `POST /api/push/subscriptions` with the events and a label from the user agent ("Chrome on Android"). **Turn off:** `DELETE` on the server, then `unsubscribe()`. The dialog's "on" state is the server's lookup for this device's endpoint, so a device the server dropped (gone) reads as off.
  - **Presence** (chat page): every 5 s it checks the shown window (`?window=` in the URL, which the nav's `pushState` updates) and whether the page is visible and focused. It beats (`POST /api/push/presence`) when that changes or 15 s have passed, sends `window: ""` when the page is hidden or blurred, and only while this device has a subscription. While a session shows, its notification (tag `mo-<window>`) is closed on this device.
- **Tests:** no Node test for the DOM/worker code (none of the page scripts have one); the pure helpers (`urlBase64ToBytes`, `deviceLabel`, `sameKey`) live in `static/notify-model.js` and get `jstests/notify_model.test.js`. The end-to-end check is step 5.
- **Built as planned,** plus: the manifest link carries `crossorigin="use-credentials"`, since browsers fetch a manifest without credentials by default and basic auth would refuse it.

## Step 5 in detail: end to end

- `make install` + `mo restart`, then on the Android emulator's Chrome (trusted CA): Notifications → Turn on → Send test, check the notification and that tapping it opens the dashboard. Then a real session: a throwaway Claude session asking an `AskUserQuestion` must notify with the question as the body, and tapping it must open that session's chat. With the chat for that session open and focused on the emulator, the same must not notify.
- Desktop Chrome: the same Turn on / Send test.
- iPhone: only if one is available (Home Screen app, basic auth kept or not). Otherwise listed as unverified.
- CLAUDE.md: the push pieces (`internal/webpush`, `PushService`, the notifier, `sw.js`/`notify.js`, `[web] disable_push`) and their tests in `.claude/rules/testing.md`.
- **Not done yet.** On the emulator, Chrome's dashboard tabs sat at the basic-auth prompt, so Turn on / Send test never ran. Next: try it on a real phone (signed in), then the real-session checks above.
