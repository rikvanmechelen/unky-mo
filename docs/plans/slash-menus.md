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

## Step 1 in detail (built)

`internal/web/menu.go` — `parseMenu(screen) *menuView`, pure; tests in
`menu_test.go` over `testdata/menus/` (`.txt` plain, `.ansi` with SGR).

What the fixtures taught beyond the survey:

- A tab strip's **first label is the dialog's title** (`Settings  Status
  Config …`, `Permissions  Allow  Ask …`), the rest are the tabs. Which one
  is active is only in the colours: reverse video (`ESC[7m`) or a
  background (`ESC[48;5;…m`), so `parseMenu` takes the `capture-pane -e`
  text and strips SGR itself; a plain capture just marks no tab.
- The `▔` edge is menu-only (no permission, trust or queue fixture has
  one), which is what keeps `parseMenu` and the dedicated parsers apart.
- Rows exist only when the block has a cursor, a scroll marker (`↓ 9 more`,
  or `↓ 10.  Bash(…)` where the marker replaces the cursor gutter) or a
  numbered row; otherwise everything is `extra` (slider, status panel,
  help text). Lines at the base indent before the first row/box are the
  subtitle, after the list ends they are `extra` (`/model`'s effort line,
  `/theme`'s preview).
- The legend isn't always the last line (`/skills` ends on a note and keeps
  its hints in the subtitle, comma-separated), so it's the last line
  containing `Esc to`. `Type to …` has no key (`typing: true`).
- Unmarked description lines under marked rows (`/plugin`) can't be told
  from unmarked rows (`/theme`), so they are rows; clicking will be a
  closed loop on the cursor row's text (step 3), not a key count.
- `config.ansi` is the search-focused state; Esc there clears the search
  instead of closing, so a probe loop must close menus until the `▔` edge
  is gone.
- `Raw` (the block, dedented, plain) is always returned so the browser can
  fall back to it.

## Step 2 in detail (built)

`GET /api/sessions/{w}/menu` → `{"menu": menuView | null}`, ETag = the menu's
`sig` (304 while nothing moves). `ClaudePane` gained `CaptureStyled`
(`tmux.CapturePaneStyled`, `capture-pane -e`), since the active tab is only
in the colours. Browser: `menu-model.js` (`menuSections`, Node-tested),
`menu.js` (`createMenuWatcher` polls while the footer is gone,
`createMenuDialog` draws a read-only `<dialog>`: tabs, input, grouped rows,
extra, legend, a "Terminal view" toggle for `raw`), and the dialog banner
grows an "Open menu" button whenever the watcher finds a menu. Nothing is
typed yet.

## Step 3 in detail

`POST /api/sessions/{w}/menu/keys`:

```json
{"sig": "…", "keys": [{"key": "down"}, {"key": "s"}, {"text": "think"}],
 "goto": {"row": 3}}
```

- `sig` must equal the screen's current sig, else **409** with the current
  menu in the body (the browser re-renders); no menu → 409 `{menu:null}`.
  Nothing is pressed in either case.
- **Vocabulary** (`menuKeyToTmux`): `up down left right enter esc tab btab
  space pgup pgdn home end backspace`, `C-a`…`C-z` except `C-c C-d C-z`
  (quit/suspend Claude), and any single printable character (`{"key":"s"}`
  is how the legend's letters are sent) or `{"text": …}` (no control
  characters, ≤ 200 chars). ≤ 64 keys. Anything else → 400.
- **Esc**: at most one per request, and it must be last. Esc on a menu
  closes it (or clears a search), and a second one opens `/rewind`.
- **`goto`** moves the cursor to a row of the view the sig names, as a
  closed loop on the row's *text*, not a key count: press ↓ or ↑ toward it,
  re-read, stop on a match. Lists scroll (`↓ 9 more`) and unmarked
  description lines are rows, so counting keys would drift. Bounded by
  rows + above + below + 2 presses, and it gives up (409, cursor wherever
  it is) when two presses in a row change nothing. `keys` run after it.
- After the keys it waits (`Server.menuSettle` × a few) for the screen to
  change from the old sig, since Claude redraws asynchronously, and answers
  `{"menu": newView | null}` so the browser needn't wait for a poll. One
  request at a time (`menuMu`), like the mode cycle.
- Tests use a `fakeMenuPane` that draws a list in the probed layout and
  reacts to Up/Down/Enter/Esc/text, with a viewport and no wrap.
- Browser: ↑/↓/←/→/Enter/Esc/Tab/typing go to the menu, clicking a row is
  `goto` (+ Enter for a numbered/pick-one list), legend items are buttons
  (a `←/→` pair is two), "Type to search" focuses the dialog's input,
  Close sends Esc (once, and only while a menu is still up).

## Step 3 notes (built)

As specified; `Server.modeSettle` is the poll interval and `menuMu` guards
the endpoint. Row clicks carry the sig of the screen they were made on
(`payload.sig`), so a click queued behind other keys 409s instead of
moving to whatever row now has that index. Input: a persistent `<input>`
(survives re-renders, keeps focus) whose edits go out as
`menuTextDelta` (backspaces to the common prefix, then the tail). Esc is
the menu's: the dialog's native `cancel` is cancelled and sent as `esc`;
"Hide" just hides it.

## Step 4 notes (built)

Much of pattern C was already covered by step 3 (legend buttons for
Space/`s`/`t`/ctrl keys, ‹ › for tabs, the typed input for search, row click
= move, click again = Enter). What probing for this step found:

- **PageUp/PageDown do nothing** in Claude's lists (`/plugin`, 3820 rows):
  a cut list scrolls only by moving the cursor. So there is no paging;
  "↓ N more" and a ▲/▼ pair step the cursor, and search is how you get
  to row 3000.
- **Detail pages** (Enter on a plugin or MCP server) are the same shape:
  prose, then actions with a cursor (`❯ Install for you…`, `❯ 1.
  Reconnect`). Prose above the first row can't always be told from rows
  (`/rewind` and `/theme` put short unmarked rows above the cursor), so
  only unmarked lines that hang deep (≥ base+8) or run past 60 characters
  count as prose, and a line directly above a marked row stays a group
  heading.
- Fixtures added: `plugin-detail`, `mcp-detail`, `config-search`.
- **Probe hazard**: in `/config` Enter toggles the row under the cursor and
  `/` is the only way into the search box, so typing a command's name into
  it and pressing Enter changes real settings (it flipped auto-compact and
  idle compaction in the probe session, put back by hand). Probe `/config`
  with a throwaway `$HOME`/`CLAUDE_CONFIG_DIR`, or not at all.

## Step 5 notes (built)

- **Slash list tags**: `claude.SlashCommand.Terminal` (and `terminal` in
  `/commands`) marks the dialogs the web can't drive — `login`, `logout`,
  `ide`, `feedback`, `resume` (an external flow, or done better by the
  dashboard's own resume). Every other `Dialog` command now reads "opens
  menu" (`commandMenuTag` in `menu-model.js`); those that print a line
  instead (`/context`, `/color red`) simply open nothing.
- **Auto-open**: after a send, a message that is exactly `/word`
  (`isBareSlashCommand`) makes the chat view look for a menu for 4 s
  (`expectMenu`: forces the watcher on, because the footer poll lags) and
  opens the dialog when one shows. A skill command polls briefly and finds
  none. A menu started in the terminal still just gets the banner's "Open
  menu" button — nothing pops up uninvited.
- **Phone**: the dialog scrolls (`overflow: auto`), row lists cap at 40dvh,
  buttons are 36px tall, legend keys are real buttons (the keyboard on a
  phone has no arrows or Esc).
- **Notifier**: nothing to build. It only reads panes of windows with no
  session row, for the trust dialog; a menu belongs to a session with a
  row, and `TestMenusAreNotOtherDialogs` pins that no menu fixture parses
  as the trust dialog or a permission prompt.
