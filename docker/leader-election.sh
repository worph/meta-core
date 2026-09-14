#!/bin/bash
# =============================================================================
# Leader Election Script for meta-core
#
# The flock here is a MUTEX, not service discovery. It is the only thing
# stopping two containers that mount the same /meta-core volume from putting
# two Redis writers on one RDB/AOF. Sibling services no longer read anything
# from this directory — they locate meta-core over UDP (meta-discovery v1, see
# docs/project-architecture/service-discovery.md), so the kv-leader.info file
# this script used to publish is gone. Keep the lock; it costs nothing and the
# corruption it prevents is unrecoverable.
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

# Set up signal handlers for graceful shutdown

# Ensure lock directory exists
mkdir -p "$(dirname "$LOCK_FILE")"

echo "[election] Starting leader election..."
echo "[election] Lock file: $LOCK_FILE"

# Open lock file for flock (file descriptor 200)
exec 200>"$LOCK_FILE"

# Main election loop
while true; do
    # Try non-blocking exclusive flock
    if flock -n 200; then
        echo "[election] Acquired flock - transitioning to LEADER"

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
