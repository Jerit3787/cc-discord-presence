#!/bin/bash
# Automatic statusline setup for cc-discord-presence

set -euo pipefail

CLAUDE_DIR="$HOME/.claude"
SETTINGS_FILE="$CLAUDE_DIR/settings.json"
WRAPPER_SRC="$(cd "$(dirname "$0")" && pwd)/statusline-wrapper.sh"
WRAPPER_DEST="$CLAUDE_DIR/statusline-wrapper.sh"

echo "🔧 Setting up statusline integration for cc-discord-presence..."

if ! command -v jq &> /dev/null; then
    echo ""
    echo "⚠️  jq not found. Please manually add to $SETTINGS_FILE:"
    echo '  "statusLine": {"command": "~/.claude/statusline-wrapper.sh", "type": "command"}'
    exit 1
fi

if [[ ! -f "$WRAPPER_SRC" ]]; then
    echo "Error: statusline wrapper not found at $WRAPPER_SRC" >&2
    exit 1
fi

mkdir -p "$CLAUDE_DIR"
if [[ ! -f "$SETTINGS_FILE" ]]; then
    echo "Creating $SETTINGS_FILE..."
    printf '{}\n' > "$SETTINGS_FILE"
fi

# Back up the whole statusLine object, not just a script path. This preserves
# commands with arguments, inline jq, and extra keys such as padding.
if ! EXISTING_STATUS=$(jq -c '.statusLine // empty' "$SETTINGS_FILE"); then
    echo "Error: $SETTINGS_FILE is not valid JSON" >&2
    exit 1
fi
if [[ -n "$EXISTING_STATUS" ]]; then
    EXISTING_CMD=$(jq -r '.command // empty' <<< "$EXISTING_STATUS")
    if [[ "$EXISTING_CMD" != *"statusline-wrapper.sh" ]]; then
        BACKUP="$CLAUDE_DIR/statusline.settings.json"
        printf '%s\n' "$EXISTING_STATUS" > "$BACKUP"
        echo "✓ Backed up existing statusLine config to $BACKUP"

        # Copy a simple standalone script as well, but never overwrite an
        # existing ~/.claude/statusline.sh.
        if [[ -n "$EXISTING_CMD" ]]; then
            case "$EXISTING_CMD" in
                *" "*|*"|"*|*">"*|*"<"*|*'$'*) ;;
                *)
                    EXPANDED_CMD="${EXISTING_CMD/#\~/$HOME}"
                    if [[ -f "$EXPANDED_CMD" ]]; then
                        DEST="$CLAUDE_DIR/statusline.sh"
                        if [[ -e "$DEST" ]]; then
                            DEST="$CLAUDE_DIR/statusline.sh.bak"
                        fi
                        cp -p "$EXPANDED_CMD" "$DEST"
                        chmod +x "$DEST"
                        echo "✓ Copied existing statusline script to $DEST"
                    fi
                    ;;
            esac
        fi
    fi
fi

cp "$WRAPPER_SRC" "$WRAPPER_DEST"
chmod +x "$WRAPPER_DEST"
echo "✓ Copied statusline wrapper to $WRAPPER_DEST"

# Merge only command/type so other statusLine keys are preserved. Write through
# the original file so its mode and symlink (if any) are kept.
TMP_FILE=$(mktemp "$SETTINGS_FILE.XXXXXX")
trap 'rm -f "$TMP_FILE"' EXIT
if ! jq '
    .statusLine = (
        (if (.statusLine | type) == "object" then .statusLine else {} end)
        + {"command": "~/.claude/statusline-wrapper.sh", "type": "command"}
    )
' "$SETTINGS_FILE" > "$TMP_FILE"; then
    echo "Error: could not update $SETTINGS_FILE (is it valid JSON?)" >&2
    exit 1
fi
cat "$TMP_FILE" > "$SETTINGS_FILE"
rm -f "$TMP_FILE"
trap - EXIT

echo "✓ Updated $SETTINGS_FILE"
echo ""
echo "✅ Setup complete! Restart Claude Code to apply changes."
