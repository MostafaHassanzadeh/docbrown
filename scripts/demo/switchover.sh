#!/usr/bin/env bash
# A clean, planned switchover a -> b: expect zero lost writes.
set -euo pipefail
DEMO=${DEMO:-/tmp/docbrown-demo}
PGBIN=${PGBIN:-$(pg_config --bindir)}
"$PGBIN/pg_ctl" -D "$DEMO/a" stop -m fast >/dev/null
"$PGBIN/pg_ctl" -D "$DEMO/b" promote >/dev/null
sleep 0.3
echo 5434 > "$DEMO/target"
touch "$DEMO/a/standby.signal"
echo "primary_conninfo = 'host=127.0.0.1 port=5434 user=postgres'" >> "$DEMO/a/postgresql.auto.conf"
"$PGBIN/pg_ctl" -D "$DEMO/a" -l "$DEMO/a.log" start >/dev/null
echo "switched over to node-b"
