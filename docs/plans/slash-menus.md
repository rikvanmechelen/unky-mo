# Slash-command menus in the web chat

Goal: the chat view can operate the menus Claude Code opens behind its slash
commands (`/model`, `/config`, `/permissions`, `/mcp`, `/skills`, …), so a
session can be driven from a phone without "opens in terminal".

Today `static/commands.js` tags every `local-jsx` built-in "opens in
terminal", and `/prompt` refuses while a dialog covers the message box
(`checkInputBoxEmpty`, the dialog banner). We already answer three dialog
families natively (permission, AskUserQuestion, folder trust), each with its
own parser and driver. That doesn't scale to ~40 menus that change every
Claude Code release, so this plan handles them **generically**.

Probed against Claude Code **2.1.325** in a 140×50 pane
(`internal/web/testdata/menus/*.txt`, sanitized plain captures; re-probe on
upgrade, like the other fixtures).

## What the menus have in common

Every menu is drawn **below the prompt box's place**: the prompt box (two `─`
rules around `❯`) and the mode footer are gone, replaced by a block that
starts under a `▔▔▔` edge and ends with a **footer legend**:

```
   Select model                                   ← title
   Switch between Claude models. …                ← subtitle (optional)
   ❯ 1.  Default (recommended) ✔  Fable 5.1       ← rows; ❯ = cursor, ✔ = current
     2.  Opus 5.5                 For complex …
     ▸   More models
   ● High effort (default) ←/→ to adjust          ← extra control (optional)
   Enter to set as default · s to use this session only · Esc to cancel   ← legend
```

Six patterns cover everything probed:

| # | Pattern | Seen in | Keys that drive it |
|---|---|---|---|
| A | **Pick one** (radio list, optional numbers, optional preview below) | `/model`, `/theme` (+ diff preview), `/export`, `/rewind`, `/memory` | ↑/↓, Enter, digit (numbered), Esc |
| B | **Slider** | `/effort`, the effort line in `/model` | ←/→, Tab (ultracode), Enter, `s` |
| C | **Searchable / tabbed list** with toggles, sort, drill-in | `/config`, `/permissions`, `/plugin`, `/skills`, `/mcp`, `/resume` | type to search, ↑/↓, Space (toggle), Enter (open), ←/→ (tab), `t` (sort), `s`/ctrl+… |
| D | **Text input** | `/add-dir` (path, Tab completes), search boxes, rename | text, Tab, Enter, Esc |
| E | **Read-only panel** (tabs) | `/status`, `/usage`, `/help`, `/hooks`, `/tasks` | ←/→ tabs, ↑/↓ scroll, Esc |
| F | **No dialog**: prints a `⎿` line | `/context`, `/color red`, `/agents` (removed) | none — already in the transcript as `local-command-stdout` |

Structural features shared by A–E:

- **Cursor** `❯` marks the focused row; **state glyphs** on rows: `✔`/`✘`
  (on/off, current), `◯` (toggle), `⚠` (needs auth), `▸`/`→`/`›` (opens
  more), `●`.
- **Groups**: unindented heading lines between rows (`/mcp`: "User MCPs",
  "claude.ai").
- **Tabs**: a header strip (`Settings  Status  Config  Usage  Stats`), the
  active one marked; `/status`, `/usage` and `/config` are one tabbed
  dialog.
- **Search box**: a `╭ │ ⌕ … │ ╰` frame; typing goes into it (some lists
  need `/` first).
- **Windowing**: long lists show a viewport with `↓ 9 more` or `↓ 10.` as a
  scroll indicator (`/plugin` is 3820 rows — never enumerable).
- **Footer legend**: `key to action · key to action`, split on ` · `. This
  is the one machine-readable statement of which keys do what *on this
  screen*, so the browser can render the legend as buttons without a
  per-command table.

Complications seen:

- **Esc Esc opens `/rewind`** when the box is empty — a client must never
  send two Escapes blindly.
- A command can leave the dialog for something that isn't one: `/memory` →
  Enter opens `$EDITOR` in the pane; `/login` opens a browser on the machine;
  `/statusline` and similar are *prompt* commands that start a model turn.
  Detection must say "this isn't a menu" and fall back.
- Some menus change the session itself: `/resume` swaps the session id
  under the window, `/rewind` rewrites the transcript tree, `/color` and
  `/theme` change state, `/model` and `/effort` offer "this session only".

## Approach: parse the screen, forward semantic keys

Not one driver per command (the `answer.go` style: right for a form we know,
wrong for 40 moving menus). Instead:

1. **`parseMenu(screen) *menuView`** — pure, over the capture (after
   `cropSidePanel`). Finds the block under the `▔` edge, and returns
   `{title, subtitle, tabs[{label, active}], search{text, focused} | nil,
   rows[{text, cols[], cursor, marker, group, depth}], scroll{above, below},
   legend[{key, label}], extra[]string, sig}`. `extra` is whatever it
   doesn't understand (slider line, theme preview), kept verbatim. Returns
   nil when no menu is on screen. `sig` = hash of the normalized block.
2. **`GET /api/sessions/{w}/menu`** — captures Claude's pane, parses, returns
   the view (or `{menu:null}`). Polled while a menu is open (1 s, like
   `/permission`), and by the dialog banner's "Open menu" button.
3. **`POST /api/sessions/{w}/menu/keys`** `{sig, keys:[…]}` — re-reads the
   screen and refuses with 409 (no keys) unless `sig` matches. Keys come
   from a fixed vocabulary (`up down left right enter esc tab space pgup
   pgdn`, a digit, `text`), reusing `ClaudePane.SendKeys` and
   `liveKeyNames` rules (no control characters). Returns the new view, so
   the browser updates without a second round trip. **No two Escapes in one
   request**, and Esc is only sent while the parser still sees a menu.
4. **Browser renderer (`static/menu.js`)** — one modal for all patterns:
   tabs, search input, rows (cursor, marker, columns), legend buttons, and
   an "extra" block in monospace. Clicking a row = move the cursor there
   (↑/↓ × distance, re-read after the burst) and optionally Enter; arrow
   keys, Enter, Esc and typing work from the keyboard. Windowed lists show
   "N more" with PgUp/PgDn and the search box, never a fake full list.
5. **Raw fallback.** If a screen has the legend but fails the structure
   (unknown layout), show the capture verbatim (`capture-pane -e` through
   terminal.js's SGR renderer) with the same key bar. Coverage then doesn't
   depend on the parser being complete; the structured view is the upgrade.

Opening a menu: the composer sends `/model` through `/prompt` as today; the
dialog banner's current "answer it in the terminal" becomes "Open menu"
whenever `parseMenu` succeeds. The "opens in terminal" tag in the slash list
becomes "opens menu" (or stays for the excluded ones below).

Closing the loop: when the menu closes, Claude prints `⎿ Set model to …`
(pattern F), which the transcript already shows.

### Deliberately **not** done generically

- Commands with arguments don't need the menu: `/model opus`, `/effort high`,
  `/color red`, `/config key=value`, `/add-dir <path>`. An optional later
  step can build **argument forms** for the three or four most used
  (model, effort, theme) that just send the command — more reliable than
  driving the dialog, but one table entry each.
- **Excluded** (shown "opens in terminal"): `/login`, `/logout`, `/ide`,
  `/feedback` (external flows), `/resume` (the dashboard's own resume
  swaps windows and state correctly — use that), anything that hands the
  pane to `$EDITOR`. `/rewind` is allowed but its confirm step needs the
  explicit-click treatment trust did.
- The web is a full TUI stand-in (see the permission/lift/cleanup
  decisions), so destructive menu actions are **not** blocked; the `sig`
  guard against acting on a stale screen is the safety net.

## Risks

- **Layout drift** each Claude Code release (the same exposure as the other
  parsers). Mitigation: fixtures per version, raw fallback, a `mo` test
  that fails loudly when a capture stops parsing.
- **Concurrent terminal user**: both can type. `sig` + re-read per request
  handles it; polling shows the other person's moves.
- **Latency of row clicks** (N key presses). Send the burst in one
  `send-keys` call; re-read once.
- **Hover/preview content** (`/theme`'s diff, `/context` grid) is just
  `extra` text; colour is lost in the structured view (acceptable; raw view
  keeps it).

## Steps

Per the repo habit, each step gets a "Step N in detail" section here
**before** it's built, and is committed on its own.

1. **Menu parser + fixtures.** `internal/web/menu.go`: `parseMenu`, pure,
   tested over `testdata/menus/` (title, tabs, search, rows with
   cursor/marker/group, windowing, legend, nil for an idle screen, a
   permission dialog and the trust dialog — those keep their own
   parsers). Add `capture-pane -e` fixtures if highlight turns out to be
   the only cursor signal. No server or UI change.
2. **Read-only endpoint + raw view.** `GET /menu` (state row → pane target,
   `cropSidePanel`, ETag = sig), `Menus` interface in `deps.go` if a new
   seam is needed (else `ClaudePane.Capture`). Browser: dialog banner gets
   "Open menu", showing the parsed view read-only (+ raw fallback). Useful
   on its own for the read-only panels (E).
3. **Key forwarding.** `POST /menu/keys` with `sig` guard, key vocabulary,
   Esc rules, tests with a `fakeMenu` pane that redraws like the probes.
   Browser: keyboard + legend buttons + clickable rows for patterns A, B,
   D; typed search for C.
4. **Patterns C and windowing.** Toggles (Space), tabs, sort, drill-in as
   legend/keys; "N more", PgUp/PgDn; search; selected-row scrolling.
5. **Wire-up.** Slash list: "opens menu" vs "opens in terminal"
   (excluded set in `claude.builtinCommands`, e.g. `Menu bool`); composer
   `/model` etc. open the modal automatically when `/menu` answers; phone
   layout; notifier: a menu left open doesn't notify.
6. **Optional: argument forms** for `/model`, `/effort`, `/theme` (send the
   command text; the menu stays for everything else).
7. **Docs/tests**: CLAUDE.md paragraph, `.claude/rules/testing.md` entry,
   re-probe notes (version in the fixture header).

## Open questions

- Should a menu be a modal over the chat or a tab in the editor strip (it
  can stay open while you read the transcript)? Leaning modal: the pane is
  blocked while it's open, so nothing else can happen.
- Are there menus worth native treatment beyond the generic one
  (`/permissions` rules editor, `/mcp` status, `/skills` toggles)? Decide
  after step 4 with the generic version in hand.
