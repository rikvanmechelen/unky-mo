# mo web as an installed app that finds its machine

**Status:** steps 1–3 built on main, 2026-11-14.

## Goal

Install mo web once on the phone (and desktop) and have it work wherever I am. Today the address is the problem: the laptop is `192.168.1.20` at home, something else at the office, and the dashboard is bookmarked by IP. I want to:

- **Detect** which known address of a machine answers right now, and go there without typing.
- **Add** a new address by hand when none of the known ones answer.
- **Not type the password** on every launch, every address and every machine. All machines use the same login.
- Keep notifications working when the address changes. Today a notification tap opens the origin that subscribed, which is dead once the IP changed.

Out of scope: reaching a machine from outside its network (cellular). That's a VPN's job; see "Stable names".

## The constraint that shapes everything: an app is an origin

A PWA belongs to an **origin**: scheme + host + port. `https://192.168.1.20:7890` and `https://10.1.4.7:7890` are two unrelated apps to the browser, even when they're the same laptop. Per origin, separately:

- the install (home screen icon, manifest, standalone window),
- the service worker and its cache,
- localStorage / IndexedDB (drafts, editor tabs, rails, review ticks: everything mo web keeps in the browser),
- the push subscription,
- the basic-auth login the browser remembers.

So "the app switches IP" can't mean "the same page moves to another IP". There are three ways around it, and they combine:

1. **A stable name** for each machine, so the origin never changes. The cert already covers `<hostname>.local` (`TLSHosts`). Installing from `https://laptop.local:7890` fixes everything here **if** mDNS works on every network I use. Home networks: usually. Office Wi-Fi: often not (client isolation, multicast filtered). A VPN with its own DNS (Tailscale MagicDNS: `laptop.<tailnet>.ts.net` on a fixed `100.x` address, reachable from cellular too) fixes it everywhere, and needs only that the cert covers its name.
2. **Jump between origins.** The installed app, opened on a dead address, finds a live one and navigates there. Cheap, but in a standalone window an out-of-scope navigation opens the browser's in-app tab (Android: a Custom Tab with a URL bar; iOS: a Safari sheet), and the new origin has none of the old one's storage or push.
3. **A shell that stays put.** The installed app's own origin (wherever it was installed from) keeps serving its pages from the service worker cache, even when that address is unreachable. Those pages are a small shell that finds the machine and shows mo web **in an iframe** from the address that answers. The app never leaves its origin, so the icon, the window, the push subscription and the stored login stay one thing. This is the only option that delivers "one app" without a stable name, and it costs the most.

**Recommendation:** build it in phases, each useful alone, and stop as soon as it's good enough.

- **Phase A (steps 1–3): the server knows who it is, its cert follows the network, the app opens to a launcher that finds the machine, and logging in is a form with a per-device token instead of the basic-auth prompt.** Option 2 plus groundwork for 3. Fixes the immediate pain (finding the IP, the password prompts, the iOS Home Screen basic-auth risk and the emulator being stuck on the prompt in `notifications.md` step 5).
- **Phase B (steps 4–6): the shell + iframe, the shell's updates from whichever machine answers, and push through it.** Option 3. Only after the probes below come back fine, and only if phase A plus a stable name still leaves the "several apps" problem in practice.
- Whatever the phases: also do option 1 (`[web] extra_hosts`), it's a few lines.

## Credentials: what the app may store

Same login on every machine makes it tempting to keep the password in the app. Don't store the password; store **per-device tokens**:

- Pairing: the app sends username + password once to a machine (`POST /api/hub/pair`), the machine answers with a random 256-bit token for this device. The app keeps the token, never the password.
- The machine stores only `sha256(token)`, with a label ("Pixel 9 — Chrome"), created and last-used times, in `<config dir>/web-devices.json` (0600, like `web-auth.toml`). `mo web devices list|revoke` and a "Devices" dialog in the dashboard revoke one device on one machine without touching the others or the password.
- Tokens expire after 90 days unused. A token is full access, like the password: the web is a full TUI stand-in.
- Why not the password: the app's storage is readable by any script that ever runs in its origin (an XSS in the transcript renderer is the realistic path, and the transcript is untrusted text). A stolen password opens **every** machine, and anything else that shares it, until it's changed everywhere. A stolen token opens one machine until it's revoked or expires.
- Cost: the password is typed once per machine per device. With two or three machines that's a handful of times, ever.
- **Opt-in "remember password to pair my other machines"** is possible in phase B only (in phase A each origin is its own storage, so there's nothing to share). If wanted: an AES-GCM key generated with WebCrypto as **non-extractable**, kept in IndexedDB, wrapping the password. That stops someone copying the storage off the device (a backup, a profile copy), not a script running in the origin, since that script can ask the key to decrypt. Default off, and the dialog says exactly that. Open question below.
- The Credential Management API (`PasswordCredential`) would hand the password to the browser's password manager instead, which is better, but it's Chrome-only and doesn't help on iOS. Not worth a second path.

## Detecting which address answers

What the browser can and can't know:

- It can't see the network (no SSID, no subnet; WebRTC hides local IPs behind mDNS names). So no guessing a `/24`, and no scanning: slow, noisy, and nothing to scan with.
- It can `fetch` a candidate with a short timeout. A failure looks the same whether the host is down, the port is closed, or **the cert isn't trusted on this device**. The launcher says "not answering (or its certificate isn't trusted here)" and offers "Open in browser" so the real error shows.
- Another device can answer on the same address at another location (the office's `192.168.1.20` isn't my laptop). So every probe checks a **machine id**, and nothing is sent to an address (no token, no password) before its `hello` names the expected machine over HTTPS. The local CA protects this: a stranger can't present a cert this phone trusts.

The probe:

- `GET /api/hub/hello` (public, `Access-Control-Allow-Origin: *`, no credentials): `{app: "unky-mo", machine: <id>, name: <hostname>, version}`. Minimal on purpose: it's readable by anyone on the network (the hostname already is, over mDNS).
- `GET /api/hub/addrs` (authenticated): the URLs the machine can be reached at right now (`TLSHosts` + port, plus `extra_hosts`), so the app learns new addresses while connected.
- Candidates per machine: `{url, source: reported | manual | learned, lastOk, lastFail}`. Order: last address that worked, then the stable names (`.local`, `extra_hosts`), then reported IPs, then the rest.
- Race: the last good address gets a 600 ms head start, then all candidates in parallel, each with a 2.5 s timeout (`AbortController`). First matching `hello` wins. Re-probe on the `online` event, when the app comes back to the foreground, and when the page loses its connection (phase B: the frame reports it).
- Manual add: host or IP, optional port (default 7890), https only. It's probed at once and kept only if it answers as a machine (a new machine is added, a known one gains the address).

## Probes before building (phase B gates on these)

Run on Android Chrome (emulator + a real phone), iOS Safari as a Home Screen app (if a device is available), desktop Chrome:

1. **Install from a private IP / `.local` name with the local CA.** Does Android Chrome mint a real WebAPK (no Chrome badge), or fall back to a shortcut? Does the manifest `id` keep one install when `start_url` changes?
2. **Offline start.** An installed app whose origin is unreachable, with a fetch handler serving `/hub` from cache: does it open (Android, iOS, desktop), or does the browser show its own offline page first?
3. **iOS Home Screen + basic auth** (from `notifications.md` step 5): is the login kept? If not, step 3 is required, not optional.
4. **Cross-origin iframe of a private address from a private origin.** Chrome's Local Network Access permission applies to requests from a *less* private context; private → private shouldn't prompt. Check it doesn't, and what `allow=` the iframe needs if it does.
5. **Storage and service workers inside the iframe** are partitioned by the top-level site. Check the frame's localStorage works and survives restarts (Safari: partitioned storage in a Home Screen app's iframe).
6. **Third-party cookies in the iframe:** Safari blocks them, partitioned or not (to confirm); Chrome allows `Partitioned` (CHIPS). This decides whether the frame can use a cookie or must send a header (the plan assumes the header).
7. **Push from several registrations** in one origin, each subscribed with a different VAPID key (one per machine), on Android and iOS.
8. **A core written with `cache.put` from cross-origin bytes** is served by the service worker after a cold start with the install origin unreachable (Android, iOS Home Screen, desktop), and survives a few days of not opening the app.
9. **Notification vibration on Android:** whether `vibrate: [...]` has any effect (Android 8+ fixes vibration per notification channel, and Chrome has one channel per origin, so the pattern is most likely ignored), and that both a "done" and a "needs you" notification buzz with the channel's setting (a replacing notification too, via `renotify`).

## Steps

### Phase A

1. **Server identity, addresses, a cert that follows the network.**
   - `machine-id`: 128 random bits, hex, created once in `$XDG_STATE_HOME/unky-mo/` (not the config dir: dotfiles sync would give two machines the same id).
   - `GET /api/hub/hello` (public, CORS `*`, `Cache-Control: no-store`) and `GET /api/hub/addrs` (authenticated).
   - `[web] extra_hosts = ["laptop.tail1234.ts.net", …]`: added to `TLSHosts`' list, so a VPN or DNS name gets into the cert and into `addrs`.
   - **The cert follows the network.** Today the leaf is signed at startup for the interfaces of that moment and served from a fixed `tls.Config.Certificates`, so a laptop that moves from home to office without restarting mo has a cert without its new IP: a TLS error by IP. Serve it through `GetCertificate` from an `atomic.Pointer`, and every 30 s compare `interfaceIPv4s()` with the leaf's IPs; on a difference, `EnsureTLS` re-signs (the CA is on disk and never changes, so every trusting device keeps trusting) and the pointer swaps. The HTTP→HTTPS redirect's host check reads the same pointer.
   - Tests: `hello` shape and CORS, `addrs` behind auth, `extra_hosts` in the cert, the swap on a changed interface list (injected interface lister and clock), the redirect accepting a host only after the swap covers it.

2. **The offline launcher.**
   - **Public paths.** `BasicAuth` (and later the token check) lets through, unauthenticated, only: `/manifest.json`, `/sw.js`, `/icons/*`, `/favicon/*`, `/hub` and its own script/style, `/api/hub/hello`. Static code, no data. Everything else stays behind auth.
   - **Manifest:** `id: "/hub"`, `start_url: "/hub"`, `scope: "/"`. Drop `crossorigin="use-credentials"` once the manifest is public.
   - **Service worker fetch handler** (today's `sw.js` deliberately has none): `/hub` and the static files it needs are cache-first with a background revalidate (the server's content-hash ETags make that cheap). A navigation that fails (offline / unreachable) gets the cached `/hub`. `/api/*` and SSE are never cached: the dashboard stays live. The cache is versioned by a hash of the static file list (`GET /api/hub/assets`, public), so an update can't mix versions.
   - **`/hub`** (`static/hub.html`, `hub.js`, pure logic in `hub-model.js`, Node-tested): the machines this app knows (name, the address that answered, status square), "Add address", and per machine "Open". Opening it on a reachable origin that is the last-used machine goes straight to the dashboard (`/`) unless "stay on launcher" was the reason it opened. On an unreachable origin: probe, show what answers, and **jump** (top-level navigation) to the dashboard at the address found.
   - Storage: IndexedDB `mo-hub` (machines, candidates, last used). Per origin in this phase, so each origin learns on its own; `addrs` fills it in quickly.
   - Tests (`jstests/hub_model.test.js`): candidate ordering, merging `addrs` into known machines, the race with fake fetch/timers (head start, timeouts, a wrong machine id ignored, the first match wins), URL validation for manual entry (https only, no path/user info, port range).

3. **Login with device tokens.**
   - `POST /api/hub/pair` `{username, password, label}`: verify (the PBKDF2 cost is the brake), rate-limited to 5 failures per minute per client IP; answers `{token, deviceId}` and, for a first-party request, also sets the token as a cookie `mo_device` (`HttpOnly; Secure; SameSite=Lax; Path=/; Max-Age` 90 days).
   - The auth middleware accepts, in order: the `mo_device` cookie, `Authorization: Bearer <token>`, basic auth (curl and existing setups keep working). A browser navigation (`Accept: text/html`) with none gets the login page with 401 and **no** `WWW-Authenticate` header, so there's no native prompt. API calls keep the 401 with the basic challenge only when the request didn't come from our page (no `X-Mo-Client` header), so `curl -u` still works.
   - `web-devices.json`: `{id, hash, label, created, lastUsed}`, `lastUsed` written at most hourly. Lookup by hash in a map; comparison constant-time.
   - `mo web devices list|revoke <id>`; a "Devices" dialog in the dashboard header listing them, marking "this device", with Revoke. "Log out" revokes this device's token and clears the cookie.
   - `mo web` without a login still refuses a non-loopback address. Tokens exist only when a login does.
   - Tests: pair success/failure, the rate limit, each credential kind, expiry, revocation taking effect at once, the HTML-vs-API 401 split, the public paths reachable without credentials and nothing else.

### Phase B (after the probes)

4. **The shell: one app, the machine in an iframe.**
   - `/hub` becomes the app's home for good: after finding the machine it shows `https://<found>/?embed=1` (or `/chat?window=…&embed=1`) in a full-window iframe instead of navigating. The shell's origin is wherever the app was installed from, and its pages come from its service worker, so it doesn't matter whether that address answers today.
   - **Token handoff:** the frame posts `{type: "mo-ready"}` to its parent; the shell checks `event.origin` is the address it opened and answers `{type: "mo-auth", token}` with that exact `targetOrigin`. The frame keeps the token in sessionStorage (partitioned under the shell, which is what we want).
   - **Requests from the frame carry the token as a header.** Assumes probe 6 (no third-party cookies on Safari). `common.js` wraps `fetch` once to add `Authorization: Bearer` in embed mode. What can't send headers: `EventSource` (transcript, subagents) and URL loads (`<img>` thumbnails and tool images, attachment previews, `/ca.crt`, links opening `/branch` in a new tab). These get a short-lived signed **URL ticket** (`POST /api/hub/ticket` → HMAC over device id + expiry, 10 min, accepted only on GETs of an allowlist of routes), appended by one helper `authURL(u)`. The step starts with an inventory of every URL the static files build (`grep` for `fetch(`, `EventSource`, `.src =`, `href`, `window.open`).
   - **Framing is restricted:** `Content-Security-Policy: frame-ancestors 'self' <origins>` on every page, where `<origins>` are the shell origins devices paired from (recorded from the pair request's `Origin`). Without it, any page could frame the destructive UI.
   - **Frame → shell messages** carry nothing sensitive: the title, the most urgent status (for the shell's favicon/badge), "connection lost" (re-probe), "switch machine". Sent to the parent origin the auth message came from, never `*`.
   - A machine not yet paired from this shell: the shell asks for the password once, pairs, keeps the token (and the password too only with the opt-in above).
   - The frame's own push UI hides in embed mode and asks the shell instead (step 6).
   - Tests: the handoff model (wrong origin ignored, wrong source ignored), `authURL`, the ticket (expiry, route allowlist, POST refused, another device's ticket after revoke), the CSP header, the fetch wrapper only in embed mode.

5. **The core updates from whichever machine answers.**
   The app has two layers with different update rules:

   | Layer | Updated by |
   |---|---|
   | The service worker script (`sw.js`) and the manifest (name, icons) | Only the install origin. The browser re-checks them on launch; when that address doesn't answer, the check fails silently and the old ones stay. |
   | The **core** (the shell's HTML/JS/CSS in Cache Storage) | Any page in the app's origin, with bytes fetched from any machine. |

   - **The service worker stays dumb so it rarely needs an update.** It answers navigations from the cache the "current core" pointer names (a small record in Cache Storage), and otherwise serves a recovery page written inline in the script. All logic lives in the core. The pointer format, cache names and the recovery page are the **SW contract**; changing any of them is a new `SW_VERSION`.
   - **Fetching a core:** after connecting, the core asks the machine `GET /api/hub/core` (CORS, token-authenticated): `{version, swVersion, manifestVersion, protocol, files: {path: {etag, type}}}`. If `version` is newer than the running core, it downloads the files into a new cache `core-<version>` (`cache.put` under the app's own URLs), then moves the pointer. It takes effect on the next launch, or now with a "Reload" toast. Rules:
     - Only from a machine whose `hello` matched its id, over HTTPS, with a valid token from this device: a machine already trusted to drive agents. Never from a machine merely answering.
     - Only a newer version. Never downgrade.
     - (Later, if wanted: an Ed25519 signature over the bundle, verified with WebCrypto against a key in the core.)
   - **Rollback:** the previous core is kept. A new core must post "booted" to the service worker within 5 s, or the service worker moves the pointer back and marks that version bad (not retried).
   - **Recovery page** (inline in `sw.js`, so it exists even when every cache is broken): "Use the core this app was installed with" (a copy kept in `core-installed`) and "Fetch the core from address …".
   - **The "base is outdated" warning.** `sw.js` carries `SW_VERSION` and the manifest a `manifestVersion` (in its `id`-stable fields, e.g. a `version` key the core reads). The machine reports the ones it ships. If either is newer than what's installed, the core shows a dismissible strip: "This app's base is older than <machine>'s (v3 → v5). Open it once from <install address>, or reinstall from <current address>." Before showing it, the core calls `registration.update()` whenever the install origin itself answers a probe, so the warning only appears when an automatic update really can't happen. Reinstalling from another address makes that address the new install origin (a fresh app: paired tokens and stored machines start over, which the strip says).
   - **What then updates without reinstalling:** everything inside the iframe (served live by the machine, as today), and the core (above). **What doesn't:** the service worker contract, and the app's name/icons/shortcuts (iOS never refreshes those after install; Android only from the install origin).
   - **Compatibility across versions.** Machines won't all run the same build. `protocol` (the hello/pair/core/ticket API and the postMessage messages) is versioned. A core works with machines one protocol version older and newer than its own, and a machine accepts cores one version older. A machine too old for the core gets "update mo on <machine>" instead of a broken frame.
   - Tests: the version comparison and update decision in `hub-model.js` (newer only, bad versions skipped, untrusted machine refused, warning conditions), the service worker's routing and rollback as pure functions over a fake cache (Node), `/api/hub/core`'s listing matching the embedded files.

6. **Push through the shell.**
   - Each machine has its own VAPID key, and a push subscription is bound to one key. The shell registers one service worker **per machine**, under its own scope (`/hub/push/<machine-id>/`), subscribes it with that machine's key (`GET /api/push` from the machine) and gives the subscription to that machine (`POST /api/push/subscriptions`, as today).
   - The push itself goes machine → FCM/Apple → phone, so it works whatever the machine's LAN address is. A notification's URL now names the machine and window (`/hub?machine=<id>&window=@3`), and a tap opens the shell, which finds the machine's current address and opens that session.
   - Presence (don't buzz the device looking at the session) moves to the frame unchanged; it talks to its own machine.
   - (Alternative: one shared VAPID key copied between machines, `mo web push export|import`, and one subscription. Simpler in the browser, but a key copied around is a key that leaks. Per-machine registrations first.)
   - **Telling "done" from "needs you" by feel** (Android; iOS has no web vibration at all). A per-notification `vibrate` pattern is most likely ignored on Android 8+ (probe 9): vibration belongs to the notification channel, and Chrome has one channel per origin, configurable under Android's app notification settings for Chrome. So one pattern for both events, set by hand. **Decision: both "done" and "needs you" always buzz** (never `silent`), so a buzz always means "look at the phone"; the title tells them apart. If probe 9 shows patterns do work, add two patterns on top (short for done, long-long for needs you); either way both buzz. `navigator.vibrate()` from an open page needs a recent tap and doesn't run in a service worker, so it can't help here.
   - If distinct patterns turn out to matter a lot: a native app with its own channels (e.g. ntfy, which mo web would publish to) can have one channel per event, each with its own vibration. Out of scope here, as in `notifications.md`.
   - Tests: the registration bookkeeping in `hub-model.js`, the notification URL builder on the server, the click routing, no event's payload ever `silent`.

7. **End to end and docs.** The probe devices again, through the real flows: install, change network (home ↔ office, or two emulator networks), app opens and finds the machine, add an address by hand, pair, revoke, (phase B) a notification after an address change opens the right session. CLAUDE.md: architecture paragraph, public paths, devices, `extra_hosts`.

## Step 1 in detail: identity, addresses, a cert that follows the network

- **`config.DefaultStateDir()`**: `$XDG_STATE_HOME/unky-mo`, else `~/.local/state/unky-mo`. **`[web] extra_hosts`** (`WebConfig.ExtraHosts []string`).
- **`web.MachineID(dir)`** (`hub.go`): reads `<dir>/machine-id` (32 hex characters), or creates it (dir 0700, file 0600, temp + rename). A file that exists but isn't 32 hex characters is an error, never replaced: a new id makes every device see a new machine.
- **`TLSHosts(listenAddr, extra)`**: `extra` names are lowercased and appended after the interface IPs. Only IPs and plain DNS names (`[a-z0-9-]` labels, no wildcard) are kept; anything else is dropped, so a typo can't break the cert.
- **`HubAddrs(scheme, listenAddr, hosts)`** (pure): the URLs a device can reach this machine at. Loopback names (`localhost`, `127.0.0.1`, `::1`) are left out, IPv6 is bracketed, the listen port is used. A listen address with an explicit host (not `0.0.0.0`/`::`/empty) gives only that host.
- **`Deps.Hub *HubInfo`** `{Machine, Name, Version string; Addrs func() []string}`; nil → the routes are 404.
  - `GET /api/hub/hello`: `{app: "unky-mo", machine, name, version, protocol: 1}`, `Access-Control-Allow-Origin: *`, `Cache-Control: no-store`. A simple GET with no custom headers, so no preflight is needed. (Made public in step 2; until then it sits behind basic auth like everything else.)
  - `GET /api/hub/addrs`: `{addrs: [...]}`, `no-store`. Stays authenticated.
- **`CertSource`** (`certsource.go`): built from the TLS dir, a `hosts func() []string` and a clock. `NewCertSource` runs `EnsureTLS` once. `GetCertificate` returns the current cert from an `atomic.Pointer`; `Leaf()` the current leaf. `Refresh()` re-runs `EnsureTLS` (which reuses the leaf when it still covers every host) when the host set differs from the last one or the leaf is within `leafRenewBefore` of expiring, and swaps the pointer. `Run(ctx, every)` calls `Refresh` every 30 s. Only for the generated cert: `cert_file` stays a fixed certificate.
- `cmd/mo/web.go` serves `TLSConfig.GetCertificate` from the `CertSource`, and the redirect's host check reads `CertSource.Leaf()`.
- Tests: `MachineID` create/reuse/corrupt/permissions; `tlsHosts` with extra names (valid, invalid dropped, duplicates); `HubAddrs` table; `hello` (shape, CORS, no-store) and `addrs`, both 404 without `Hub`; `CertSource`: a changed host list swaps to a leaf covering the new IP with the same CA, an unchanged one keeps the same cert, a leaf near expiry is renewed, `GetCertificate` returns the swapped cert.

## Step 2 in detail: the offline launcher

**Change from the overview:** the service worker contract of step 5 (dumb worker, core pointer, rollback, recovery page) is built here, so step 5 adds only where a core comes from and doesn't bump `SW_VERSION`. The core is fetched as one JSON document rather than a file listing, and the launcher's storage is localStorage (`mo.hub`) rather than IndexedDB: it's small, synchronous and easy to test.

- **The core** is the launcher's own files, all served under `/hub` (`coreFiles` in `hub_core.go`) so a cached core never shadows a live dashboard file and the worker can tell its requests apart without reading the cache: `/hub` (`hub.html`), `/hub/hub.css` (its own small stylesheet with the theme tokens it uses, since `style.css` is 150 KB), `/hub/hub.js`, `/hub/hub-model.js`, `/hub/theme.js`, `/hub/idle.svg`. (Built this way; the first draft served them at their dashboard paths.) Its release is `CORE_RELEASE` in `hub-model.js`, an integer; the server reads it from the embedded file.
  - `GET /api/hub/core` (public, CORS `*`, ETag): `{release, hash, sw, manifest, protocol, entry: "/hub", files: {path: {type, body}}}`. `sw` is `SW_VERSION` read from `sw.js`, `manifest` is `mo_version` read from `manifest.json`. `hash` is over the files.
  - A Go test keeps a table of release → hash and fails when the core's files changed without a new `CORE_RELEASE` (or the table wasn't updated), so "only newer" can be decided by comparing integers.
- **Public paths** (no login): `GET`/`HEAD` of `/manifest.json`, `/sw.js`, `/icons/*`, `/favicon/*`, the core's files, `/api/hub/hello`, `/api/hub/core`. `BasicAuth` lets exactly these through (`isPublicPath`, table-tested both ways). Everything else, including `/api/hub/addrs`, stays behind the login.
- **Manifest:** `id: "/"` (the identity existing installs already have, since it defaulted to `start_url`), `start_url: "/hub"`, `mo_version: 1`. The pages drop `crossorigin="use-credentials"` on the manifest link.
- **The service worker** (`sw.js`, `SW_VERSION = 1`). Pure functions over an injected `caches` and `fetch`, exported for Node, with the event listeners only attached inside a worker:
  - Storage: cache `mo-meta` holds the pointer at `/__mo/pointer`: `{current, previous, installed, tries, confirmed, bad: [releases]}`, where each core entry is `{release, entry, files: [paths]}`. Cache `mo-core-<release>` holds that core's files under their paths.
  - `/__mo/*` is the worker's own API, answered without the network: `GET /__mo/state` (`{sw, pointer}`), `POST /__mo/install` (body: a `/api/hub/core` document; refused if malformed, not newer than the current core, or marked bad), `POST /__mo/booted` (`{release}`: confirms the current core), `POST /__mo/reset` (back to the installed core). A page not controlled by the worker gets the server's 404 and knows there's no worker.
  - A `GET` of one of the current core's paths is answered from its cache. A core may only hold paths under `/hub`. The worker's fetch listener only takes navigations, `/__mo/*` and `GET /hub…`; the dashboard's polls and streams never pass through it.
  - A navigation to anything else goes to the network; if that fails or takes over 5 s, the answer is the core's entry page (the launcher reads `location` to know what was asked for). With no core at all, an inline recovery page: an address field and "Reset launcher".
  - **Rollback:** serving the entry of an unconfirmed core counts a try. After 3 tries without `booted` the worker marks that release bad and goes back to the previous core.
  - `install`: fetch the own origin's `/api/hub/core` and install it as `installed` (best effort). `activate`: claim clients, delete `mo-core-*` caches that are not current, previous or installed. Push and notification clicks are unchanged.
- **The launcher** (`hub.js` over `hub-model.js`):
  - `mo.hub` in localStorage: `{machines: {id: {name, candidates: [{url, source, lastOk, lastFail}]}}, last}`. Sources: `learned` (it answered), `reported` (`/api/hub/addrs`), `manual`.
  - On load it posts `booted`, asks for persistent storage, and probes its own origin. If that answers: learn it, read `addrs` (with the page's login), check for a newer core at `/api/hub/core` (install it, then "Launcher updated: Reload"), then go to the asked-for page (`/` for `/hub`) unless `?stay`.
  - If its own origin doesn't answer: probe every known machine (`findMachine`: last good address first with a 600 ms head start, then all, 2.5 s timeout each, first `hello` with the right machine id wins). If the last-used machine answers, go to the same page there; otherwise show the list.
  - The page lists machines (name, address that answered, status), and has "Add address" (`normalizeAddress`: https only except localhost, default port 7890, no path or user info; probed at once, kept only if a machine answers).
  - The dashboard header gets a "Machines" link to `/hub?stay=1`. Every page registers the worker (not only when push is supported).
- Tests: Go (`isPublicPath` table; `/api/hub/core` shape, release/sw/manifest read from the files, public through `BasicAuth`; the release table guard). Node (`jstests/hub_model.test.js`: address normalization, learning and merging, candidate order, the probe race with fake fetch and timers, the auto-forward decision; `jstests/sw.test.js`: install/refuse, routing core paths, navigation fallback on failure and timeout, recovery page, tries and rollback, booted, reset, cache cleanup).

## Step 3 in detail: login with device tokens

- **`DeviceStore`** (`devices.go`), file `<config dir>/web-devices.json` (0600, temp + rename, under a mutex): `[{id, hash, label, origin, created, lastUsed}]`. `id` is 8 random bytes in hex, the token 32 random bytes base64url, `hash` its SHA-256 in hex. `origin` is the pairing request's `Origin` (for step 4's framing rule).
  - `Pair(label, origin)` → token + device; at most 64 devices (a 65th is refused).
  - `Lookup(token)` → the device, unless it's unknown or unused for 90 days (expired ones are dropped). `lastUsed` is written at most hourly.
  - `List()`, `Revoke(id)`.
  - The file is re-read when its size or mtime changes, so `mo web devices revoke` takes effect in a running server at once.
- **`RequireLogin(h, creds, devices)`** replaces `BasicAuth` in `mo web` (`BasicAuth` stays, as `RequireLogin` without devices). In order:
  1. Public paths (`isPublicPath`), and `POST /api/hub/pair`, which checks the password itself.
  2. The `mo_device` cookie, then `Authorization: Bearer`, then basic auth. The device found is put in the request context.
  3. Otherwise: a browser navigation (`Sec-Fetch-Mode: navigate`) is redirected (303) to `/hub?login=1&next=<path>`, so there's no native prompt. A request with `Sec-Fetch-Mode` (the dashboard's own fetches) gets a plain 401. Anything else (curl, old browsers without fetch metadata) gets the 401 with the basic challenge as today, so `curl -u` and existing logins keep working.
- **Endpoints** (`handlers_devices.go`; all 404 without a login configured, since tokens exist only alongside one):
  - `POST /api/hub/pair` `{username, password, label}` → `{token, device: {id, label}}` and the cookie `mo_device` (`HttpOnly; SameSite=Lax; Path=/; Max-Age` 90 days; `Secure` over TLS). Wrong login → 401. More than 5 failures a minute from one client IP → 429 before the password is checked. Same-origin only in this step (a cross-origin `Origin` is refused with 403); step 4 opens it for the shell.
  - `GET /api/hub/whoami` → `{device: {id, label} | null}`: the launcher's "am I logged in here" check.
  - `GET /api/hub/devices` → the list, `current` marking this device. `DELETE /api/hub/devices/{id}` revokes one. `POST /api/hub/logout` revokes this device and clears the cookie.
  - A cookie that's still used gets re-set (new `Max-Age`) when `lastUsed` is written, so an active device stays logged in.
- **CLI:** `mo web devices list` and `mo web devices revoke <id>`.
- **Launcher:** when its own origin answers but `whoami` is a 401 (or `?login`), it shows a login form (username, password, a device name defaulting to `deviceLabel`'s guess), posts `pair`, then opens `next`. The password is never stored. (A core change: the launcher hasn't left this machine yet, so release 1's recorded hash is updated rather than raising the release.)
- **Dashboard:** a "Devices" button next to "Machines" opens a dialog: this machine's paired devices with label, last used, "this device", Revoke, and "Log out" for this one.
- Tests: `DeviceStore` (pair/lookup, hash only on disk, mode 0600, expiry, hourly `lastUsed`, revoke, re-read after an outside change, the cap); `RequireLogin` (each credential kind, a revoked or expired token refused at once, the navigation/fetch/curl split, public paths); the endpoints (pair sets the cookie and stores only the hash, wrong password, the rate limit, a cross-origin pair refused, no login → 404, whoami, list/revoke/logout).

## Decisions

- **No public host for the shell.** A shell on GitHub Pages would be one stable origin, but code served from a third party would hold tokens that drive agents on my machines, and every request would be public → private (Local Network Access prompts). The shell is served by mo web itself and cached.
- **No password in the app** by default. Tokens per device per machine.
- **Machine id checked before anything is sent** to an address.
- **Static code becomes public, data never.** The list of public paths is fixed in code and tested.
- **The cert keeps its CA forever,** as today. Only the leaf follows the network.
- **Phase A works without phase B,** and phase B only starts if the probes pass.
- **The service worker is a stable contract, the core is not.** The core updates from any paired machine; only a contract change asks for a reinstall, and the app says so.

## Open questions

- **Is a VPN (Tailscale or similar) allowed on the machines and phone?** If so, `extra_hosts` plus installing from its name may make phase B unnecessary, and it also works off the LAN.
- **Does mDNS (`.local`) resolve on the office network?** Quick check from the phone: `https://<hostname>.local:7890/api/hub/hello` after step 1.
- **Remember the password to pair new machines** (phase B, opt-in, with the stated limits): wanted, or is typing it once per machine fine?
