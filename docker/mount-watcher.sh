#!/bin/bash
# =============================================================================
# Mount Watcher Script
#
# Monitors /meta-core/mounts/mounts.json and manages mounts via the local
# rclone RC daemon. After the rclone-only consolidation, this script no
# longer shells out to mount.cifs / mount.nfs — every mount type is executed
# through rclone's mount/mount endpoint.
#
# Mount types:
#   - "smb"    → on-the-fly :smb: remote synthesised from SMB-* fields
#   - "rclone" → references a pre-defined remote in rclone.conf
#
# All mounts are read-only by construction (no API surface to override).
# Errors surface in /meta-core/mounts/errors/{id}.error
# =============================================================================

set -u

CONFIG_DIR="${META_CORE_PATH:-/meta-core}/mounts"
CONFIG_FILE="$CONFIG_DIR/mounts.json"
ERROR_DIR="$CONFIG_DIR/errors"
FILES_PATH="${FILES_PATH:-/files}"
POLL_INTERVAL="${MOUNT_POLL_INTERVAL:-5}"

# Fall-back VFS cache settings — applied when a mount entry leaves the field
# blank. Must stay in sync with DefaultCacheMaxSize/MaxAge/DirCacheTime in
# packages/meta-core/internal/mounts/types.go.
DEFAULT_CACHE_MAX_SIZE="50G"
DEFAULT_CACHE_MAX_AGE="24h"
DEFAULT_DIR_CACHE_TIME="5m"

RCLONE_RC_URL="${RCLONE_RC_URL:-http://127.0.0.1:5572}"
RCLONE_RC_AUTH="${RCLONE_RC_AUTH:-admin:admin}"

# ANSI colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

log() {
    echo -e "[$(date -Iseconds)] [${CYAN}mount-watcher${NC}] $1"
}

log_success() {
    echo -e "[$(date -Iseconds)] [${CYAN}mount-watcher${NC}] ${GREEN}$1${NC}"
}

log_warn() {
    echo -e "[$(date -Iseconds)] [${CYAN}mount-watcher${NC}] ${YELLOW}$1${NC}"
}

log_error() {
    echo -e "[$(date -Iseconds)] [${CYAN}mount-watcher${NC}] ${RED}$1${NC}"
}

# Initialize directories
init_dirs() {
    mkdir -p "$CONFIG_DIR" "$ERROR_DIR"

    # Initialize empty config if not exists
    if [ ! -f "$CONFIG_FILE" ]; then
        echo '{"version":1,"mounts":[]}' > "$CONFIG_FILE"
        log "Initialized empty mounts config"
    fi
}

# Check if path is mounted (uses /proc/mounts for Alpine compatibility)
is_mounted() {
    local mount_path="$1"
    local clean_path="${mount_path%/}"
    grep -q " ${clean_path} " /proc/mounts 2>/dev/null
}

# Clear error file for mount
clear_error() {
    local id="$1"
    rm -f "$ERROR_DIR/$id.error"
}

# Write error to file
write_error() {
    local id="$1"
    local error="$2"
    echo "$(date -Iseconds)" > "$ERROR_DIR/$id.error"
    echo "$error" >> "$ERROR_DIR/$id.error"
}

# Build the rclone remote source string for a mount entry. Echoes the result.
# For type=smb, synthesises an on-the-fly :smb: remote so we don't have to
# mutate rclone.conf at runtime. For type=rclone, joins the named remote with
# the optional remote path.
build_rclone_fs() {
    local type="$1"
    local smb_server="$2"
    local smb_share="$3"
    local smb_username="$4"
    local smb_password_obscured="$5"
    local smb_domain="$6"
    local rclone_remote="$7"
    local rclone_path="$8"

    case "$type" in
        smb)
            # On-the-fly remote: :<backend>,k1=v1,k2=v2:<path>
            # The obscured password is base64-ish (A-Za-z0-9+/_-=) so safe to
            # embed without quoting.
            local opts="host=${smb_server}"
            if [ -n "$smb_username" ]; then
                opts="${opts},user=${smb_username}"
            fi
            if [ -n "$smb_password_obscured" ]; then
                opts="${opts},pass=${smb_password_obscured}"
            fi
            if [ -n "$smb_domain" ]; then
                opts="${opts},domain=${smb_domain}"
            fi
            echo ":smb,${opts}:${smb_share}"
            ;;
        rclone)
            echo "${rclone_remote}:${rclone_path}"
            ;;
        *)
            echo ""
            ;;
    esac
}

# Mount via rclone RC API (always read-only).
do_mount_rclone() {
    local id="$1"
    local fs="$2"
    local mount_path="$3"
    local cache_max_size="$4"
    local cache_max_age="$5"
    local dir_cache_time="$6"

    mkdir -p "$mount_path"

    # Apply defaults for any blank cache fields.
    [ -z "$cache_max_size" ] && cache_max_size="$DEFAULT_CACHE_MAX_SIZE"
    [ -z "$cache_max_age" ]  && cache_max_age="$DEFAULT_CACHE_MAX_AGE"
    [ -z "$dir_cache_time" ] && dir_cache_time="$DEFAULT_DIR_CACHE_TIME"

    log "Mounting (rclone, read-only): ${fs} -> ${mount_path} (cache ${cache_max_size}/${cache_max_age}, dir-cache ${dir_cache_time})"

    # Build JSON body via python3 — embedding the obscured password and
    # display fs string into a hand-rolled heredoc is asking for an injection
    # bug the next time we add a field with a quote in it.
    local json_body
    json_body=$(python3 -c '
import json, sys
print(json.dumps({
    "fs": sys.argv[1],
    "mountPoint": sys.argv[2],
    "mountOpt": {
        "AllowOther": True,
        "ReadOnly": True,
    },
    "vfsOpt": {
        # CacheMode 3 = "full" — cache both reads and writes on local disk.
        # CacheMode 2 ("writes") would silently bypass the cache for reads,
        # which is the opposite of what we want for a read-only mount.
        "CacheMode": 3,
        "CacheMaxSize": sys.argv[3],
        "CacheMaxAge": sys.argv[4],
        "DirCacheTime": sys.argv[5],
        # Note: ChunkStreams (per-file parallel TCP) helps single-stream
        # throughput in isolation but, under the 4+ concurrent reads from
        # the processing pipeline, multiplies into 16+ simultaneous SMB
        # requests and tanks aggregate throughput. Left at default — rely on
        # file-level concurrency from the pipeline for parallelism instead.
        "ReadOnly": True,
    },
}))
' "$fs" "$mount_path" "$cache_max_size" "$cache_max_age" "$dir_cache_time")

    local output
    output=$(curl -s -X POST \
        -H "Content-Type: application/json" \
        -u "$RCLONE_RC_AUTH" \
        -d "$json_body" \
        "${RCLONE_RC_URL}/mount/mount" 2>&1)

    sleep 2

    if is_mounted "$mount_path"; then
        clear_error "$id"
        log_success "rclone mount successful: $mount_path"
        return 0
    else
        local error_msg="rclone mount failed"
        if echo "$output" | grep -q "error"; then
            error_msg="$output"
        fi
        write_error "$id" "$error_msg"
        log_error "rclone mount failed: $error_msg"
        return 1
    fi
}

# Unmount a path. Always goes through the rclone RC API now — every mount we
# create lives behind rclone's FUSE driver.
do_unmount() {
    local id="$1"
    local mount_path="$2"

    if ! is_mounted "$mount_path"; then
        log "Already unmounted: $mount_path"
        return 0
    fi

    log "Unmounting: $mount_path"
    curl -s -X POST \
        -H "Content-Type: application/json" \
        -u "$RCLONE_RC_AUTH" \
        -d "{\"mountPoint\":\"${mount_path}\"}" \
        "${RCLONE_RC_URL}/mount/unmount" > /dev/null 2>&1

    sleep 1

    # Fallback to lazy umount in case the FUSE process is wedged.
    if is_mounted "$mount_path"; then
        log_warn "Force unmounting (lazy): $mount_path"
        umount -l "$mount_path" 2>&1
        sleep 1
    fi

    if is_mounted "$mount_path"; then
        log_error "Failed to unmount: $mount_path"
        return 1
    fi

    log_success "Unmounted: $mount_path"
    return 0
}

# Process all mounts from config
process_mounts() {
    if [ ! -f "$CONFIG_FILE" ]; then
        return
    fi

    # Parse JSON using python3 — pipe-separated fields, NFS dropped.
    local mounts
    mounts=$(python3 -c '
import sys, json

try:
    with open("'"$CONFIG_FILE"'", "r") as f:
        data = json.load(f)
except:
    sys.exit(0)

for m in data.get("mounts", []):
    fields = [
        m.get("id", ""),
        m.get("type", ""),
        str(m.get("enabled", True)),
        str(m.get("desiredMounted", False)),
        m.get("mountPath", ""),
        m.get("smbServer", ""),
        m.get("smbShare", ""),
        m.get("smbUsername", ""),
        m.get("smbPasswordObscured", ""),
        m.get("smbDomain", ""),
        m.get("rcloneRemote", ""),
        m.get("rclonePath", ""),
        m.get("cacheMaxSize", ""),
        m.get("cacheMaxAge", ""),
        m.get("dirCacheTime", ""),
    ]
    print("|".join(fields))
' 2>/dev/null)

    if [ -z "$mounts" ]; then
        return
    fi

    while IFS='|' read -r id type enabled desired mount_path \
        smb_server smb_share smb_username smb_password_obscured smb_domain \
        rclone_remote rclone_path \
        cache_max_size cache_max_age dir_cache_time; do
        # Skip empty lines
        [ -z "$id" ] && continue

        # Skip retired/legacy types — the Go-side migration disables NFS but a
        # stale entry could still arrive here on a downgrade/upgrade race.
        if [ "$type" != "smb" ] && [ "$type" != "rclone" ]; then
            log_warn "Skipping unsupported mount type '$type' (id=$id) — only smb and rclone are supported"
            continue
        fi

        local is_mounted_now=false
        is_mounted "$mount_path" && is_mounted_now=true

        # Disabled → unmount if currently up
        if [ "$enabled" != "True" ] && [ "$enabled" != "true" ]; then
            if [ "$is_mounted_now" = true ]; then
                log "Mount $id ($mount_path) disabled, unmounting..."
                do_unmount "$id" "$mount_path"
            fi
            continue
        fi

        if [ "$desired" = "True" ] || [ "$desired" = "true" ]; then
            # Should be mounted
            if [ "$is_mounted_now" = false ]; then
                local fs
                fs=$(build_rclone_fs "$type" \
                    "$smb_server" "$smb_share" "$smb_username" \
                    "$smb_password_obscured" "$smb_domain" \
                    "$rclone_remote" "$rclone_path")

                if [ -z "$fs" ]; then
                    log_error "Could not build rclone fs spec for mount $id (type=$type)"
                    write_error "$id" "Invalid mount config: missing required fields for type=$type"
                    continue
                fi

                log "Mount $id ($mount_path) desired but not mounted, mounting..."
                do_mount_rclone "$id" "$fs" "$mount_path" \
                    "$cache_max_size" "$cache_max_age" "$dir_cache_time"
            fi
        else
            # Should be unmounted
            if [ "$is_mounted_now" = true ]; then
                log "Mount $id ($mount_path) not desired, unmounting..."
                do_unmount "$id" "$mount_path"
            fi
        fi
    done <<< "$mounts"
}

# Main loop
main() {
    log "Starting mount watcher (poll interval: ${POLL_INTERVAL}s, rclone-only)"

    init_dirs

    while true; do
        process_mounts
        sleep "$POLL_INTERVAL"
    done
}

# Handle signals for graceful shutdown
cleanup() {
    log "Shutting down mount watcher..."
    exit 0
}

trap cleanup SIGTERM SIGINT

main "$@"
