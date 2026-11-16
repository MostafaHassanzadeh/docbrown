#!/usr/bin/env bash
# An ugly failover: the standby stops receiving, the primary keeps acknowledging
# writes, then crashes. Expect lost writes, and WAL past the fork on node-a.
set -euo pipefail
DEMO=${DEMO:-/tmp/docbrown-demo}
PGBIN=${PGBIN:-$(pg_config --bindir)}
WR=$(pgrep -f 'postgres: walreceiver' | head -1)
kill -STOP "$WR"
sleep 1.5
"$PGBIN/pg_ctl" -D "$DEMO/a" stop -m immediate >/dev/null
kill -9 "$WR"
for _ in $(seq 10); do "$PGBIN/pg_ctl" -D "$DEMO/b" promote >/dev/null 2>&1 && break; sleep 0.3; done
sleep 0.3
echo 5434 > "$DEMO/target"
echo "node-a crashed, node-b promoted. Evidence for the report:"
echo "  $PGBIN/pg_controldata $DEMO/a > $DEMO/controldata.txt"
echo "  $PGBIN/pg_waldump --timeline=1 --start=\$(awk '{print \$2}' $DEMO/b/pg_wal/00000002.history) -p $DEMO/a/pg_wal > $DEMO/waldump.txt"
