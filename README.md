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

1. **Before:** `docbrown pre` records the state of the cluster: LSNs, replication lag, timeline and WAL archive status.
2. **During:** `docbrown probe` writes a small, numbered row every 50 ms through the same endpoint your applications use. Every result is logged **outside the database**:
   - `ack`: the commit succeeded (with the server address and WAL LSN)
   - `fail`: the write was rejected
   - `in-doubt`: the commit was sent but no answer came back
3. **After:** `docbrown report` compares the probe log against the new primary, reads the new timeline's `.history` file and the old primary's last LSN, and writes a report.

A write that was **acknowledged** but is **missing** on the new primary is lost data. That's the real definition: the client was told "saved", but the row is gone.

## What it measures

| Metric | How |
|---|---|
| Write downtime (client view) | Last successful write before → first successful write after |
| Read downtime | Same, with a separate `SELECT` probe |
| Switchover phases | Old primary shutdown → `selected new timeline ID` → first write on the new primary |
| Lost writes | Exact IDs of acknowledged probe rows missing on the new primary |
| In-doubt writes | Which unanswered commits actually landed |
| Lost bytes | Old primary's last LSN minus the timeline fork LSN |
| Lost transactions (application data) | `pg_waldump` of the old primary's WAL past the fork: xid, commit time, tables |
| Time without a replica | Promotion → old primary streaming from the new one with lag near zero |
| PITR gap (WAL-G) | Promotion → first segment and `.history` of the new timeline archived |

## Quick start

```bash
# 1. Snapshot the cluster
docbrown pre --config docbrown.yaml

# 2. Start the probe and keep it running
docbrown probe --config docbrown.yaml

# 3. In another terminal, run your switchover
patronictl switchover

# 4. Stop the probe (Ctrl+C), then build the report
docbrown report --run runs/<run-id>
```

The report is written to `reports/<run-id>.md`.

## Configuration

```yaml
# docbrown.yaml
endpoint:                 # where your applications connect (VIP / HAProxy)
  host: db.example.internal
  port: 5432
nodes:                    # each node directly
  - name: node-a
    host: 10.0.1.10
    port: 5432
  - name: node-b
    host: 10.0.2.10
    port: 5432
database: postgres
user: docbrown            # password from PGPASSWORD or ~/.pgpass, never in this file
probe:
  interval_ms: 50
```

The probe creates its own table, `docbrown_probe`, and never touches your application tables.

## Before you run it

- **Disable automatic `pg_rewind` during tests.** If the old primary went past the fork, Patroni will rewind it right away and delete the WAL that proves what was lost. Set `use_pg_rewind: false` for the test, and turn it back on afterwards.
- **Use a dedicated user** with only the permissions docbrown needs.
- **Keep `runs/`, `reports/` and `evidence/` out of git.** They contain data from your real cluster. The provided `.gitignore` already excludes them.

## Requirements

- PostgreSQL 13+
- Patroni (other HA tools are planned)
- `pg_waldump` and `pg_controldata` on the nodes, for application-level analysis
- WAL-G, optional, for PITR gap measurement

## Roadmap

- [ ] `pre`, `probe`, `report` for a manual switchover
- [ ] Application-level lost-transaction report from WAL
- [ ] Prometheus metrics (`docbrown_write_downtime_seconds`, `docbrown_lost_writes_total`, …)
- [ ] Unplanned failover mode
- [ ] Split-brain detection across data centers

## License

[Apache-2.0](LICENSE)
