# mdm-watch

A small macOS daemon that monitors system configuration profiles and alerts you when your MDM pushes something new.

## What it does

- Detects added, removed, or updated configuration profiles (MDM and manual)
- Sends a clickable macOS notification — tap it to open the web UI with full details
- Marks profiles that have elevated access as **⚠ CRITICAL** (DLP, firewall, PPPC, network filters, VPN, etc.)
- Shows payload contents: what each profile actually controls (Chrome policies, Safari settings, certificates, FileVault escrow, firewall rules)
- Keeps a change log of everything that has ever been installed or removed

## Web UI

```
mdm-watch serve
```

Opens `http://localhost:8765` — three sections:

| Section | What's here |
|---|---|
| System profiles | MDM device configuration (firewall, certificates, endpoint security) |
| User profiles | Per-user policies (browser settings, managed preferences) |
| Provisioning profiles | App signing profiles with entitlements and certificates |

Each card is expandable — click to see payload bundle IDs, raw policy data, UUIDs, and install date.

![MDM Watch UI](docs/preview.png)

## Install

### Prerequisites

```bash
brew install terminal-notifier   # clickable notifications
```

### Build

```bash
git clone https://github.com/dzarlax/mdm-watch.git
cd mdm-watch
go build -o mdm-watch .
sudo cp mdm-watch /usr/local/bin/mdm-watch
```

### Run as a LaunchAgent (auto-start on login)

```bash
cp com.dzarlax.mdm-watch.plist ~/Library/LaunchAgents/
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.dzarlax.mdm-watch.plist
```

The agent runs `mdm-watch serve` — a long-running process that checks profiles every 5 minutes and serves the UI at `localhost:8765`.

### Verify it's running

```bash
launchctl list | grep mdm-watch   # should show a PID
curl -s http://localhost:8765/ | head -5
```

### Restart after update

```bash
sudo cp mdm-watch /usr/local/bin/mdm-watch
launchctl kickstart -k gui/$(id -u)/com.dzarlax.mdm-watch
```

## State and logs

All data lives in `~/.local/state/mdm-watch/`:

| File | Contents |
|---|---|
| `state.json` | Snapshot of currently active profiles |
| `changes.log` | Timestamped history of every ADDED / REMOVED / UPDATED event |
| `stdout.log` | Daemon stdout (periodic check results) |
| `stderr.log` | Daemon stderr (errors) |

## Critical profile detection

Profiles are automatically flagged **⚠ CRITICAL** if their name, identifier, description, or payload data matches any of these keywords:

`dlp` · `data loss` · `endpoint security` · `network filter` · `content filter` · `web filter` · `ssl inspection` · `tls inspection` · `traffic` · `proxy` · `keychain` · `screen` · `accessibility` · `full disk access` · `kernel extension` · `system extension` · `pppc` · `privacy` · `firewall` · `vpn` · `mdm agent` · `mdm enrollment` · `supervision`

The matched keywords are shown inside the expanded card.

## Without terminal-notifier

If `terminal-notifier` is not installed, notifications fall back to `osascript` — they appear but are not clickable. Install it with `brew install terminal-notifier` to get the click-to-open-UI behaviour.
