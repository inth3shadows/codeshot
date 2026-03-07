#!/usr/bin/env bash
# Codeshot installer — macOS (iTerm2)
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/inth3shadows/codeshot/main/install/install.sh | bash
#   # or from a local clone:
#   ./install/install.sh
set -euo pipefail

INSTALL_DIR="$HOME/.codeshot"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"

HOOK_SRC="$REPO_ROOT/hook/hook.js"
HANDLER_SRC="$REPO_ROOT/handler/handler.applescript"

HOOK_DST="$INSTALL_DIR/hook/hook.js"
HANDLER_APP="$INSTALL_DIR/Codeshot.app"
SETTINGS="$HOME/.claude/settings.json"

# ── Helpers ────────────────────────────────────────────────────────────────

log()  { printf '\033[36m  %s\033[0m\n' "$*"; }
ok()   { printf '\033[32m%s\033[0m\n' "$*"; }
fail() { printf '\033[31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }

require() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not installed."; }

# ── Prerequisites ──────────────────────────────────────────────────────────

require node
require jq

if ! osascript -e 'application "iTerm" exists' 2>/dev/null | grep -q true; then
    echo "WARNING: iTerm2 not detected. Install iTerm2 for full functionality."
    echo "         https://iterm2.com"
fi

# ── Copy files ─────────────────────────────────────────────────────────────

mkdir -p "$INSTALL_DIR/hook"
cp "$HOOK_SRC" "$HOOK_DST"
log "Copied hook.js to $INSTALL_DIR"

# ── Register codeshot:// protocol handler via minimal .app bundle ──────────

# Create a minimal .app that macOS will call for codeshot:// URLs
APP_CONTENTS="$HANDLER_APP/Contents"
mkdir -p "$APP_CONTENTS/MacOS"
mkdir -p "$APP_CONTENTS/Resources"

# Info.plist registers the URL scheme
cat > "$APP_CONTENTS/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
    "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleIdentifier</key>
    <string>com.inth3shadows.codeshot</string>
    <key>CFBundleName</key>
    <string>Codeshot</string>
    <key>CFBundleExecutable</key>
    <string>codeshot-handler</string>
    <key>CFBundleURLTypes</key>
    <array>
        <dict>
            <key>CFBundleURLName</key>
            <string>Codeshot Protocol</string>
            <key>CFBundleURLSchemes</key>
            <array>
                <string>codeshot</string>
            </array>
        </dict>
    </array>
    <key>LSUIElement</key>
    <true/>
</dict>
</plist>
PLIST

# Executable that receives the URL and opens an iTerm2 tab
cat > "$APP_CONTENTS/MacOS/codeshot-handler" <<'HANDLER'
#!/usr/bin/env bash
# Receives: codeshot://run?code=...&cwd=... or codeshot://file?path=...&cwd=...
URI="$1"

# Decode URL-encoded string
urldecode() { python3 -c "import sys, urllib.parse; print(urllib.parse.unquote(sys.argv[1]))" "$1"; }

HOST=$(echo "$URI" | sed 's|codeshot://\([^?]*\).*|\1|')
QUERY=$(echo "$URI" | sed 's|[^?]*?||')

# Parse cwd
CWD=$(echo "$QUERY" | tr '&' '\n' | grep '^cwd=' | sed 's/^cwd=//')
CWD=$(urldecode "$CWD")
[ -z "$CWD" ] && CWD="$HOME"

# Get code
if [ "$HOST" = "file" ]; then
    FILE=$(echo "$QUERY" | tr '&' '\n' | grep '^path=' | sed 's/^path=//')
    FILE=$(urldecode "$FILE")
    CODE=$(cat "$FILE")
elif [ "$HOST" = "run" ]; then
    CODE=$(echo "$QUERY" | tr '&' '\n' | grep '^code=' | sed 's/^code=//')
    CODE=$(urldecode "$CODE")
fi

[ -z "$CODE" ] && exit 0

# Write launcher script
TMP=$(mktemp /tmp/codeshot-XXXXXX.sh)
cat > "$TMP" <<SCRIPT
cd '$CWD' 2>/dev/null || true
echo ''
echo ' Codeshot  Review then press Enter to execute  (Ctrl+C to cancel)'
echo ''
cat << 'CODEOF'
$CODE
CODEOF
echo ''
echo '----------------------------------------------------'
read -r _
eval '$CODE'
rm -f '$TMP'
SCRIPT
chmod +x "$TMP"

# Open new iTerm2 tab with the launcher
osascript <<APPLESCRIPT
tell application "iTerm"
    if (count of windows) = 0 then
        create window with default profile
    end if
    tell current window
        create tab with default profile
        tell current session
            write text "bash '$TMP'"
        end tell
    end tell
end tell
APPLESCRIPT
HANDLER
chmod +x "$APP_CONTENTS/MacOS/codeshot-handler"

# Register the app bundle with Launch Services
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister \
    -f "$HANDLER_APP" 2>/dev/null || true
log "Registered codeshot:// protocol handler (Codeshot.app)"

# ── Add Stop hook to ~/.claude/settings.json ──────────────────────────────

mkdir -p "$(dirname "$SETTINGS")"
[ -f "$SETTINGS" ] || echo '{}' > "$SETTINGS"

HOOK_CMD="node \"$HOOK_DST\""

# Check if already registered
ALREADY=$(jq -r '
  .hooks.Stop // [] |
  map(.hooks // []) | add // [] |
  map(select(.command | test("codeshot"))) | length
' "$SETTINGS" 2>/dev/null || echo 0)

if [ "$ALREADY" -gt 0 ]; then
    log "Stop hook already registered (skipped)"
else
    HOOK_ENTRY=$(jq -n --arg cmd "$HOOK_CMD" '{
        matcher: "",
        hooks: [{ type: "command", command: $cmd }]
    }')

    TMP_SETTINGS=$(mktemp)
    jq --argjson entry "$HOOK_ENTRY" '
        .hooks.Stop = ((.hooks.Stop // []) + [$entry])
    ' "$SETTINGS" > "$TMP_SETTINGS" && mv "$TMP_SETTINGS" "$SETTINGS"
    log "Added Stop hook to $SETTINGS"
fi

echo ''
ok "Codeshot installed."
echo "Start a new Claude Code session and click any code block to try it."
