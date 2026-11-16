package report

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const maxRows = 200

// Markdown renders the summary as a report a human can read top to bottom.
func Markdown(s *Summary) string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	p("# docbrown report: run %s\n\n", s.RunID)
	p("%s\n\n", verdict(s))

	p("## Summary\n\n")
	p("| | |\n|---|---|\n")
	if s.Switched {
		p("| Switchover | %s → %s (timeline %d → %d) |\n", s.OldPrimary, s.NewPrimary, s.Fork.OldTLI, s.Fork.NewTLI)
	} else {
		p("| Switchover | not detected (primary stayed %s) |\n", s.OldPrimary)
	}
	p("| Write downtime (client view) | **%s** |\n", dur(s.WriteDown))
	p("| Read downtime (client view) | %s |\n", dur(s.ReadDown))
	p("| Acknowledged writes lost | **%d** of %d acknowledged |\n", len(s.Lost), s.WritesAcked)
	p("| In-doubt writes | %d landed, %d did not |\n", len(s.InDoubtLanded), len(s.InDoubtGone))
	p("| Writes reported failed but present | %d |\n", len(s.Phantom))
	if s.Fork.ForkLSN != "" {
		p("| Timeline fork LSN | `%s` |\n", s.Fork.ForkLSN)
	}
	if s.Fork.DivergedBytes > 0 {
		p("| Old primary past the fork | at least **%d bytes** |\n", s.Fork.DivergedBytes)
	}
	for _, ph := range s.Phases {
		p("| %s | %s |\n", ph.Name, dur(ph.Duration))
	}
	if s.StandbyReads > 0 {
		p("| Reads answered by a standby through the endpoint | %d |\n", s.StandbyReads)
	}
	p("| Probe | %s → %s, every %d ms (polls every %d ms) |\n\n",
		s.ProbeStart.Format("15:04:05.000"), s.ProbeEnd.Format("15:04:05.000"), s.IntervalMS, s.PollIntervalMS)

	if len(s.Moments) > 0 {
		p("## Timeline\n\n")
		t0 := s.Moments[0].T
		if t, ok := s.moment(mFirstWriteFail); ok {
			t0 = t
		}
		p("Times are relative to the first write failure (or the first event). Node states are sampled every %d ms, so they are accurate to about that much.\n\n", s.PollIntervalMS)
		p("| Time | Offset | Event | Detail |\n|---|---|---|---|\n")
		for _, m := range s.Moments {
			p("| %s | %s | %s | %s |\n", m.T.Format("15:04:05.000"), offset(m.T.Sub(t0)), m.Name, m.Note)
		}
		p("\n")
	}

	p("## Lost writes\n\n")
	if len(s.Lost) == 0 {
		p("None. Every write the endpoint acknowledged is on the new primary.\n\n")
	} else {
		p("These writes were **acknowledged to the client** but are **not on the new primary**.\n\n")
		writeTable(&b, s.Lost)
	}

	if len(s.InDoubtLanded)+len(s.InDoubtGone) > 0 {
		p("## In-doubt writes\n\n")
		p("The client sent these but got no reliable answer. docbrown checked the new primary to see what really happened.\n\n")
		if len(s.InDoubtLanded) > 0 {
			p("**Landed** (an application would have retried and maybe duplicated them):\n\n")
			writeTable(&b, s.InDoubtLanded)
		}
		if len(s.InDoubtGone) > 0 {
			p("**Did not land:**\n\n")
			writeTable(&b, s.InDoubtGone)
		}
	}
	if len(s.Phantom) > 0 {
		p("## Writes reported as failed but present\n\n")
		p("The client was told these failed, yet they are on the new primary. An application would likely retry them and create duplicates.\n\n")
		writeTable(&b, s.Phantom)
	}

	p("## Timeline fork\n\n")
	p("%s\n\n", s.Fork.Verdict)
	p("| | |\n|---|---|\n")
	p("| Old timeline | %d |\n| New timeline | %d |\n", s.Fork.OldTLI, s.Fork.NewTLI)
	if s.Fork.ForkLSN != "" {
		p("| Fork LSN | `%s` (from %s) |\n", s.Fork.ForkLSN, s.Fork.ForkSource)
	}
	if s.Fork.OldLastSeenLSN != "" {
		p("| Highest LSN seen on the old primary | `%s` |\n", s.Fork.OldLastSeenLSN)
	}
	if s.Fork.OldCheckpointLSN != "" {
		p("| Old primary latest checkpoint (pg_controldata) | `%s` (%s) |\n", s.Fork.OldCheckpointLSN, s.Fork.OldClusterState)
	}
	p("\n")
	if s.Fork.HistoryReadError != "" && s.Fork.ForkLSN == "" {
		p("The new primary refused to show its history file (`%s`). Copy it from the new primary's `pg_wal/` or from the WAL archive and pass `--history`.\n\n", s.Fork.HistoryReadError)
	}

	if s.WAL != nil {
		p("## Application transactions after the fork\n\n")
		p("From `%s`, starting at `%s`: %d records, **%d committed** transactions, %d aborted, %d unfinished.\n\n",
			s.WAL.Source, s.WAL.FromLSN, s.WAL.Records, s.WAL.Committed, s.WAL.Aborted, s.WAL.Unfinished)
		if len(s.WAL.Tables) > 0 {
			p("Committed changes that exist only on the old timeline:\n\n| Table and operation | Rows |\n|---|---|\n")
			keys := make([]string, 0, len(s.WAL.Tables))
			for k := range s.WAL.Tables {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				p("| %s | %d |\n", k, s.WAL.Tables[k])
			}
			p("\n")
		}
		if s.WAL.Committed > 0 {
			p("| XID | Commit time | First LSN | Changes |\n|---|---|---|---|\n")
			n := 0
			for _, tx := range s.WAL.Transactions {
				if tx.Status != "committed" {
					continue
				}
				if n++; n > maxRows {
					p("| … | | | %d more in summary.json |\n", s.WAL.Committed-maxRows)
					break
				}
				p("| %d | %s | `%s` | %s |\n", tx.XID, tx.CommitTime, tx.FirstLSN, changes(tx.Changes))
			}
			p("\n")
		}
	}

	if s.Switched {
		var notes []string
		if _, ok := s.moment(mReplicaHealthy); !ok {
			notes = append(notes, "The new primary had **no caught-up replica** by the end of the probe.")
		}
		if !s.Archive.Enabled {
			notes = append(notes, "`archive_mode` was off on the new primary before the switchover.")
		} else {
			if !s.Archive.NewTimelineArchived {
				notes = append(notes, fmt.Sprintf("The new primary did not archive anything during the probe (last archived: `%s`). Until it does, point-in-time recovery on timeline %d is not possible.", s.Archive.LastArchived, s.Fork.NewTLI))
			}
			if s.Archive.FailedAfter > s.Archive.FailedBefore {
				notes = append(notes, fmt.Sprintf("Archiving **failed %d times** on the new primary during the probe.", s.Archive.FailedAfter-s.Archive.FailedBefore))
			}
		}
		if len(notes) > 0 {
			p("## Replica and archive\n\n")
			for _, n := range notes {
				p("- %s\n", n)
			}
			p("\n")
		}
	}

	if len(s.WriteOutages) > 0 || len(s.ReadOutages) > 0 {
		p("## Outages\n\n| Operation | From | To | Duration | Failed attempts | First error |\n|---|---|---|---|---|---|\n")
		for _, o := range s.WriteOutages {
			outageRow(&b, "write", o)
		}
		for _, o := range s.ReadOutages {
			outageRow(&b, "read", o)
		}
		p("\n")
	}

	if s.Switched && s.Fork.DivergedBytes > 0 && s.WAL == nil {
		p("## Next step: see what the old primary wrote past the fork\n\n")
		p("On the old primary, before it is rewound or rebuilt:\n\n```sh\n")
		p("pg_controldata \"$PGDATA\" > controldata.txt\n")
		p("pg_waldump --timeline=%d --start=%s -p \"$PGDATA/pg_wal\" > waldump.txt\n", s.Fork.OldTLI, s.Fork.ForkLSN)
		p("```\n\nThen: `docbrown report --run %s --controldata controldata.txt --waldump waldump.txt`\n\n", s.RunID)
	}

	if len(s.Warnings) > 0 {
		p("## Warnings\n\n")
		for _, w := range s.Warnings {
			p("- %s\n", w)
		}
		p("\n")
	}
	p("---\nGenerated by docbrown at %s.\n", s.GeneratedAt.Format(time.RFC3339))
	return b.String()
}

func verdict(s *Summary) string {
	switch {
	case len(s.Lost) > 0:
		return fmt.Sprintf("**%d acknowledged writes were lost.** Write downtime was %s.", len(s.Lost), dur(s.WriteDown))
	case s.Fork.DivergedBytes > 0:
		return fmt.Sprintf("**No probe write was lost, but the old primary wrote %d bytes past the fork point.** Application data may be missing; see below.", s.Fork.DivergedBytes)
	case s.Switched:
		return fmt.Sprintf("**No acknowledged write was lost.** Write downtime was %s.", dur(s.WriteDown))
	default:
		return fmt.Sprintf("No switchover was detected. Write downtime during the probe was %s.", dur(s.WriteDown))
	}
}

func writeTable(b *strings.Builder, ws []Write) {
	fmt.Fprintf(b, "| Seq | Client time | Result | Server | LSN | Timeline | Error |\n|---|---|---|---|---|---|---|\n")
	for i, w := range ws {
		if i == maxRows {
			fmt.Fprintf(b, "| … | %d more in summary.json | | | | | |\n", len(ws)-maxRows)
			break
		}
		tli := ""
		if w.TLI > 0 {
			tli = fmt.Sprint(w.TLI)
		}
		fmt.Fprintf(b, "| %d | %s | %s | %s | %s | %s | %s |\n", w.Seq, w.T.Format("15:04:05.000"), w.Result, w.Server, code(w.LSN), tli, esc(firstLine(w.Err)))
	}
	b.WriteString("\n")
}

func outageRow(b *strings.Builder, op string, o Outage) {
	to := "not recovered"
	if o.End != nil {
		to = o.End.Format("15:04:05.000")
	}
	fmt.Fprintf(b, "| %s | %s | %s | %s | %d | %s |\n", op, o.Start.Format("15:04:05.000"), to, dur(o.Duration), o.Attempts, esc(o.FirstError))
}

func changes(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s ×%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func dur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.2f s", d.Seconds())
}

func offset(d time.Duration) string {
	sign := "+"
	if d < 0 {
		sign, d = "−", -d
	}
	return sign + dur(d)
}

func code(s string) string {
	if s == "" {
		return ""
	}
	return "`" + s + "`"
}

func esc(s string) string { return strings.ReplaceAll(s, "|", "\\|") }
