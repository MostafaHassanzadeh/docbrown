# docbrown

> Every switchover splits time in two. Doc Brown goes back to the fork and brings home what got left behind.

**docbrown** measures what a PostgreSQL switchover really costs. It tells you how long your clients were actually down, where the timeline forked, and exactly which writes were lost on the way, row by row.

> **Status:** early development. Run it on a test cluster first, never straight on production.

---

## Why

Your HA tool says the switchover "took 4 seconds". But:

- How long could your **application** not write?
- Did the old primary commit anything the new primary never received?
- If it did, **which rows**?
- How long were you running **without a replica** afterwards?

docbrown answers these questions with evidence. It doesn't guess.

## How it works

1. **Before:** `docbrown pre` records the state of every node (role, LSN, timeline, replication, WAL archiver) and creates one small table, `docbrown_probe`.
2. **During:** `docbrown probe` writes a numbered row every 50 ms **through the same endpoint your applications use**, reads through it too, and polls every node directly. Every result is logged **on disk, outside the database**:
   - `ack`: the commit was confirmed (with the server, WAL LSN and timeline that answered)
   - `fail`: definitely not committed (rejected by the server, or never sent)
   - `in-doubt`: sent, but no reliable answer came back
3. **After:** `docbrown report` checks every probe row against the new primary, reads the new timeline's `.history` file, and writes a report.

A write that was **acknowledged** but is **missing** on the new primary is lost data. That's the real definition: the client was told "saved", but the row is gone.

docbrown only uses PostgreSQL itself, so it works with Patroni, repmgr, pg_auto_failover or a manual `pg_ctl promote`.

## What it measures

| Metric | How |
|---|---|
| Write downtime (client view) | End of the last acknowledged write before → end of the first one after |
| Read downtime | Same, with a separate `SELECT` probe |
| Switchover timeline | Old primary last seen as primary → new primary first seen as primary → first write acknowledged on the new timeline |
| Lost writes | Exact sequence numbers of acknowledged probe rows missing on the new primary |
| In-doubt writes | Which unanswered writes actually landed (an app retrying them would duplicate them) |
| Phantom writes | Writes reported as failed that are on the new primary anyway |
| Timeline fork | Fork LSN from the `.history` file vs. the highest LSN seen on the old primary |
| Lost transactions (application data) | Optional: `pg_waldump` of the old primary past the fork → committed transactions, tables, operation counts |
| Time without a replica | Promotion → a standby streaming with lag under `healthy_lag_bytes` |
| Old primary back as replica | Promotion → old primary streaming from the new one |
| PITR gap | Promotion → first file archived by the new primary |

Node states are polled every 250 ms by default, so the timeline is accurate to about that much. Write and read downtime come from the 50 ms probe.

See [docs/example-report.md](docs/example-report.md) for a real report from the crash demo below.

## Install

Requires Go 1.22+ to build.

```bash
go install github.com/MostafaHassanzadeh/docbrown/cmd/docbrown@latest
# or, from a clone:
go build -o docbrown ./cmd/docbrown
```

## Quick start

```bash
cp docbrown.example.yaml docbrown.yaml   # then edit it

# 1. Snapshot the cluster and create docbrown_probe
docbrown pre

# 2. Start the probe and keep it running
docbrown probe

# 3. In another terminal, run your switchover
patronictl switchover

# 4. When the cluster has settled, stop the probe (Ctrl+C) and build the report
docbrown report
```

The report is written to `reports/<run-id>.md`, with every number also in `reports/<run-id>.json`.

`docbrown report` exits with code **2** when acknowledged writes were lost, so you can use it in scripts and CI.

### When the old primary went past the fork

If the report says the old primary wrote past the fork point, collect evidence **on the old primary, before it is rewound or rebuilt**:

```bash
pg_controldata "$PGDATA" > controldata.txt
pg_waldump --timeline=<old tli> --start=<fork LSN> -p "$PGDATA/pg_wal" > waldump.txt
docbrown report --controldata controldata.txt --waldump waldump.txt
```

The report prints these commands with the right timeline and LSN filled in. If your monitoring user can't read the new primary's `.history` file, copy it from `pg_wal/` or your WAL archive and pass `--history 00000002.history`.

## Configuration

```yaml
# docbrown.yaml (libpq key=value connection strings)
endpoint: "host=db.example.internal port=5432 dbname=postgres user=docbrown sslmode=prefer connect_timeout=2"
nodes:
  - name: node-a
    dsn: "host=10.0.1.10 port=5432 dbname=postgres user=docbrown sslmode=prefer connect_timeout=2"
  - name: node-b
    dsn: "host=10.0.2.10 port=5432 dbname=postgres user=docbrown sslmode=prefer connect_timeout=2"
probe:
  interval_ms: 50
  timeout_ms: 1000
  poll_interval_ms: 250
healthy_lag_bytes: 1048576
```

- `endpoint` is where your applications connect: a VIP, HAProxy, or DNS name. One host only.
- Passwords never go in this file. Use `PGPASSWORD` or `~/.pgpass`.
- Set `sslmode` explicitly. The driver's default is `require`.

The probe only ever writes to `docbrown_probe`. Remove it when you're done: `DROP TABLE docbrown_probe;`

### Database permissions

```sql
CREATE ROLE docbrown LOGIN;
GRANT pg_monitor TO docbrown;
GRANT CREATE ON SCHEMA public TO docbrown;  -- for docbrown_probe; or create the table yourself
-- Optional, to read the timeline .history file directly:
GRANT pg_read_server_files TO docbrown;
```

## Try it locally

`scripts/demo/` builds a throwaway primary + async standby and a fake VIP on your machine (Linux or macOS with PostgreSQL installed):

```bash
scripts/demo/setup.sh
CFG=/tmp/docbrown-demo/docbrown.yaml
docbrown pre --config $CFG
docbrown probe --config $CFG --duration 10s &
sleep 3 && scripts/demo/crash.sh        # or scripts/demo/switchover.sh
wait && docbrown report --config $CFG
```

`switchover.sh` is a clean planned switchover and should lose nothing. `crash.sh` stalls replication, keeps acknowledging writes, then crashes the primary, and docbrown should find every lost row.

## Before you run it on a real cluster

- **Disable automatic `pg_rewind` during tests.** If the old primary went past the fork, Patroni will rewind it right away and delete the WAL that proves what was lost. Set `use_pg_rewind: false` for the test and turn it back on afterwards.
- **Use a dedicated user** with only the permissions above.
- **Keep `runs/`, `reports/` and evidence files out of git.** They contain data from your real cluster. The `.gitignore` already excludes them.

## Requirements

- PostgreSQL 13+ (tested on 16)
- `pg_waldump` and `pg_controldata` on the old primary, only for the application-level analysis

## Roadmap

- [x] `pre`, `probe`, `report` for a planned switchover
- [x] Lost-transaction report from the old primary's WAL
- [ ] Read lost WAL straight from the WAL-G archive when the old primary's disk is gone
- [ ] Prometheus metrics (`docbrown_write_downtime_seconds`, `docbrown_lost_writes_total`, …)
- [ ] Unplanned failover mode that runs continuously
- [ ] Split-brain detection across data centers

## License

[MIT](LICENSE)
