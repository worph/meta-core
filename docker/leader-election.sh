#!/bin/bash
# =============================================================================
# Leader Election Script for meta-core
#
# Uses flock for leader election. Only the leader runs the full application
# stack (supervisord with Redis, meta-core, nginx, rclone, mount-watcher).
# Followers remain dormant, periodically retrying to acquire the lock.
#
# Environment variables:
#   META_CORE_PATH         - Path to meta-core data directory (default: /meta-core)
#   META_CORE_HTTP_PORT    - Port for meta-core HTTP API (default: 9000)
#   ELECTION_RETRY_SECS    - Seconds between flock retry attempts (default: 5)
# =============================================================================

set -e

# Configuration
META_CORE_PATH="${META_CORE_PATH:-/meta-core}"
META_CORE_HTTP_PORT="${META_CORE_HTTP_PORT:-9000}"
ELECTION_RETRY_SECS="${ELECTION_RETRY_SECS:-5}"

LOCK_FILE="${META_CORE_PATH}/locks/kv-leader.lock"
INFO_FILE="${META_CORE_PATH}/locks/kv-leader.info"
SUPERVISORD_CONF="/etc/supervisor/conf.d/supervisord.conf"

# Get the container's IP address
# Note: Alpine uses BusyBox hostname which uses -i (lowercase) instead of -I
get_local_ip() {
    # Try hostname -i first (Alpine/BusyBox), then fallback to hostname -I (GNU)
    local ip
    ip=$(hostname -i 2>/dev/null | awk '{print $1}')
    if [ -z "$ip" ] || [ "$ip" = "127.0.0.1" ]; then
        # Fallback: parse /etc/hosts or use hostname
        ip=$(getent hosts "$(hostname)" 2>/dev/null | awk '{print $1}' | head -1)
    fi
    if [ -z "$ip" ]; then
        # Last resort: use hostname
        ip=$(hostname)
    fi
    echo "$ip"
}

# Write leader info file (plain text API URL)
write_leader_info() {
    local ip=$(get_local_ip)
    local api_url="http://${ip}:${META_CORE_HTTP_PORT}"

    # Write atomically using temp file + rename
    local temp_file="${INFO_FILE}.tmp"
    echo "$api_url" > "$temp_file"
    mv "$temp_file" "$INFO_FILE"

    echo "[election] Wrote leader info: $api_url"
}

# Clean up leader info file on shutdown
cleanup() {
    echo "[election] Cleaning up..."
    rm -f "$INFO_FILE"
    exit 0
}

# Set up signal handlers for graceful shutdown
trap cleanup SIGTERM SIGINT

# Ensure lock directory exists
mkdir -p "$(dirname "$LOCK_FILE")"

echo "[election] Starting leader election..."
echo "[election] Lock file: $LOCK_FILE"
echo "[election] Info file: $INFO_FILE"

# Open lock file for flock (file descriptor 200)
exec 200>"$LOCK_FILE"

# Main election loop
while true; do
    # Try non-blocking exclusive flock
    if flock -n 200; then
        echo "[election] Acquired flock - transitioning to LEADER"

        # Write leader info before starting services
        write_leader_info

        # exec supervisord - replaces this process
        # When supervisord exits, the flock is released automatically
        echo "[election] Starting supervisord..."
        exec /usr/bin/supervisord -c "$SUPERVISORD_CONF"
    else
        echo "[election] Lock held by another process - acting as FOLLOWER"
        echo "[election] Waiting ${ELECTION_RETRY_SECS}s before retry..."
        sleep "$ELECTION_RETRY_SECS"
    fi
done
