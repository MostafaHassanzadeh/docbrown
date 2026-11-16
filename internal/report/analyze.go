// Package report turns a recorded run into numbers and a readable report.
package report

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MostafaHassanzadeh/docbrown/internal/config"
	"github.com/MostafaHassanzadeh/docbrown/internal/events"
	"github.com/MostafaHassanzadeh/docbrown/internal/pgstate"
	"github.com/MostafaHassanzadeh/docbrown/internal/probe"
)

// Options are the optional evidence files the user can hand in.
type Options struct {
	HistoryFile     string // NNNNNNNN.history copied from the new primary or WAL archive
	ControlDataFile string // `pg_controldata` output of the old primary
	WaldumpFile     string // `pg_waldump` output of the old primary from the fork LSN
}

// Write is one probe write and what became of it.
type Write struct {
	Seq      int64      `json:"seq"`
	T        time.Time  `json:"t"`
	Result   string     `json:"result"`
	Server   string     `json:"server,omitempty"`
	LSN      string     `json:"lsn,omitempty"`
	TLI      int        `json:"tli,omitempty"`
	Err      string     `json:"err,omitempty"`
	Present  bool       `json:"present_on_new_primary"`
	ServerTS *time.Time `json:"server_ts,omitempty"`
}

// Outage is a stretch where the endpoint did not serve a kind of operation.
type Outage struct {
	Start      time.Time     `json:"start"`
	End        *time.Time    `json:"end,omitempty"`
	Duration   time.Duration `json:"duration_ns"`
	Attempts   int           `json:"failed_attempts"`
	FirstError string        `json:"first_error,omitempty"`
}

// Moment is a named point in time, if it was observed.
type Moment struct {
	Name string    `json:"name"`
	T    time.Time `json:"t"`
	Note string    `json:"note,omitempty"`
}

// Fork describes where the timelines split.
type Fork struct {
	OldTLI           int    `json:"old_tli"`
	NewTLI           int    `json:"new_tli"`
	ForkLSN          string `json:"fork_lsn,omitempty"`
	ForkSource       string `json:"fork_source,omitempty"`
	OldLastSeenLSN   string `json:"old_last_seen_lsn,omitempty"`
	OldCheckpointLSN string `json:"old_checkpoint_lsn,omitempty"`
	OldClusterState  string `json:"old_cluster_state,omitempty"`
	DivergedBytes    int64  `json:"diverged_bytes_at_least"`
	Verdict          string `json:"verdict"`
	HistoryReadError string `json:"history_read_error,omitempty"`
}

// Summary is everything docbrown concluded about one run.
type Summary struct {
	RunID          string    `json:"run_id"`
	GeneratedAt    time.Time `json:"generated_at"`
	OldPrimary     string    `json:"old_primary"`
	NewPrimary     string    `json:"new_primary"`
	Switched       bool      `json:"switchover_detected"`
	ProbeStart     time.Time `json:"probe_start"`
	ProbeEnd       time.Time `json:"probe_end"`
	IntervalMS     int       `json:"interval_ms"`
	PollIntervalMS int       `json:"poll_interval_ms"`

	WritesTotal   int     `json:"writes_total"`
	WritesAcked   int     `json:"writes_acked"`
	Lost          []Write `json:"lost"`
	InDoubtLanded []Write `json:"in_doubt_landed"`
	InDoubtGone   []Write `json:"in_doubt_not_landed"`
	Phantom       []Write `json:"phantom"`

	WriteOutages []Outage      `json:"write_outages"`
	ReadOutages  []Outage      `json:"read_outages"`
	WriteDown    time.Duration `json:"write_downtime_ns"`
	ReadDown     time.Duration `json:"read_downtime_ns"`
	StandbyReads int           `json:"reads_served_by_standby"`

	Moments []Moment     `json:"moments"`
	Phases  []Phase      `json:"phases"`
	Fork    Fork         `json:"fork"`
	Archive Archive      `json:"archive"`
	WAL     *WALAnalysis `json:"wal_after_fork,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
}

// Phase is the time between two moments.
type Phase struct {
	Name     string        `json:"name"`
	From     string        `json:"from"`
	To       string        `json:"to"`
	Duration time.Duration `json:"duration_ns"`
}

// Archive is what we saw of WAL archiving on the new primary.
type Archive struct {
	Enabled             bool   `json:"enabled"`
	FailedBefore        int64  `json:"failed_count_before"`
	FailedAfter         int64  `json:"failed_count_after"`
	NewTimelineArchived bool   `json:"new_timeline_archived"`
	LastArchived        string `json:"last_archived_wal,omitempty"`
}

// Build analyses a run. It connects to the endpoint (now the new primary) to
// see which probe rows survived.
func Build(cfg *config.Config, run events.Run, opt Options) (*Summary, error) {
	var meta events.Meta
	if err := events.ReadJSON(run.MetaPath(), &meta); err != nil {
		return nil, fmt.Errorf("read meta: %w", err)
	}
	var pre probe.PreSnapshot
	if err := events.ReadJSON(run.PrePath(), &pre); err != nil {
		return nil, fmt.Errorf("read pre snapshot: %w", err)
	}
	evs, err := events.ReadAll(run.EventsPath())
	if err != nil {
		return nil, fmt.Errorf("read events (did you run `docbrown probe`?): %w", err)
	}
	s := &Summary{RunID: run.ID, GeneratedAt: time.Now(), OldPrimary: meta.OldNode,
		IntervalMS: cfg.Probe.IntervalMS, PollIntervalMS: cfg.Probe.PollIntervalMS}

	var writes, reads []events.Event
	polls := map[string][]events.Event{}
	for _, e := range evs {
		switch e.Kind {
		case events.KindWrite:
			writes = append(writes, e)
		case events.KindRead:
			reads = append(reads, e)
		case events.KindPoll:
			polls[e.Target] = append(polls[e.Target], e)
		case events.KindMark:
			if e.Note == "probe-start" {
				s.ProbeStart = e.T
			}
			if e.Note == "probe-stop" {
				s.ProbeEnd = e.T
			}
		}
	}
	sortByT(writes)
	sortByT(reads)
	for k := range polls {
		sortByT(polls[k])
	}
	if len(writes) == 0 {
		return nil, fmt.Errorf("no probe writes recorded in %s", run.EventsPath())
	}
	if s.ProbeEnd.IsZero() {
		s.ProbeEnd = writes[len(writes)-1].End()
		s.Warnings = append(s.Warnings, "probe did not stop cleanly; using the last recorded write as the end")
	}

	db, err := pgstate.Open(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	present, err := survivingRows(ctx, db, run.ID)
	if err != nil {
		return nil, fmt.Errorf("read docbrown_probe through endpoint: %w", err)
	}
	s.classifyWrites(writes, present)
	s.WriteOutages, s.WriteDown = outages(writes, events.ResultAck)
	s.ReadOutages, s.ReadDown = outages(reads, events.ResultOK)
	for _, r := range reads {
		if r.Standby != nil && *r.Standby {
			s.StandbyReads++
		}
	}

	s.findNewPrimary(cfg, polls)
	s.timeline(cfg, writes, polls, pre)
	s.fork(ctx, db, writes, polls, pre, opt)
	if opt.WaldumpFile != "" {
		wa, err := analyzeWaldump(ctx, db, opt.WaldumpFile, s.Fork.ForkLSN)
		if err != nil {
			s.Warnings = append(s.Warnings, "waldump analysis failed: "+err.Error())
		} else {
			s.WAL = wa
		}
	}
	return s, nil
}

func sortByT(e []events.Event) {
	sort.SliceStable(e, func(i, j int) bool { return e[i].T.Before(e[j].T) })
}

func survivingRows(ctx context.Context, db *sql.DB, runID string) (map[int64]time.Time, error) {
	rows, err := db.QueryContext(ctx, `SELECT seq, server_ts FROM docbrown_probe WHERE run_id = $1`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var seq int64
		var ts time.Time
		if err := rows.Scan(&seq, &ts); err != nil {
			return nil, err
		}
		out[seq] = ts
	}
	return out, rows.Err()
}

func (s *Summary) classifyWrites(writes []events.Event, present map[int64]time.Time) {
	s.WritesTotal = len(writes)
	for _, e := range writes {
		w := Write{Seq: e.Seq, T: e.T, Result: e.Result, Server: e.Server, LSN: e.LSN, TLI: e.TLI, Err: e.Err}
		if ts, ok := present[e.Seq]; ok {
			w.Present = true
			w.ServerTS = &ts
		}
		switch e.Result {
		case events.ResultAck:
			s.WritesAcked++
			if !w.Present {
				s.Lost = append(s.Lost, w)
			}
		case events.ResultInDoubt:
			if w.Present {
				s.InDoubtLanded = append(s.InDoubtLanded, w)
			} else {
				s.InDoubtGone = append(s.InDoubtGone, w)
			}
		case events.ResultFail:
			if w.Present {
				s.Phantom = append(s.Phantom, w)
			}
		}
	}
}

// outages finds every stretch between a success and the next success that
// contains at least one failure. Duration runs from the end of the last
// success before to the end of the first success after: that is how long a
// client trying continuously could not get through.
func outages(evs []events.Event, okResult string) ([]Outage, time.Duration) {
	var out []Outage
	var total time.Duration
	var lastOK *events.Event
	var cur *Outage
	for i := range evs {
		e := evs[i]
		if e.Result == okResult {
			if cur != nil {
				end := e.End()
				cur.End = &end
				cur.Duration = end.Sub(cur.Start)
				total += cur.Duration
				out = append(out, *cur)
				cur = nil
			}
			lastOK = &evs[i]
			continue
		}
		if cur == nil {
			start := e.T
			if lastOK != nil {
				start = lastOK.End()
			}
			cur = &Outage{Start: start, FirstError: firstLine(e.Err)}
		}
		cur.Attempts++
	}
	if cur != nil {
		end := evs[len(evs)-1].End()
		cur.Duration = end.Sub(cur.Start)
		total += cur.Duration
		out = append(out, *cur) // End stays nil: never recovered during the probe
	}
	return out, total
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func isPrimary(e events.Event) bool { return e.Poll != nil && e.Poll.Reachable && !e.Poll.InRecovery }
func isStandby(e events.Event) bool { return e.Poll != nil && e.Poll.Reachable && e.Poll.InRecovery }
func reachable(e events.Event) bool { return e.Poll != nil && e.Poll.Reachable }

func (s *Summary) findNewPrimary(cfg *config.Config, polls map[string][]events.Event) {
	// The new primary is whichever node was last seen as primary.
	var best time.Time
	for _, n := range cfg.Nodes {
		p := polls[n.Name]
		for i := len(p) - 1; i >= 0; i-- {
			if isPrimary(p[i]) {
				if p[i].T.After(best) {
					best = p[i].T
					s.NewPrimary = n.Name
				}
				break
			}
		}
	}
	if s.NewPrimary == "" {
		s.Warnings = append(s.Warnings, "no node was seen as primary at the end of the probe")
	}
	s.Switched = s.NewPrimary != "" && s.NewPrimary != s.OldPrimary
}

func (s *Summary) addMoment(name string, t time.Time, note string) {
	if !t.IsZero() {
		s.Moments = append(s.Moments, Moment{Name: name, T: t, Note: note})
	}
}

func (s *Summary) moment(name string) (time.Time, bool) {
	for _, m := range s.Moments {
		if m.Name == name {
			return m.T, true
		}
	}
	return time.Time{}, false
}

// Moment names.
const (
	mLastOldAck     = "last write acknowledged by the old primary"
	mFirstWriteFail = "first write failure"
	mOldLastPrimary = "old primary last seen as primary"
	mOldGone        = "old primary stopped being primary"
	mNewPromoted    = "new primary first seen as primary"
	mFirstNewAck    = "first write acknowledged by the new primary"
	mOldRejoined    = "old primary streaming from the new one"
	mReplicaHealthy = "replica caught up (lag under threshold)"
	mNewTLIArchived = "first file archived by the new primary"
)

func (s *Summary) timeline(cfg *config.Config, writes []events.Event, polls map[string][]events.Event, pre probe.PreSnapshot) {
	oldTLI := 0
	if st, ok := pre.Nodes[s.OldPrimary]; ok {
		oldTLI = st.TLI
	}
	newTLI := 0
	if p := polls[s.NewPrimary]; len(p) > 0 {
		for i := len(p) - 1; i >= 0; i-- {
			if isPrimary(p[i]) {
				newTLI = p[i].Poll.TLI
				break
			}
		}
	}
	s.Fork.OldTLI, s.Fork.NewTLI = oldTLI, newTLI

	for _, w := range writes {
		if w.Result == events.ResultAck && w.TLI == oldTLI {
			s.removeMoment(mLastOldAck)
			s.addMoment(mLastOldAck, w.End(), fmt.Sprintf("seq %d via %s", w.Seq, w.Server))
		}
	}
	for _, w := range writes {
		if w.Result != events.ResultAck {
			s.addMoment(mFirstWriteFail, w.T, fmt.Sprintf("seq %d: %s", w.Seq, firstLine(w.Err)))
			break
		}
	}

	old := polls[s.OldPrimary]
	for i, e := range old {
		if !isPrimary(e) {
			if i > 0 && isPrimary(old[i-1]) {
				s.addMoment(mOldLastPrimary, old[i-1].T, "")
				note := "unreachable"
				if isStandby(e) {
					note = "now in recovery"
				}
				s.addMoment(mOldGone, e.T, note)
				break
			}
		}
	}

	if !s.Switched {
		s.finishPhases()
		return
	}

	var promotedAt time.Time
	for _, e := range polls[s.NewPrimary] {
		if isPrimary(e) {
			promotedAt = e.T
			s.addMoment(mNewPromoted, e.T, fmt.Sprintf("timeline %d", e.Poll.TLI))
			break
		}
	}
	for _, w := range writes {
		if w.Result == events.ResultAck && newTLI > 0 && w.TLI == newTLI {
			s.addMoment(mFirstNewAck, w.End(), fmt.Sprintf("seq %d via %s", w.Seq, w.Server))
			break
		}
	}
	if !promotedAt.IsZero() {
		for _, e := range old {
			if e.T.After(promotedAt) && isStandby(e) && e.Poll.Receiver == "streaming" && e.Poll.TLI == newTLI {
				s.addMoment(mOldRejoined, e.T, "")
				break
			}
		}
		for _, e := range polls[s.NewPrimary] {
			if !e.T.After(promotedAt) || !isPrimary(e) {
				continue
			}
			ok := false
			for _, sb := range e.Poll.Standbys {
				if sb.State == "streaming" && sb.LagBytes >= 0 && sb.LagBytes <= cfg.HealthyLagBytes {
					ok = true
				}
			}
			if ok {
				s.addMoment(mReplicaHealthy, e.T, fmt.Sprintf("threshold %d bytes", cfg.HealthyLagBytes))
				break
			}
		}
		s.archive(polls[s.NewPrimary], pre, promotedAt, newTLI)
	}
	s.finishPhases()
}

func (s *Summary) removeMoment(name string) {
	out := s.Moments[:0]
	for _, m := range s.Moments {
		if m.Name != name {
			out = append(out, m)
		}
	}
	s.Moments = out
}

func (s *Summary) archive(newPolls []events.Event, pre probe.PreSnapshot, promotedAt time.Time, newTLI int) {
	if st, ok := pre.Nodes[s.NewPrimary]; ok {
		for _, set := range st.Settings {
			if set.Name == "archive_mode" && set.Value != "off" {
				s.Archive.Enabled = true
			}
		}
		if st.Archiver != nil {
			s.Archive.FailedBefore = st.Archiver.FailedCount
		}
	}
	// The archiver's own counter is the reliable signal: last_archived_wal can
	// lag behind or point at an old-timeline file after promotion.
	baseline := int64(-1)
	for _, e := range newPolls {
		if e.Poll == nil || e.Poll.Archiver == nil || !isPrimary(e) {
			continue
		}
		a := e.Poll.Archiver
		s.Archive.FailedAfter = a.FailedCount
		s.Archive.LastArchived = a.LastArchived
		if baseline < 0 {
			baseline = a.ArchivedCount
			continue
		}
		if a.ArchivedCount > baseline && !s.Archive.NewTimelineArchived {
			s.Archive.NewTimelineArchived = true
			s.addMoment(mNewTLIArchived, e.T, a.LastArchived)
		}
	}
	_ = newTLI
}

func (s *Summary) finishPhases() {
	sort.SliceStable(s.Moments, func(i, j int) bool { return s.Moments[i].T.Before(s.Moments[j].T) })
	add := func(name, from, to string) {
		a, ok1 := s.moment(from)
		b, ok2 := s.moment(to)
		if ok1 && ok2 {
			s.Phases = append(s.Phases, Phase{Name: name, From: from, To: to, Duration: b.Sub(a)})
		}
	}
	add("Old primary to new primary", mOldGone, mNewPromoted)
	add("Promotion to first write through the endpoint", mNewPromoted, mFirstNewAck)
	add("Last old write to first new write", mLastOldAck, mFirstNewAck)
	add("Running without a replica", mNewPromoted, mReplicaHealthy)
	add("Old primary back as replica", mNewPromoted, mOldRejoined)
	add("No point-in-time recovery on the new timeline", mNewPromoted, mNewTLIArchived)
}

func (s *Summary) fork(ctx context.Context, db *sql.DB, writes []events.Event, polls map[string][]events.Event, pre probe.PreSnapshot, opt Options) {
	f := &s.Fork
	// Highest LSN we saw the old primary reach on the old timeline.
	var maxOld pgstate.LSN
	consider := func(lsn string) {
		if l, err := pgstate.ParseLSN(lsn); err == nil && l > maxOld {
			maxOld = l
		}
	}
	for _, e := range polls[s.OldPrimary] {
		if isPrimary(e) && e.Poll.TLI == f.OldTLI {
			consider(e.Poll.LSN)
		}
	}
	for _, w := range writes {
		if w.Result == events.ResultAck && w.TLI == f.OldTLI {
			consider(w.LSN)
		}
	}
	if maxOld > 0 {
		f.OldLastSeenLSN = maxOld.String()
	}

	if !s.Switched {
		f.Verdict = "No switchover was detected: the same node was primary before and after."
		return
	}

	var history string
	if opt.HistoryFile != "" {
		b, err := os.ReadFile(opt.HistoryFile)
		if err != nil {
			s.Warnings = append(s.Warnings, "read history file: "+err.Error())
		} else {
			history, f.ForkSource = string(b), opt.HistoryFile
		}
	}
	if history == "" && f.NewTLI > 0 {
		h, err := pgstate.ReadHistory(ctx, db, f.NewTLI)
		if err != nil {
			f.HistoryReadError = err.Error()
		} else {
			history, f.ForkSource = h, fmt.Sprintf("pg_wal/%08X.history on the new primary", f.NewTLI)
		}
	}
	var fork pgstate.LSN
	if history != "" {
		entries, err := pgstate.ParseHistory(history)
		if err != nil {
			s.Warnings = append(s.Warnings, "parse history: "+err.Error())
		}
		for _, e := range entries {
			if e.ParentTLI == f.OldTLI {
				fork = e.SwitchPoint
			}
		}
		if fork > 0 {
			f.ForkLSN = fork.String()
		}
	}

	if opt.ControlDataFile != "" {
		lsn, state, err := parseControlData(opt.ControlDataFile)
		if err != nil {
			s.Warnings = append(s.Warnings, "controldata: "+err.Error())
		} else {
			f.OldClusterState = state
			if lsn > 0 {
				f.OldCheckpointLSN = lsn.String()
				if lsn > maxOld {
					maxOld = lsn
				}
			}
		}
	}

	switch {
	case fork == 0:
		f.Verdict = "Fork point unknown: the timeline history could not be read. " +
			"Pass --history with the new timeline's .history file to compare LSNs."
	case maxOld > fork:
		f.DivergedBytes = int64(maxOld - fork)
		f.Verdict = fmt.Sprintf("The old primary went at least %d bytes past the fork point. "+
			"WAL written there exists only on the old primary (and possibly the archive).", f.DivergedBytes)
	default:
		f.Verdict = "The old primary was never seen past the fork point: everything it was observed " +
			"to write is on the new timeline."
	}
}

func parseControlData(path string) (pgstate.LSN, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, "", err
	}
	var lsn pgstate.LSN
	var state string
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "Latest checkpoint location":
			lsn, _ = pgstate.ParseLSN(v)
		case "Database cluster state":
			state = v
		}
	}
	if lsn == 0 && state == "" {
		return 0, "", fmt.Errorf("%s does not look like pg_controldata output", path)
	}
	return lsn, state, nil
}
