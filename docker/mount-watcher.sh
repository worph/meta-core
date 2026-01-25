#!/bin/bash
# =============================================================================
# Mount Watcher Script
#
# Monitors /meta-core/mounts/mounts.json and manages NFS, SMB, and rclone mounts.
# Runs as a supervisord program, polling every 5 seconds.
#
# Uses findmnt to check actual mount status (source of truth).
# Writes errors to /meta-core/mounts/errors/{id}.error
# =============================================================================

set -u

CONFIG_DIR="${META_CORE_PATH:-/meta-core}/mounts"
CONFIG_FILE="$CONFIG_DIR/mounts.json"
ERROR_DIR="$CONFIG_DIR/errors"
FILES_PATH="${FILES_PATH:-/files}"
POLL_INTERVAL="${MOUNT_POLL_INTERVAL:-5}"

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

# Check if path is mounted
is_mounted() {
    local mount_path="$1"
    findmnt -n "$mount_path" > /dev/null 2>&1
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

# Mount NFS share (always read-only)
do_mount_nfs() {
    local id="$1"
    local server="$2"
    local path="$3"
    local mount_path="$4"
    local options="${5:-}"

    mkdir -p "$mount_path"

    # Always mount read-only for safety
    local mount_opts="ro"
    [ -n "$options" ] && mount_opts="${mount_opts},${options}"

    log "Mounting NFS (read-only): ${server}:${path} -> ${mount_path}"
    output=$(mount -t nfs -o "${mount_opts}" "${server}:${path}" "${mount_path}" 2>&1)
    local rc=$?

    if [ $rc -eq 0 ]; then
        clear_error "$id"
        log_success "NFS mount successful: $mount_path"
    else
        write_error "$id" "$output"
        log_error "NFS mount failed: $output"
    fi
    return $rc
}

# Mount SMB/CIFS share (always read-only)
do_mount_smb() {
    local id="$1"
    local server="$2"
    local share="$3"
    local mount_path="$4"
    local username="${5:-}"
    local password_obscured="${6:-}"
    local domain="${7:-}"
    local options="${8:-}"

    mkdir -p "$mount_path"

    # Always mount read-only for safety
    local mount_opts="ro"

    # If we have credentials, reveal password and build options
    if [ -n "$username" ]; then
        mount_opts="${mount_opts},username=${username}"

        if [ -n "$password_obscured" ]; then
            # Reveal password using rclone (only exists in memory briefly)
            local password
            password=$(rclone reveal "$password_obscured" 2>/dev/null)
            if [ -n "$password" ]; then
                mount_opts="${mount_opts},password=${password}"
            fi
        fi

        if [ -n "$domain" ]; then
            mount_opts="${mount_opts},domain=${domain}"
        fi
    fi

    # Add any additional options
    if [ -n "$options" ]; then
        if [ -n "$mount_opts" ]; then
            mount_opts="${mount_opts},${options}"
        else
            mount_opts="$options"
        fi
    fi

    log "Mounting SMB (read-only): //${server}/${share} -> ${mount_path}"

    # Execute mount directly (not via eval) to handle special chars in password
    local output
    local rc
    if [ -n "$mount_opts" ]; then
        output=$(mount -t cifs "//${server}/${share}" "${mount_path}" -o "${mount_opts}" 2>&1)
        rc=$?
    else
        output=$(mount -t cifs "//${server}/${share}" "${mount_path}" 2>&1)
        rc=$?
    fi

    # Clear password from any variables (best effort)
    password=""
    mount_opts=""

    if [ $rc -eq 0 ]; then
        clear_error "$id"
        log_success "SMB mount successful: $mount_path"
    else
        write_error "$id" "$output"
        log_error "SMB mount failed: $output"
    fi
    return $rc
}

# Mount rclone remote using RC API (always read-only)
do_mount_rclone() {
    local id="$1"
    local remote="$2"
    local remote_path="$3"
    local mount_path="$4"
    local options="${5:-}"

    mkdir -p "$mount_path"

    # Build the remote source string
    local fs="${remote}:${remote_path}"

    # Use rclone RC API to mount (always read-only for safety)
    log "Mounting rclone (read-only): ${fs} -> ${mount_path}"

    # Build JSON body for rclone mount with read-only option
    local json_body
    json_body=$(cat <<EOF
{
    "fs": "${fs}",
    "mountPoint": "${mount_path}",
    "mountOpt": {
        "AllowOther": true,
        "ReadOnly": true
    },
    "vfsOpt": {
        "CacheMode": 2,
        "ReadOnly": true
    }
}
EOF
)

    output=$(curl -s -X POST \
        -H "Content-Type: application/json" \
        -u admin:admin \
        -d "$json_body" \
        "http://127.0.0.1:5572/mount/mount" 2>&1)

    # Wait a moment for mount to establish
    sleep 2

    if is_mounted "$mount_path"; then
        clear_error "$id"
        log_success "rclone mount successful: $mount_path"
        return 0
    else
        local error_msg="rclone mount failed"
        # Try to extract error from response
        if echo "$output" | grep -q "error"; then
            error_msg="$output"
        fi
        write_error "$id" "$error_msg"
        log_error "rclone mount failed: $error_msg"
        return 1
    fi
}

# Unmount a path
do_unmount() {
    local id="$1"
    local mount_path="$2"
    local type="$3"

    if ! is_mounted "$mount_path"; then
        log "Already unmounted: $mount_path"
        return 0
    fi

    if [ "$type" = "rclone" ]; then
        # Use rclone RC API to unmount
        log "Unmounting rclone: $mount_path"
        curl -s -X POST \
            -H "Content-Type: application/json" \
            -u admin:admin \
            -d "{\"mountPoint\":\"${mount_path}\"}" \
            "http://127.0.0.1:5572/mount/unmount" > /dev/null 2>&1
    else
        log "Unmounting: $mount_path"
        umount "$mount_path" 2>&1
    fi

    # Wait a moment
    sleep 1

    # Check if still mounted
    if is_mounted "$mount_path"; then
        # Force unmount with lazy option
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
    # Check if config file exists
    if [ ! -f "$CONFIG_FILE" ]; then
        return
    fi

    # Parse JSON using python3
    local mounts
    mounts=$(python3 -c '
import sys, json

try:
    with open("'"$CONFIG_FILE"'", "r") as f:
        data = json.load(f)
except:
    sys.exit(0)

for m in data.get("mounts", []):
    # Output pipe-separated values
    fields = [
        m.get("id", ""),
        m.get("type", ""),
        str(m.get("enabled", True)),
        str(m.get("desiredMounted", False)),
        m.get("mountPath", ""),
        m.get("nfsServer", ""),
        m.get("nfsPath", ""),
        m.get("smbServer", ""),
        m.get("smbShare", ""),
        m.get("smbUsername", ""),
        m.get("smbPasswordObscured", ""),
        m.get("smbDomain", ""),
        m.get("rcloneRemote", ""),
        m.get("rclonePath", ""),
        m.get("options", "")
    ]
    print("|".join(fields))
' 2>/dev/null)

    if [ -z "$mounts" ]; then
        return
    fi

    while IFS='|' read -r id type enabled desired mount_path nfs_server nfs_path smb_server smb_share smb_username smb_password_obscured smb_domain rclone_remote rclone_path options; do
        # Skip empty lines
        [ -z "$id" ] && continue

        # Check actual mount status
        local is_mounted_now=false
        is_mounted "$mount_path" && is_mounted_now=true

        # Handle disabled mounts - unmount if currently mounted
        if [ "$enabled" != "True" ] && [ "$enabled" != "true" ]; then
            if [ "$is_mounted_now" = true ]; then
                log "Mount $id ($mount_path) disabled, unmounting..."
                do_unmount "$id" "$mount_path" "$type"
            fi
            continue
        fi

        # Handle desired state
        if [ "$desired" = "True" ] || [ "$desired" = "true" ]; then
            # Should be mounted
            if [ "$is_mounted_now" = false ]; then
                log "Mount $id ($mount_path) desired but not mounted, mounting..."
                case "$type" in
                    nfs)
                        do_mount_nfs "$id" "$nfs_server" "$nfs_path" "$mount_path" "$options"
                        ;;
                    smb)
                        do_mount_smb "$id" "$smb_server" "$smb_share" "$mount_path" "$smb_username" "$smb_password_obscured" "$smb_domain" "$options"
                        ;;
                    rclone)
                        do_mount_rclone "$id" "$rclone_remote" "$rclone_path" "$mount_path" "$options"
                        ;;
                    *)
                        log_error "Unknown mount type: $type"
                        ;;
                esac
            fi
        else
            # Should be unmounted
            if [ "$is_mounted_now" = true ]; then
                log "Mount $id ($mount_path) not desired, unmounting..."
                do_unmount "$id" "$mount_path" "$type"
            fi
        fi
    done <<< "$mounts"
}

# Main loop
main() {
    log "Starting mount watcher (poll interval: ${POLL_INTERVAL}s)"

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
