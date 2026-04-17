# Unky Mo

A terminal UI for orchestrating multiple Claude Code sessions across your projects. See which sessions are running, which need your attention, and switch between them — all from one place.

## Prerequisites

- **Go** 1.22+ (`go version`)
- **tmux** 3.0+ (`tmux -V`)
- **Claude Code CLI** (`claude --version`)
- **GitHub CLI** (`gh --version`) — optional, for pull request display

## Installation

```bash
git clone git@github.com:rikvanmechelen/unky-mo.git
cd unky-mo
make install   # builds and installs to ~/go/bin/mo
```

## Quick Start

### 1. Configure your workspace

Create `~/.config/unky-mo/config.toml`:

```toml
workspace_dirs = ["/path/to/your/workspace"]
```

Unky Mo will auto-discover all git repositories in these directories.

### 2. Install notification hooks

This adds hooks to `~/.claude/settings.json` so Claude Code can notify Unky Mo when sessions need your attention:

```bash
mo hooks install
```

### 3. Launch

```bash
mo
```

If you're not already inside tmux, Unky Mo automatically creates a tmux session (with mouse support enabled) and launches itself inside it. If the session already exists, it attaches to it. If the TUI crashed, it restarts automatically.

When you launch Claude sessions from the TUI, they open as sibling tmux windows with an interactive sidebar. When you exit a Claude session (`ctrl-d ctrl-d` or `/exit`), the window closes cleanly — no orphaned panes.

## Dashboard

The main screen shows all your projects with git status and session indicators, plus an active sessions panel on the right:

```
 Unky Mo  3 active  ▲1                    │
 ▸ moma-apps-rails  [ruby]  main *2  ● active   │ Active Sessions ◀
   moma-go          [go  ]  main     ○ no session│   ● moma-apps-rails
   unky-mo          [go  ]  main ↑3  ● idle      │ ▸ ● unky-mo        idle
   moma-auth0       [node]  main     ○ no session│   ● mla-wrapper
   ...                                            │
```

Each project row shows: name, language, git branch, dirty/ahead/behind counts, and session status.

### Dashboard shortcuts

| Key          | Action                                |
|--------------|---------------------------------------|
| `↑` / `k`   | Move up                               |
| `↓` / `j`   | Move down                             |
| `→`          | Focus active sessions panel           |
| `←`          | Focus project list                    |
| `enter`      | Open project detail / switch to session |
| `/`          | Filter projects (fuzzy search)        |
| `n`          | Start a new Claude session            |
| `a`          | Attach to session window              |
| `?`          | Toggle help overlay                   |
| `ctrl+r`     | Restart TUI + all sidebars            |
| `q`          | Quit                                  |

### Status indicators

| Symbol         | Color  | Meaning                                         |
|----------------|--------|--------------------------------------------------|
| ● active       | Yellow | Claude is working (mid-turn)                     |
| ● needs input  | Green  | Claude finished its turn, waiting for you         |
| ● permission!  | Red    | Claude needs you to approve a permission          |
| ○ no session   | Gray   | No Claude session running in this project         |

Lists wrap around — pressing up at the top goes to the bottom.

## Sidebar

Every Claude session window includes a sidebar pane on the right. It has two sections: **active sessions** at the top and **changed files** at the bottom.

```
┌──────────────────────────────────┬────────────────────────────────────┐
│                                  │ ── Sessions ──                     │
│  Claude Code session             │    ☗ Unky Mo Home                  │
│  (your main work area)           │    ● rails-app                     │
│                                  │ ▸  ● unky-mo              idle    │
│  > working on feature...         │                                    │
│                                  │ Changed 3 files +42 -8             │
│                                  │  internal/tui/                     │
│                                  │ ▸   app.go                         │
│                                  │     sidebar/                       │
│                                  │       model.go                     │
│                                  │  go.mod                            │
│                                  │                                    │
│                                  │  ↑↓ nav   ⏎/d diff                │
│                                  │  v edit   o open                   │
│                                  │  ` popup  s synced                 │
│                                  │  ^r refresh                        │
└──────────────────────────────────┴────────────────────────────────────┘
```

The current window's project is highlighted in bold white + underline. Arrow down past the sessions to navigate into the changed files tree.

### Sidebar shortcuts — Sessions section

| Key          | Action                                |
|--------------|---------------------------------------|
| `↑` / `k`   | Move up                               |
| `↓` / `j`   | Move down (into files section at end) |
| `enter`      | Switch to selected session            |
| `t`          | Toggle terminal drawer                |
| `T`          | Create new terminal tab               |
| `tab`        | Cycle terminal tabs                   |
| `x`          | Close current terminal tab            |
| `` ` ``      | Open a floating popup terminal        |
| `s`          | Push session to sync repo             |
| `ctrl+r`     | Restart sidebar (reload binary)       |
| click        | Switch to clicked session             |

### Sidebar shortcuts — Files section

| Key          | Action                                |
|--------------|---------------------------------------|
| `↑` / `k`   | Move up (into sessions at top)        |
| `↓` / `j`   | Move down                             |
| `enter` / `d`| Show git diff in floating popup      |
| `v`          | Open in `$EDITOR` (vim/nvim) popup    |
| `o`          | Open in VS Code or default editor     |

### Sync status

The `s sync` label in the footer shows sync status:
- **`s sync`** (gray) — not synced
- **`s synced`** (green) — up to date with sync repo
- **`s sync ↑`** (yellow) — local changes newer than last push

### Terminals

- **`t`** — Toggle a terminal drawer below the Claude pane
- **`T`** — Create additional terminal tabs
- **`tab`** / **`shift+tab`** — Cycle between terminal tabs
- **`x`** — Close current terminal tab
- **`` ` ``** — Open a floating 80% popup terminal

### Focus management

- `Ctrl-b` + right arrow — focus the sidebar pane
- `Ctrl-b` + left arrow — focus the Claude pane
- Click — tmux mouse support is enabled

## Project Detail Screen

Press `enter` on a project in the dashboard to see its detail view with a side-by-side layout:

```
 ← moma-apps-rails  [ruby]  /Users/.../moma-apps-rails

 Sessions ◀                          │ Pull Requests
 ▸ ● generate-test-qr  2h ⇅         │   #42 fix auth flow
   ○ fix-modal-back    3d           │   #41 add QR scanner
                                     │   #39 update deps
 Last messages                       │
   You: fix the failing test...      │
   Claude: I found the issue...      │
                                     │
 Worktrees                           │
   testing-worktrees                 │
     ○ worktree-session-1   1d      │
   feature-branch                    │
     (no sessions)                   │
```

**Left panel** — Sessions (with recap preview), worktrees with nested sessions. Synced sessions marked with `⇅`. Sessions auto-pull from the sync repo when you open a project.

**Right panel** — Open pull requests from GitHub (requires `gh auth login`). Press `enter` to expand a PR inline showing description, stats, review status, and actions.

### Project detail shortcuts

| Key     | Action                                          |
|---------|-------------------------------------------------|
| `↑/↓`  | Navigate within the focused panel               |
| `←/→`  | Switch between left and right panels            |
| `enter` | Resume session / launch worktree / expand PR   |
| `o`     | Open PR in browser                              |
| `w`     | Create worktree (or worktree from expanded PR)  |
| `c`     | Checkout expanded PR's branch locally           |
| `n`     | Start a new Claude session                      |
| `a`     | Attach to existing session window               |
| `esc`   | Back to dashboard                               |

Sessions under worktrees resume in the **worktree's directory**, not the project root.

## CLI Commands

```bash
mo                              # Launch the TUI (default)
mo list                         # List all discovered projects
mo sessions                     # List active Claude Code sessions
mo start <project>              # Start a new Claude session
mo start <project> -p "prompt"  # Start with an initial prompt
mo resume <project>             # Resume the most recent session
mo attach <project>             # Switch to a project's tmux window
mo scan                         # Re-scan workspace directories
mo hooks install                # Install notification hooks
mo hooks uninstall              # Remove notification hooks
mo hooks status                 # Check if hooks are installed
mo sync init <repo-url>         # Connect to a private GitHub repo for syncing
mo sync init-key                # Generate a shared encryption key (run once)
mo sync show-key                # Print the key for copying to another machine
mo sync push <project>          # Encrypt and push a session to the sync repo
mo sync pull                    # Pull + decrypt all sessions (files only)
mo sync pull <project>          # Pull a session and resume it in a tmux window
mo sync list                    # List available synced sessions
mo sync migrate                 # Re-encrypt any legacy plaintext sessions
mo debug <project>              # Dump session/worktree debug info
mo version                      # Print version
```

### Examples

```bash
# See what projects are available
mo list

# Start Claude in a specific project
mo start my-rails-app

# Start Claude with a specific task
mo start my-rails-app -p "fix the failing test in users_controller_test.rb"

# Check which Claude sessions are running
mo sessions

# Resume where you left off
mo resume my-rails-app

# Debug session detection for a project
mo debug my-rails-app
```

## Configuration

Config file: `~/.config/unky-mo/config.toml`

```toml
# Directories to scan for git repositories
workspace_dirs = ["/Users/you/workspace", "/Users/you/personal"]

# tmux session name (default: "mo")
tmux_session = "mo"

# Unix socket path for notifications (default: "/tmp/unky-mo.sock")
socket_path = "/tmp/unky-mo.sock"

# Auto-scan workspace dirs on startup (default: true)
scan_on_startup = true

# Play terminal bell on notification (default: true)
notify_sound = true

# Manually define projects (optional, overrides auto-discovery)
[[project]]
name = "my-rails-app"
path = "/Users/you/workspace/my-rails-app"
description = "Main web application"
language = "ruby"
tags = ["production"]
```

### Project auto-discovery

Unky Mo scans each directory in `workspace_dirs` for subdirectories containing a `.git` directory. Directories ending in `.worktrees` are automatically excluded. Languages are detected automatically:

| File              | Language |
|-------------------|----------|
| `Gemfile`         | ruby     |
| `go.mod`          | go       |
| `package.json`    | node     |
| `requirements.txt` / `pyproject.toml` / `Pipfile` | python |
| `Cargo.toml`      | rust     |
| `Podfile`         | ios      |
| `build.gradle` / `pom.xml` | java |

Manually defined `[[project]]` entries override auto-discovered settings for the same path.

## How Notifications Work

When you run `mo hooks install`, Unky Mo adds two hooks to `~/.claude/settings.json`:

1. **Notification hook** — Fires when Claude has been idle for 60+ seconds (`idle_prompt`) or needs a permission approval (`permission_prompt`). Sends a message to Unky Mo's Unix socket.
2. **Stop hook** — Fires when Claude finishes a turn. Clears the idle/permission status.

The TUI also proactively detects idle sessions by checking the `stop_reason` of the last assistant message in the session JSONL (`end_turn` = idle, `tool_use` = still working). This works even if a notification is missed.

The TUI listens on a Unix socket (`/tmp/unky-mo.sock` by default) and updates status indicators in real time. Sidebar instances read a shared state file (`/tmp/unky-mo-state.json`) written by the TUI. If Unky Mo isn't running, the hooks exit silently with no effect on Claude.

To remove the hooks:

```bash
mo hooks uninstall
```

## Session Sync

Sync individual Claude sessions between machines using a private GitHub repo. Useful when you want to pick up a session on another computer.

Sessions are **encrypted client-side with AES-256-GCM** before anything is pushed. Project directory names on the remote are opaque HMAC hashes and commit messages are generic, so the repo contents and git log don't leak project names, hostnames, or conversation content. Everything is decrypted locally on pull.

### Setup

**1. Create a private repo** on GitHub (e.g. `coding-sessions`).

**2. Generate a shared key on one machine:**

```bash
mo sync init-key
```

This writes a 32-byte random key to `~/.config/unky-mo/sync.key` (mode `0600`). Anyone holding this key can decrypt your synced sessions — treat it like a password.

**3. Copy the key to every other machine** that should sync. Either copy the file directly (via 1Password, `scp`, a USB key, etc.) or print it and paste it in:

```bash
# On the source machine:
mo sync show-key

# On each other machine:
mkdir -p ~/.config/unky-mo
printf '%s\n' '<paste the base64 key>' > ~/.config/unky-mo/sync.key
chmod 600 ~/.config/unky-mo/sync.key
```

Alternatively, keep the key out of the filesystem by exporting `UNKY_MO_SYNC_KEY=<base64>` in your shell — this takes precedence over the file.

**4. Initialize the sync repo on each machine:**

```bash
mo sync init git@github.com:youruser/coding-sessions.git
```

### Pushing a session

From the CLI:
```bash
mo sync push moma-apps-rails
```

Or press `s` in any sidebar to push the current project's session.

The session JSONL and metadata are encrypted and committed to the repo. The sidebar shows sync status: green when synced, yellow when local changes are newer.

### Pulling sessions

Pull every synced session's history down to this machine:
```bash
mo sync pull
```

Pull a single project and immediately resume it:
```bash
mo sync pull moma-apps-rails
```

Sessions also auto-pull when you open a project detail screen.

### Listing available sessions

```bash
mo sync list
```

### Migrating from an older plaintext repo

```bash
mo sync migrate
```

Re-encrypts plaintext sessions. Git history still contains old blobs — delete and recreate the remote to fully purge.

### Lost or leaking keys

1. Delete and recreate the sync repo on GitHub.
2. `rm ~/.config/unky-mo/sync/` on each machine, then `mo sync init-key --force` once and redistribute the new key.
3. `mo sync init <new-url>` and push fresh from each machine.

## Development

```bash
# Build and install
make install

# Build only (local binary)
go build -o mo ./cmd/mo

# Run tests
go vet ./...

# Dev workflow: edit code → make install → ctrl+r in Mo to reload
```

When developing Mo while using it, `ctrl+r` restarts the TUI and all sidebars to pick up the new binary.

## tmux Tips

- **Switch windows**: `Ctrl-b` then window number (`0` = TUI, `1` = first project, etc.)
- **List windows**: `Ctrl-b w` (tmux's built-in window picker)
- **Switch panes**: `Ctrl-b` + arrow key, or click with mouse
- **Rename window**: `Ctrl-b ,`
- **Detach**: `Ctrl-b d` (everything keeps running in the background)
- **Reattach**: `tmux attach -t mo`

## Project Structure

```
unky-mo/
├── cmd/mo/main.go          # CLI entry point (Cobra commands)
├── internal/
│   ├── config/             # TOML config loading
│   ├── claude/             # Session detection, JSONL parsing, hook management
│   ├── github/             # GitHub PR fetching via gh CLI
│   ├── tmux/               # tmux command wrapper (sessions, windows, panes, popups)
│   ├── notify/             # Unix socket notification server
│   ├── state/              # Shared JSON state file (TUI ↔ sidebars)
│   ├── sync/               # Encrypted session sync via private git repo
│   ├── project/            # Project model, scanner, worktree support, git status
│   └── tui/
│       ├── app.go          # Main TUI model, views, key handling
│       ├── delegate.go     # Project list item renderer (with git status)
│       ├── keys.go         # Key bindings
│       ├── styles.go       # Lipgloss theme (dark background ~#14191E)
│       └── sidebar/        # Compact sidebar TUI for tmux panes
├── scripts/
│   ├── notify-hook.sh      # Claude Code notification hook
│   └── stop-hook.sh        # Claude Code stop hook
├── Makefile
└── CLAUDE.md
```
