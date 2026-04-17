# Unky Mo

A terminal UI for orchestrating multiple Claude Code sessions across your projects. See which sessions are running, which need your attention, and switch between them — all from one place.

## Prerequisites

- **Go** 1.22+ (`go version`)
- **tmux** 3.0+ (`tmux -V`)
- **Claude Code CLI** (`claude --version`)
- **GitHub CLI** (`gh --version`) — optional, for pull request display

## Installation

```bash
# Clone and build
cd /path/to/unky-mo
make install
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

## Sidebar

Every Claude session window includes a sidebar pane on the right showing active sessions with live status indicators. The current window's project is highlighted in bold white + underline.

```
┌──────────────────────────────────┬────────────────────────────────┐
│                                  │ ── Sessions ──                 │
│  Claude Code session             │    ☗ Unky Mo Home              │
│  (your main work area)           │    ● rails-app                 │
│                                  │ ▸  ● unky-mo           idle   │
│  > working on feature...         │    ○ mla-wrapper               │
│                                  │                                │
│                                  │  ↑↓ nav  ⏎ switch             │
│                                  │  t term  ` popup               │
│                                  │  ctrl+r restart                │
└──────────────────────────────────┴────────────────────────────────┘
```

### Sidebar shortcuts

| Key          | Action                                |
|--------------|---------------------------------------|
| `↑` / `k`   | Move up                               |
| `↓` / `j`   | Move down                             |
| `enter`      | Switch to selected session            |
| `t`          | Open a terminal split below Claude    |
| `` ` ``      | Open a floating popup terminal        |
| `ctrl+r`     | Restart sidebar (reload binary)       |
| click        | Switch to clicked session             |
| `q`          | Close the sidebar pane                |

**Unky Mo Home** (first item) switches back to the main TUI (window 0).

The sidebar only shows projects with active Claude sessions. It always targets its own project window for terminals — `t` and `` ` `` work regardless of which item the cursor is on.

### Terminals

From the sidebar you can open terminals for the current project:

- **`t`** — Split a terminal pane below the Claude session. Press `t` multiple times to create more panes. Use `Ctrl-b ↑/↓` to switch between them.
- **`` ` ``** — Open a floating 80% popup terminal. Closes when you `exit` or press `Esc`. Great for one-off commands.

Both open in the project's directory.

### Focus management

- `Ctrl-b` + right arrow — focus the sidebar
- `Ctrl-b` + left arrow — focus the Claude pane
- Click — tmux mouse support is enabled, so you can click on panes

## Project Detail Screen

Press `enter` on a project in the dashboard to see its detail view with a side-by-side layout:

```
 ← moma-apps-rails  [ruby]  /Users/.../moma-apps-rails

 Sessions ◀                          │ Pull Requests
 ▸ ● generate-test-qr  2h           │   #42 fix auth flow
   ○ fix-modal-back    3d           │   #41 add QR scanner
                                     │   #39 update deps
 Worktrees                           │
   testing-worktrees                 │
     ○ worktree-session-1   1d      │
     ○ worktree-session-2   5d      │
   feature-branch                    │
     (no sessions)                   │
```

**Left panel** — Sessions grouped by location (main project + each worktree), and worktree headers you can launch new sessions into.

**Right panel** — Open pull requests from GitHub (requires `gh auth login`).

### Project detail shortcuts

| Key     | Action                                    |
|---------|-------------------------------------------|
| `↑/↓`  | Navigate within the focused panel         |
| `tab`   | Switch between left and right panels      |
| `enter` | Resume selected session / launch worktree |
| `o`     | Open selected PR in browser               |
| `n`     | Start a new Claude session                |
| `a`     | Attach to existing session window         |
| `w`     | Create a new git worktree + session       |
| `esc`   | Back to dashboard                         |

Sessions under worktrees resume in the **worktree's directory**, not the project root.

## TUI Overview

```
┌────────────────────────────────────────────────────┐
│  Unky Mo                             2 active  ▲1  │
├────────────────────────────────────────────────────┤
│  ▸ my-rails-app       [ruby]    ● active           │
│    my-go-service      [go  ]    ● needs input      │
│    my-frontend        [node]    ○ no session        │
│    another-project    [py  ]    ○ no session        │
│    ...                                              │
├────────────────────────────────────────────────────┤
│ ↑↓:navigate  enter:open  n:new session  a:attach   │
│ /:filter  ?:help  q:quit                            │
└────────────────────────────────────────────────────┘
```

### Status indicators

| Symbol | Color  | Meaning                                      |
|--------|--------|----------------------------------------------|
| ● active       | Green  | Claude is working                            |
| ● needs input  | Yellow | Claude has been idle 60s+ or stuck on a permission prompt |
| ○ no session   | Gray   | No Claude session running in this project    |

The header shows a count of active sessions and an attention badge (▲) when sessions need you.

### Dashboard shortcuts

| Key        | Action                    |
|------------|---------------------------|
| `↑` / `k`   | Move up                   |
| `↓` / `j`   | Move down                 |
| `enter` / `→` | Open project detail       |
| `esc` / `←`   | Go back                   |
| `/`          | Filter projects (fuzzy search) |
| `n`          | Start a new Claude session |
| `a`          | Attach to session window  |
| `?`          | Toggle help overlay       |
| `ctrl+r`     | Restart TUI + all sidebars (picks up new binary) |
| `q`          | Quit                      |

Lists wrap around — pressing up at the top goes to the bottom.

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

The TUI also proactively detects idle sessions by checking if the session JSONL file hasn't been modified in >60 seconds, so status updates work even if a notification is missed.

The TUI listens on a Unix socket (`/tmp/unky-mo.sock` by default) and updates the status indicators in real time. Sidebar instances read a shared state file (`/tmp/unky-mo-state.json`) written by the TUI. If Unky Mo isn't running, the hooks exit silently with no effect on Claude.

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

```bash
mo sync push moma-apps-rails
```

The session JSONL and metadata are encrypted and committed to the repo under an opaque directory name.

### Pulling sessions

Pull every synced session's history down to this machine (decrypts into `~/.claude/projects/...`, no tmux windows opened):

```bash
mo sync pull
```

Pull a single project and immediately resume it in a new tmux window:

```bash
mo sync pull moma-apps-rails
```

Sessions for projects that aren't checked out on this machine are skipped with a warning — check the project out locally and re-run to pull.

### Listing available sessions

```bash
mo sync list
```

Shows all sessions in the repo with their title, source machine, and age (the metadata is decrypted on the fly):

```
  moma-apps-rails           generate-test-qr-tickets  from mac-office  2h ago
  unky-mo                   unky-mo-session-orchestrator  from mac-office  5m ago
```

### Migrating from an older plaintext repo

Early versions of the sync tooling pushed plaintext JSONL and metadata. If your sync repo still contains any of those, run:

```bash
mo sync migrate
```

This re-encrypts each plaintext project directory into the new hashed/encrypted layout, commits, and pushes.

Important: git history still contains the original plaintext blobs. To fully purge them, either (1) delete and recreate the remote repo on GitHub and `mo sync init <new-url>` / push from each machine, or (2) rewrite history with `git-filter-repo` on the sync clone and force-push.

### Lost or leaking keys

If the shared key leaks (published to a public gist, included in a screenshot, committed by mistake), rotate immediately:

1. Delete and recreate the sync repo on GitHub.
2. `rm ~/.config/unky-mo/sync/` on each machine, then `mo sync init-key --force` once and redistribute the new key.
3. `mo sync init <new-url>` and push fresh from each machine.

Rotating the key does not re-encrypt old pushes, so the old remote must be destroyed.

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
│   ├── project/            # Project model, scanner, worktree support
│   └── tui/
│       ├── app.go          # Main TUI model, views, key handling
│       ├── delegate.go     # Project list item renderer
│       ├── keys.go         # Key bindings
│       ├── styles.go       # Lipgloss theme (dark background ~#14191E)
│       └── sidebar/        # Compact sidebar TUI for tmux panes
├── scripts/
│   ├── notify-hook.sh      # Claude Code notification hook
│   └── stop-hook.sh        # Claude Code stop hook
├── Makefile
└── CLAUDE.md
```
