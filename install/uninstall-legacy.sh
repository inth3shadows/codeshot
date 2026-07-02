#!/usr/bin/env bash
# Removes leftovers from Codeshot's retired terminal protocol-handler design (macOS).
#
# Codeshot no longer installs a codeshot:// protocol handler or a Claude Code
# Stop hook — see README.md's "Status" section. If you installed that older
# design (via install/install.sh, removed from the repo), this script undoes
# it:
#   1. Unregisters Codeshot.app from Launch Services
#   2. Removes the Stop hook entry from ~/.claude/settings.json
#   3. Removes the ~/.codeshot install directory
#
# Safe to run even if you never installed the old design — each step is a
# no-op when there's nothing to remove.
#
# Usage:
#   ./install/uninstall-legacy.sh
set -euo pipefail

INSTALL_DIR="$HOME/.codeshot"
HANDLER_APP="$INSTALL_DIR/Codeshot.app"
SETTINGS="$HOME/.claude/settings.json"
LSREGISTER="/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"

log() { printf '\033[36m  %s\033[0m\n' "$*"; }
ok()  { printf '\033[32m%s\033[0m\n' "$*"; }

echo "Removing legacy Codeshot protocol-handler install..."

# Unregister the app bundle from Launch Services
if [ -d "$HANDLER_APP" ] && [ -x "$LSREGISTER" ]; then
    "$LSREGISTER" -u "$HANDLER_APP" 2>/dev/null || true
    log "Unregistered Codeshot.app from Launch Services"
else
    log "No Codeshot.app found to unregister (skipped)"
fi

# Remove Stop hook from settings.json
if [ -f "$SETTINGS" ] && command -v jq >/dev/null 2>&1; then
    MATCHES=$(jq -r '
      .hooks.Stop // [] |
      map(.hooks // []) | add // [] |
      map(select(.command | test("codeshot"))) | length
    ' "$SETTINGS" 2>/dev/null || echo 0)

    if [ "$MATCHES" -gt 0 ]; then
        TMP_SETTINGS=$(mktemp)
        jq '
          .hooks.Stop = ((.hooks.Stop // []) | map(select(
            (.hooks // []) | map(select(.command | test("codeshot"))) | length == 0
          )))
        ' "$SETTINGS" > "$TMP_SETTINGS" && mv "$TMP_SETTINGS" "$SETTINGS"
        log "Removed Stop hook from ~/.claude/settings.json"
    else
        log "No matching Stop hook found (skipped)"
    fi
else
    log "No ~/.claude/settings.json (or jq missing) — skipped"
fi

# Remove install directory
if [ -d "$INSTALL_DIR" ]; then
    rm -rf "$INSTALL_DIR"
    log "Removed $INSTALL_DIR"
else
    log "$INSTALL_DIR not found (skipped)"
fi

echo ''
ok 'Done.'
