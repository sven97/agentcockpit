#!/bin/sh
set -e

# Restore the SQLite database from GCS if a replica exists.
# On first boot or after a crash, this re-hydrates the DB from Litestream.
# The -if-replica-exists flag makes this a no-op when GCS has no data yet.
litestream restore \
  -config /etc/litestream.yml \
  -if-replica-exists \
  /data/agentcockpit.db

# Run agentcockpit under litestream.
# Litestream continuously replicates WAL changes to GCS and forwards
# signals to the subprocess so graceful shutdown works correctly.
exec litestream replicate \
  -config /etc/litestream.yml \
  -exec "/agentcockpit serve"
