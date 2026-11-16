#!/usr/bin/env bash
# Builds a throwaway primary (5433) + async standby (5434) and a fake VIP (6432).
# Run as a normal user that can run PostgreSQL. Everything lives in $DEMO.
set -euo pipefail
DEMO=${DEMO:-/tmp/docbrown-demo}
PGBIN=${PGBIN:-$(pg_config --bindir)}
export DEMO

"$PGBIN/pg_ctl" -D "$DEMO/a" stop -m immediate >/dev/null 2>&1 || true
"$PGBIN/pg_ctl" -D "$DEMO/b" stop -m immediate >/dev/null 2>&1 || true
pkill -f "$DEMO/vip" 2>/dev/null || true
rm -rf "$DEMO"; mkdir -p "$DEMO/archive"

"$PGBIN/initdb" -D "$DEMO/a" -U postgres -A trust >/dev/null
cat >> "$DEMO/a/postgresql.conf" <<C
port = 5433
listen_addresses = '127.0.0.1'
unix_socket_directories = '$DEMO'
wal_level = replica
archive_mode = on
archive_command = 'cp %p $DEMO/archive/%f'
wal_log_hints = on
C
echo "host replication all 127.0.0.1/32 trust" >> "$DEMO/a/pg_hba.conf"
"$PGBIN/pg_ctl" -D "$DEMO/a" -l "$DEMO/a.log" start >/dev/null
"$PGBIN/pg_basebackup" -h 127.0.0.1 -p 5433 -U postgres -D "$DEMO/b" -R -X stream
sed -i.bak 's/^port = 5433/port = 5434/' "$DEMO/b/postgresql.conf"
"$PGBIN/pg_ctl" -D "$DEMO/b" -l "$DEMO/b.log" start >/dev/null
echo 5433 > "$DEMO/target"

(cd "$(dirname "$0")/vip" && go build -o "$DEMO/vip" .)
nohup "$DEMO/vip" >"$DEMO/vip.log" 2>&1 &

cat > "$DEMO/docbrown.yaml" <<Y
endpoint: "host=127.0.0.1 port=6432 dbname=postgres user=postgres sslmode=disable connect_timeout=2"
nodes:
  - name: node-a
    dsn: "host=127.0.0.1 port=5433 dbname=postgres user=postgres sslmode=disable connect_timeout=2"
  - name: node-b
    dsn: "host=127.0.0.1 port=5434 dbname=postgres user=postgres sslmode=disable connect_timeout=2"
runs_dir: $DEMO/runs
reports_dir: $DEMO/reports
Y
echo "Demo cluster ready. Config: $DEMO/docbrown.yaml"
