# Unky Mo

A terminal UI for orchestrating multiple Claude Code sessions across your projects. See which sessions are running, which need your attention, and switch between them — all from one place.

## Prerequisites

- **Go** 1.22+ (`go version`)
- **tmux** 3.0+ (`tmux -V`)
- **Claude Code CLI** (`claude --version`)

## Installation

```bash
# Clone and build
cd /path/to/unky-mo
go build -o mo ./cmd/mo

# Or install to your Go bin
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
./mo hooks install
```

### 3. Launch

```bash
./mo
```

If you're not already inside tmux, Unky Mo automatically creates a tmux session called `mo` and launches itself inside it. If the `mo` session already exists, it attaches to it.

When you launch Claude sessions from the TUI, they open as sibling tmux windows with an interactive sidebar. Switch between them with `Ctrl-b` + window number, and `Ctrl-b 0` to get back to the TUI.

## Sidebar

Every Claude session window includes a narrow sidebar pane on the right that shows all sessions with live status indicators. You can navigate the sidebar and switch between projects without going back to the main TUI.

```
┌──────────────────────────────────────┬──────────────────────┐
│                                      │ ── Sessions ──────── │
│  Claude Code session                 │  ▸ ☗ Unky Mo Home    │
│  (your main work area)               │    ● rails-app       │
│                                      │    ● go-svc    idle  │
│  > working on feature...             │    ○ frontend        │
│                                      │    ○ my-python       │
│                                      │                      │
│                                      │  ↑↓ navigate         │
│                                      │  ⏎  switch           │
└──────────────────────────────────────┴──────────────────────┘
```

### Sidebar shortcuts

| Key        | Action                              |
|------------|-------------------------------------|
| `↑` / `k`   | Move up                             |
| `↓` / `j`   | Move down                           |
| `enter`      | Switch to the selected project      |
| `q`          | Close the sidebar pane              |

The first item, **Unky Mo Home**, switches back to the main TUI (window 0).

To focus the sidebar pane, use `Ctrl-b` + right arrow. To go back to the Claude pane, use `Ctrl-b` + left arrow.

The sidebar updates every second by reading a shared state file written by the main TUI. Status indicators are the same as in the main TUI: `●` green = active, `●` yellow = needs input, `●` red = permission needed, `○` gray = no session.

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
│ /:filter  s:sessions  w:worktrees  ?:help  q:quit  │
└────────────────────────────────────────────────────┘
```

### Status indicators

| Symbol | Color  | Meaning                                      |
|--------|--------|----------------------------------------------|
| ● active       | Green  | Claude is working                            |
| ● needs input  | Yellow | Claude has been idle 60s+ and is waiting for you |
| ● permission!  | Red    | Claude needs you to approve a permission     |
| ○ no session   | Gray   | No Claude session running in this project    |

The header shows a count of active sessions and an attention badge (▲) when sessions need you.

## Keyboard Shortcuts

All shortcuts are shown in the footer bar at the bottom of every screen.

### Navigation

| Key        | Action                    |
|------------|---------------------------|
| `↑` / `k`   | Move up                   |
| `↓` / `j`   | Move down                 |
| `enter` / `→` | Open project detail       |
| `esc` / `←`   | Go back                   |
| `/`          | Filter projects (fuzzy search) |

### Sessions

| Key | Action                                           |
|-----|--------------------------------------------------|
| `n`   | Start a new Claude session in the selected project |
| `a`   | Attach — switch to the project's tmux window     |
| `r`   | Resume the most recent session for the project   |
| `s`   | View all active sessions across projects         |

### Other

| Key | Action              |
|-----|---------------------|
| `w`   | View git worktrees  |
| `?`   | Toggle help overlay |
| `q`   | Quit                |

## CLI Commands

You can also use Unky Mo non-interactively from the command line:

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
mo sync pull                    # Pull + decrypt all sessions (files only, no tmux windows)
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

Unky Mo scans each directory in `workspace_dirs` for subdirectories containing a `.git` directory. Languages are detected automatically:

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

The TUI listens on a Unix socket (`/tmp/unky-mo.sock` by default) and updates the status indicators in real time. If Unky Mo isn't running, the hooks exit silently with no effect on Claude.

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
- **Rename window**: `Ctrl-b ,`
- **Detach**: `Ctrl-b d` (everything keeps running in the background)
- **Reattach**: `tmux attach -t mo`

## Project Structure

```
unky-mo/
├── cmd/mo/main.go          # CLI entry point (Cobra commands)
├── internal/
│   ├── config/             # TOML config loading
│   ├── claude/             # Session detection, hook management
│   ├── tmux/               # tmux command wrapper
│   ├── notify/             # Unix socket notification server
│   ├── project/            # Project model, scanner, worktrees
│   └── tui/                # Bubbletea TUI (app, styles, keys, delegate)
├── scripts/
│   ├── notify-hook.sh      # Claude Code notification hook
│   └── stop-hook.sh        # Claude Code stop hook
├── Makefile
└── CLAUDE.md
```
