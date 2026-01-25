#!/bin/bash
# =============================================================================
# meta-core Entrypoint Script
#
# Initializes directories and starts supervisord
# =============================================================================

set -e

# Create required directories
mkdir -p /meta-core/locks
mkdir -p /meta-core/db/redis
mkdir -p /meta-core/services
mkdir -p /meta-core/mounts/errors
mkdir -p /files
mkdir -p /var/log/supervisor
mkdir -p /var/log/nginx
mkdir -p /var/run/supervisor

# Ensure proper permissions
chmod 755 /meta-core
chmod 755 /files

# Initialize mounts config if not exists
if [ ! -f /meta-core/mounts/mounts.json ]; then
    echo '{"version":1,"mounts":[]}' > /meta-core/mounts/mounts.json
fi

echo "[entrypoint] meta-core starting..."
echo "[entrypoint] META_CORE_PATH=${META_CORE_PATH:-/meta-core}"
echo "[entrypoint] FILES_PATH=${FILES_PATH:-/files}"
echo "[entrypoint] SERVICE_NAME=${SERVICE_NAME:-meta-core}"
echo "[entrypoint] WATCH_FOLDER_LIST=${WATCH_FOLDER_LIST:-/files/}"

# Execute the main command (supervisord)
exec "$@"
