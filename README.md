# Codeshot

**Click code blocks in Claude Code's terminal output to stage them in a new terminal tab.**

No auto-execution. No separate app. No wrapper command. Works inside your existing terminal session.

---

## What it does

After each Claude Code response, Codeshot finds the code blocks and turns them into [OSC 8 hyperlinks](https://github.com/Alhadis/OSC8-Adoption) — terminal-native clickable links. Hover to highlight, click to act.

```
─────────────────────────────────────────────────────
▶ Run in terminal · powershell  Get-ChildItem -Recurse…
▶ Run in terminal · bash        find . -name "*.log" -…
─────────────────────────────────────────────────────
```

Clicking a link opens a new terminal tab with the code pre-loaded for review:

```
 Codeshot  Review then press Enter to execute  (Ctrl+C to cancel)

Get-ChildItem -Path C:\logs -Filter *.log -Recurse |
  Where-Object { $_.LastWriteTime -gt (Get-Date).AddDays(-7) }

────────────────────────────────────────────────────
>
```

Press Enter to execute. Ctrl+C to cancel. Nothing runs until you decide.

---

## Why this exists

Claude Code's safeguard system may block edits in certain contexts — when `--dangerously-skip-permissions` isn't appropriate and you still want to run the code Claude suggests. Codeshot gives you a clean path: grab the code, review it, run it yourself.

It also serves the general preference of reviewing any command before it lands in your shell — regardless of safeguards.

**Why not just copy-paste?**
Long commands wrap at terminal width and break when pasted. Code blocks with multiple commands require manual assembly. Codeshot handles encoding, escaping, and multiline code transparently.

---

## Architecture

Codeshot is intentionally minimal. Three pieces:

### 1. Stop hook (`hook/hook.js`)

Registered as a Claude Code `Stop` lifecycle hook. Fires after every Claude response.

- Reads the session JSONL at `transcript_path` (provided by Claude Code in the hook payload)
- Finds the last assistant message
- Extracts fenced code blocks via regex
- Emits OSC 8 hyperlinks to stdout — they appear in your terminal inline, below Claude's response

No polling. No background process. No persistent daemon.

### 2. Protocol handler

Registered for the `codeshot://` URL scheme at the OS level (no admin required).

- **Windows:** PowerShell script registered in `HKCU\Software\Classes\codeshot`
- **Mac:** Minimal `.app` bundle registered with Launch Services

When a link is clicked:
1. Decodes the code from the URL (or reads a temp file for long code)
2. Writes a launcher script
3. Opens a new terminal tab running the launcher
4. Launcher shows the code, waits for Enter, then runs it

**Windows Terminal:** `wt new-tab -- pwsh -NoExit -File launcher.ps1`
**iTerm2:** AppleScript `create tab with default profile`

### 3. Installer

One command. Copies files, registers the protocol handler, and appends the Stop hook to `~/.claude/settings.json`. Idempotent.

---

## Terminal support

OSC 8 is supported by all major terminal emulators:

| Terminal | Support |
|---|---|
| Windows Terminal | ✓ |
| iTerm2 | ✓ |
| Ghostty | ✓ |
| WezTerm | ✓ |
| Kitty | ✓ |
| Alacritty | ✓ |
| GNOME Terminal | ✓ |
| Others | Graceful degradation — plain text, no crash |

---

## Install

**Windows (PowerShell 7):**

```powershell
git clone https://github.com/inth3shadows/codeshot
cd codeshot
./install/install.ps1
```

Or one-liner (after release):
```powershell
irm https://raw.githubusercontent.com/inth3shadows/codeshot/main/install/install.ps1 | iex
```

**Mac:**

```bash
git clone https://github.com/inth3shadows/codeshot
cd codeshot
./install/install.sh
```

**Requirements:**
- Node.js ≥ 18 (already present if Claude Code is installed)
- Windows Terminal (Windows) or iTerm2 (Mac)
- Claude Code (paid tier required for hooks)

---

## RunEcho integration

Codeshot is a standalone companion to [RunEcho](https://github.com/inth3shadows/runecho) — a session governance layer for Claude Code.

If RunEcho's `.ai/faults.jsonl` is present in your project, Codeshot emits two signals:

| Signal | When | Purpose |
|---|---|---|
| `CODE_STAGED` | Hook fires, links displayed | Records that N code blocks were surfaced for manual review |
| `USER_EXEC` | User clicks a link, handler fires | Records that a specific block was staged for execution |

These signals feed RunEcho's M11 provenance export, closing the observability gap between autonomous Claude actions and human interventions. No RunEcho? No `.ai/` directory? Codeshot stays silent and does nothing extra.

---

## Uninstall

```powershell
# Windows
./install/install.ps1 -Uninstall
```

```bash
# Mac
# Remove ~/.codeshot and the Codeshot.app bundle, then remove the Stop hook from ~/.claude/settings.json
```

---

## Design decisions

**Why OSC 8 instead of a side panel?**
A side panel requires a separate Electron app to stay running, a launch step, and a second window to manage. OSC 8 links appear in your existing terminal inline with Claude's output — zero context switching, zero overhead.

**Why a Stop hook instead of a wrapper command?**
Wrapping `claude` means changing how you launch it — including in scripts, aliases, and CI. The Stop hook fires automatically with no changes to your workflow.

**Why a separate terminal tab instead of running in-place?**
The whole point is human review. A new tab gives you a clean context: the code is visible, nothing else is running, and you make a deliberate choice. Ctrl+C always works. This is intentionally different from clipboard copy — you don't have to remember to paste.

**Why not auto-detect the language and use the right shell?**
v1 uses PowerShell on Windows and bash on Mac for all blocks. Language-aware routing (python, node, etc.) is a v2 consideration.
