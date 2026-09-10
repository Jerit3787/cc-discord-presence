#!/bin/bash
# Statusline wrapper for Discord Rich Presence.
#
# Claude Code pipes a JSON status blob to this script on every render. We stash
# it for the Discord daemon (which reads it for accurate token/cost data), then
# render a status line:
#   - if ~/.claude/statusline.sh exists, delegate to it (your own status line)
#   - otherwise print a compact default so the status line isn't left blank

DATA_FILE="$HOME/.claude/discord-presence-data.json"
ORIGINAL_STATUSLINE="$HOME/.claude/statusline.sh"

# Read JSON from stdin
json_data=$(cat)

# Save for Discord presence (atomic write)
printf '%s' "$json_data" > "${DATA_FILE}.tmp" && mv "${DATA_FILE}.tmp" "$DATA_FILE"

# Delegate to a user-provided status line if there is one.
if [[ -x "$ORIGINAL_STATUSLINE" ]]; then
    printf '%s' "$json_data" | "$ORIGINAL_STATUSLINE"
    exit 0
fi

# Default status line. Uses jq when available, falls back to a bare string.
if command -v jq &> /dev/null; then
    printf '%s' "$json_data" | jq -r '
        [ (.workspace.current_dir // .cwd // "" | sub("^" + (env.HOME // "~"); "~"))
        , (.model.display_name // .model.id // "")
        , (if (.cost.total_cost_usd // 0) > 0 then "$" + (.cost.total_cost_usd * 10000 | round / 10000 | tostring) else empty end)
        ] | map(select(. != "" and . != null)) | join("  |  ")
    ' 2>/dev/null && exit 0
fi

printf 'Claude Code'
