---
paths:
  - "internal/tickets/**"
  - "internal/tui/ticket_detail.go"
  - "internal/tui/project_picker.go"
  - "cmd/mo/jira*.go"
---

# Tickets

Provider-agnostic ticket panel rendered in the bottom half of the dashboard right column. Jira is the only provider today but the `Ticket` / `Bucket` / `Provider` abstractions accept anything (Linear, GitHub Projects) that can return tickets assigned to the authenticated user.

## Core model

- **Buckets**: `in_progress`, `blocked`, `review`, `todo`, `unmapped`. Raw provider statuses are resolved via `StatusMap` (case-insensitive, whitespace-trimmed). Anything not matched falls into `unmapped` and renders the raw status in red brackets so workflow drift is obvious.
- **Sort within bucket** (`SortByRelevance`): `(InSprint desc, Priority desc, UpdatedAt desc)`. Stable sort — ties preserve provider-returned order.
- **Provider interface**: `Provider{Name, MyTickets, Detail}` — `Detail(ctx, id)` added for the per-ticket popup.

## Jira provider

- **Endpoint**: `POST /rest/api/3/search/jql` with `{jql, fields, maxResults}`. The v2 `/rest/api/2/search` endpoint was removed by Atlassian in 2025 (changelog CHANGE-2046); do not roll back to it.
- **Sprint detection**: JQL runs `assignee = currentUser() AND statusCategory != Done`. The dynamic sprint custom field (default `customfield_10020`) is extracted out of band because its ID varies per installation. Configurable via `sprint_field_id`.
- **Priority normalization**: Jira's named priorities (`Highest`/`Blocker`/`Critical`, `High`/`Major`, `Medium`, `Low`, `Lowest`/`Trivial`) map to the 1–5 scale.
- **Auth**: basic auth with `email:API-token`. Token lives at `~/.config/unky-mo/jira.token` (mode 0600, enforced by `LoadToken`); env var `UNKY_MO_JIRA_TOKEN` overrides the file. Tokens **never** go in `config.toml`.
- **Error surfacing**: `extractJiraError` parses `{errorMessages, errors, message}` shapes so the panel and `mo jira fetch` show just the human message, not the JSON blob.
- **Detail**: `GET /rest/api/3/issue/{key}?expand=renderedFields` — description comes back as HTML, stripped to plain text by `StripHTML` (paragraph tags → blank line, `<br>`/`</li>` → single newline, `<li>` → `- ` bullet, entities decoded).

## Rendering

Panel only appears when a token is present (file or env) OR `[[tickets.jira]]` is configured — controlled by `ticketsShouldRender()` in `internal/tui/tickets.go`. Explicit opt-out via `[tickets] disabled = true`. Overflow cap per bucket is `per_bucket_limit` (default 5); extra rows collapse to `… +N more`. Fetch cadence: `ticketsTickMsg`, default 5min (config `refresh_seconds`). Initial fetch fires from `Init`.

## Ticket detail screen (`ScreenTicket`)

`enter` on a ticket transitions to `ScreenTicket` (full-screen, matches `ScreenProject`/`ScreenHelp` pattern). Footer bindings:

- **`s` (start working)**: `handleTicketStartWorking` in `internal/tui/ticket_detail.go`. Resolves the project mapping via `resolvedMoProjectForTicket`; if unmapped, opens the picker; if mapped, calls `startWorkOnTicket`. Collides with the dashboard's `s` (suspend) — the handler in `app.go` short-circuits when `m.screen == ScreenTicket`.
- **`o`**: opens ticket URL via `openInBrowser`.
- **`y`**: copies `tickets.BranchNameForTicket(id, title)` to clipboard.
- **`esc`**: unwinds one layer at a time — remember-prompt → picker → screen → dashboard.

## Project mapping (`internal/tickets/mapping.go`)

Jira project keys (e.g. `OP`) must resolve to a Mo project name (e.g. `moma-apps-rails`) before start-working can do anything. Two sources:

1. **Config map** — hand-authored `[tickets.jira.project_map]` under each `[[tickets.jira]]`. Wins on conflict.
2. **Companion file** — `~/.config/unky-mo/jira-project-map.toml`, auto-managed by the picker (`LoadCompanionProjectMap` / `SaveProjectMapEntry`). Stored as `[[entry]]` rows with `provider` / `jira_key` / `mo_project` fields.

Separate companion file on purpose: round-tripping `config.toml` through BurntSushi/toml would lose comments and reorder blocks. `MergeProjectMaps(cfgMap, companionMap)` — config wins, companion supplements.

## Start-working state machine

```
resolve Mo project (config + companion)
    │
    ├── missing → startProjectPicker → user picks
    │                → pickerRememberActive prompt
    │                    → `r` SaveProjectMapEntry + reload map
    │                    → `n` in-memory-only for this session
    │                → fall through to branch flow
    └── present → branch flow
                    │
                    ▼
             derive `<id>-<slug>` (tickets.BranchNameForTicket)
                    │
                    ▼
             project dir on disk? (os.Stat)  no → status msg, stay
                    │ yes
                    ▼
             existing worktree for branch? (project.ListWorktrees)
                yes → focusIfExists + launchClaudeInWindow if cold
                no  → claude.SessionForPath(projectPath)?
                         yes → m.createWorktreeAndLaunch(branch)
                         no  → m.openBranchInMain(branch, false)
```

The branch-flow helpers read `m.detailProject`, so `startWorkOnTicket` sets it before returning the Cmd — copy-not-alias to avoid leaking mutations into `m.projects`.

## Project picker

A `bubbles/v2/list` model with a custom `pickerItem` wrapper (separate from `ProjectItem` so it doesn't need session-status machinery). Fuzzy filter is on by default. Activated via `startProjectPicker(provider, jiraKey)`. Key routing: while `pickerActive` is true, the main Update forwards keys to `updateProjectPicker` via `handleTicketPickerActive`, except `enter` which confirms the pick and flips to `pickerRememberActive`. `enter` inside an active filter falls through to the list so the filter can apply first.
