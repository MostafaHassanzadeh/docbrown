# docbrown report: run 20261116-223429

**30 acknowledged writes were lost.** Write downtime was 552 ms.

## Summary

| | |
|---|---|
| Switchover | node-a → node-b (timeline 1 → 2) |
| Write downtime (client view) | **552 ms** |
| Read downtime (client view) | 555 ms |
| Acknowledged writes lost | **30** of 150 acknowledged |
| In-doubt writes | 0 landed, 1 did not |
| Writes reported failed but present | 0 |
| Timeline fork LSN | `0/30339E8` |
| Old primary past the fork | at least **6288 bytes** |
| Old primary to new primary | 0 ms |
| Promotion to first write through the endpoint | 305 ms |
| Last old write to first new write | 552 ms |
| Probe | 02:04:29.077 → 02:04:37.082, every 50 ms (polls every 250 ms) |

## Timeline

Times are relative to the first write failure (or the first event). Node states are sampled every 250 ms, so they are accurate to about that much.

| Time | Offset | Event | Detail |
|---|---|---|---|
| 02:04:33.578 | −50 ms | old primary last seen as primary |  |
| 02:04:33.581 | −46 ms | last write acknowledged by the old primary | seq 90 via 127.0.0.1 |
| 02:04:33.628 | +0 ms | first write failure | seq 91: read tcp 127.0.0.1:55010->127.0.0.1:6432: read: connection reset by peer |
| 02:04:33.828 | +199 ms | old primary stopped being primary | unreachable |
| 02:04:33.828 | +199 ms | new primary first seen as primary | timeline 2 |
| 02:04:34.134 | +505 ms | first write acknowledged by the new primary | seq 101 via 127.0.0.1 |

## Lost writes

These writes were **acknowledged to the client** but are **not on the new primary**.

| Seq | Client time | Result | Server | LSN | Timeline | Error |
|---|---|---|---|---|---|---|
| 61 | 02:04:32.128 | ack | 127.0.0.1 | `0/30339E8` | 1 |  |
| 62 | 02:04:32.178 | ack | 127.0.0.1 | `0/3033AC0` | 1 |  |
| 63 | 02:04:32.228 | ack | 127.0.0.1 | `0/3033B98` | 1 |  |
| 64 | 02:04:32.278 | ack | 127.0.0.1 | `0/3033C70` | 1 |  |
| 65 | 02:04:32.327 | ack | 127.0.0.1 | `0/3033D48` | 1 |  |
| 66 | 02:04:32.377 | ack | 127.0.0.1 | `0/3033E20` | 1 |  |
| 67 | 02:04:32.427 | ack | 127.0.0.1 | `0/3033EF8` | 1 |  |
| 68 | 02:04:32.478 | ack | 127.0.0.1 | `0/3033FD0` | 1 |  |
| 69 | 02:04:32.528 | ack | 127.0.0.1 | `0/30340C0` | 1 |  |
| 70 | 02:04:32.578 | ack | 127.0.0.1 | `0/3034198` | 1 |  |
| 71 | 02:04:32.628 | ack | 127.0.0.1 | `0/3034270` | 1 |  |
| 72 | 02:04:32.677 | ack | 127.0.0.1 | `0/3034348` | 1 |  |
| 73 | 02:04:32.728 | ack | 127.0.0.1 | `0/3034420` | 1 |  |
| 74 | 02:04:32.778 | ack | 127.0.0.1 | `0/30344F8` | 1 |  |
| 75 | 02:04:32.828 | ack | 127.0.0.1 | `0/30345D0` | 1 |  |
| 76 | 02:04:32.878 | ack | 127.0.0.1 | `0/30346A8` | 1 |  |
| 77 | 02:04:32.928 | ack | 127.0.0.1 | `0/3034780` | 1 |  |
| 78 | 02:04:32.978 | ack | 127.0.0.1 | `0/3034858` | 1 |  |
| 79 | 02:04:33.027 | ack | 127.0.0.1 | `0/3034930` | 1 |  |
| 80 | 02:04:33.078 | ack | 127.0.0.1 | `0/3034A08` | 1 |  |
| 81 | 02:04:33.128 | ack | 127.0.0.1 | `0/3034AE0` | 1 |  |
| 82 | 02:04:33.177 | ack | 127.0.0.1 | `0/3034BB8` | 1 |  |
| 83 | 02:04:33.228 | ack | 127.0.0.1 | `0/3034C90` | 1 |  |
| 84 | 02:04:33.278 | ack | 127.0.0.1 | `0/3034D68` | 1 |  |
| 85 | 02:04:33.330 | ack | 127.0.0.1 | `0/3034E40` | 1 |  |
| 86 | 02:04:33.378 | ack | 127.0.0.1 | `0/3034F18` | 1 |  |
| 87 | 02:04:33.428 | ack | 127.0.0.1 | `0/3034FF0` | 1 |  |
| 88 | 02:04:33.478 | ack | 127.0.0.1 | `0/30350C8` | 1 |  |
| 89 | 02:04:33.528 | ack | 127.0.0.1 | `0/30351A0` | 1 |  |
| 90 | 02:04:33.578 | ack | 127.0.0.1 | `0/3035278` | 1 |  |

## In-doubt writes

The client sent these but got no reliable answer. docbrown checked the new primary to see what really happened.

**Did not land:**

| Seq | Client time | Result | Server | LSN | Timeline | Error |
|---|---|---|---|---|---|---|
| 91 | 02:04:33.628 | in-doubt |  |  |  | read tcp 127.0.0.1:55010->127.0.0.1:6432: read: connection reset by peer |

## Timeline fork

The old primary went at least 6288 bytes past the fork point. WAL written there exists only on the old primary (and possibly the archive).

| | |
|---|---|
| Old timeline | 1 |
| New timeline | 2 |
| Fork LSN | `0/30339E8` (from pg_wal/00000002.history on the new primary) |
| Highest LSN seen on the old primary | `0/3035278` |
| Old primary latest checkpoint (pg_controldata) | `0/2000060` (in production) |

## Application transactions after the fork

From `waldump.txt`, starting at `0/30339E8`: 90 records, **30 committed** transactions, 0 aborted, 0 unfinished.

Committed changes that exist only on the old timeline:

| Table and operation | Rows |
|---|---|
| public.docbrown_probe INSERT | 30 |

| XID | Commit time | First LSN | Changes |
|---|---|---|---|
| 791 | 2026-11-17 02:04:32.129293 +0330 | `0/30339E8` | public.docbrown_probe INSERT ×1 |
| 792 | 2026-11-17 02:04:32.179325 +0330 | `0/3033AC0` | public.docbrown_probe INSERT ×1 |
| 793 | 2026-11-17 02:04:32.229680 +0330 | `0/3033B98` | public.docbrown_probe INSERT ×1 |
| 794 | 2026-11-17 02:04:32.279712 +0330 | `0/3033C70` | public.docbrown_probe INSERT ×1 |
| 795 | 2026-11-17 02:04:32.329940 +0330 | `0/3033D48` | public.docbrown_probe INSERT ×1 |
| 796 | 2026-11-17 02:04:32.378838 +0330 | `0/3033E20` | public.docbrown_probe INSERT ×1 |
| 797 | 2026-11-17 02:04:32.428806 +0330 | `0/3033EF8` | public.docbrown_probe INSERT ×1 |
| 798 | 2026-11-17 02:04:32.478977 +0330 | `0/3033FD0` | public.docbrown_probe INSERT ×1 |
| 799 | 2026-11-17 02:04:32.531046 +0330 | `0/30340C0` | public.docbrown_probe INSERT ×1 |
| 800 | 2026-11-17 02:04:32.579187 +0330 | `0/3034198` | public.docbrown_probe INSERT ×1 |
| 801 | 2026-11-17 02:04:32.629655 +0330 | `0/3034270` | public.docbrown_probe INSERT ×1 |
| 802 | 2026-11-17 02:04:32.678874 +0330 | `0/3034348` | public.docbrown_probe INSERT ×1 |
| 803 | 2026-11-17 02:04:32.729122 +0330 | `0/3034420` | public.docbrown_probe INSERT ×1 |
| 804 | 2026-11-17 02:04:32.780140 +0330 | `0/30344F8` | public.docbrown_probe INSERT ×1 |
| 805 | 2026-11-17 02:04:32.830587 +0330 | `0/30345D0` | public.docbrown_probe INSERT ×1 |
| 806 | 2026-11-17 02:04:32.879087 +0330 | `0/30346A8` | public.docbrown_probe INSERT ×1 |
| 807 | 2026-11-17 02:04:32.929025 +0330 | `0/3034780` | public.docbrown_probe INSERT ×1 |
| 808 | 2026-11-17 02:04:32.979200 +0330 | `0/3034858` | public.docbrown_probe INSERT ×1 |
| 809 | 2026-11-17 02:04:33.028915 +0330 | `0/3034930` | public.docbrown_probe INSERT ×1 |
| 810 | 2026-11-17 02:04:33.083196 +0330 | `0/3034A08` | public.docbrown_probe INSERT ×1 |
| 811 | 2026-11-17 02:04:33.128941 +0330 | `0/3034AE0` | public.docbrown_probe INSERT ×1 |
| 812 | 2026-11-17 02:04:33.178959 +0330 | `0/3034BB8` | public.docbrown_probe INSERT ×1 |
| 813 | 2026-11-17 02:04:33.229172 +0330 | `0/3034C90` | public.docbrown_probe INSERT ×1 |
| 814 | 2026-11-17 02:04:33.279348 +0330 | `0/3034D68` | public.docbrown_probe INSERT ×1 |
| 815 | 2026-11-17 02:04:33.332339 +0330 | `0/3034E40` | public.docbrown_probe INSERT ×1 |
| 816 | 2026-11-17 02:04:33.379089 +0330 | `0/3034F18` | public.docbrown_probe INSERT ×1 |
| 817 | 2026-11-17 02:04:33.428979 +0330 | `0/3034FF0` | public.docbrown_probe INSERT ×1 |
| 818 | 2026-11-17 02:04:33.479432 +0330 | `0/30350C8` | public.docbrown_probe INSERT ×1 |
| 819 | 2026-11-17 02:04:33.529529 +0330 | `0/30351A0` | public.docbrown_probe INSERT ×1 |
| 820 | 2026-11-17 02:04:33.580486 +0330 | `0/3035278` | public.docbrown_probe INSERT ×1 |

## Replica and archive

- The new primary had **no caught-up replica** by the end of the probe.
- The new primary did not archive anything during the probe (last archived: `000000010000000000000003.partial`). Until it does, point-in-time recovery on timeline 2 is not possible.

## Outages

| Operation | From | To | Duration | Failed attempts | First error |
|---|---|---|---|---|---|
| write | 02:04:33.581 | 02:04:34.134 | 552 ms | 10 | read tcp 127.0.0.1:55010->127.0.0.1:6432: read: connection reset by peer |
| read | 02:04:33.579 | 02:04:34.134 | 555 ms | 10 | read tcp 127.0.0.1:54996->127.0.0.1:6432: read: connection reset by peer |

---
Generated by docbrown at 2026-11-17T02:04:40+03:30.
