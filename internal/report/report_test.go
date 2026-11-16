package report

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MostafaHassanzadeh/docbrown/internal/events"
)

func ev(ms int, result string) events.Event {
	return events.Event{T: time.Unix(0, 0).Add(time.Duration(ms) * time.Millisecond), LatencyMS: 2, Result: result}
}

func TestOutages(t *testing.T) {
	evs := []events.Event{ev(0, "ack"), ev(50, "ack"), ev(100, "fail"), ev(150, "in-doubt"), ev(200, "ack"), ev(250, "fail")}
	out, total := outages(evs, "ack")
	if len(out) != 2 {
		t.Fatalf("want 2 outages, got %d", len(out))
	}
	// From end of the ack at 50 (52ms) to end of the ack at 200 (202ms).
	if out[0].Duration != 150*time.Millisecond || out[0].Attempts != 2 {
		t.Fatalf("first outage %+v", out[0])
	}
	if out[1].End != nil {
		t.Fatalf("last outage should be open")
	}
	if total < out[0].Duration {
		t.Fatalf("total %s", total)
	}
}

func TestClassifyWrites(t *testing.T) {
	s := &Summary{}
	w := []events.Event{
		{Seq: 1, Result: "ack"}, {Seq: 2, Result: "ack"}, {Seq: 3, Result: "in-doubt"},
		{Seq: 4, Result: "in-doubt"}, {Seq: 5, Result: "fail"},
	}
	present := map[int64]time.Time{1: {}, 3: {}, 5: {}}
	s.classifyWrites(w, present)
	if len(s.Lost) != 1 || s.Lost[0].Seq != 2 {
		t.Fatalf("lost %+v", s.Lost)
	}
	if len(s.InDoubtLanded) != 1 || len(s.InDoubtGone) != 1 || len(s.Phantom) != 1 {
		t.Fatalf("in-doubt/phantom wrong: %+v", s)
	}
}

func TestParseControlData(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cd.txt")
	os.WriteFile(p, []byte("Database cluster state:               shut down\nLatest checkpoint location:           0/3000028\n"), 0o600)
	lsn, state, err := parseControlData(p)
	if err != nil || lsn.String() != "0/3000028" || state != "shut down" {
		t.Fatalf("%v %s %q", err, lsn, state)
	}
}

func TestWaldumpRegex(t *testing.T) {
	lines := []string{
		"rmgr: Heap        len (rec/tot):     59/    59, tx:        745, lsn: 0/03000060, prev 0/03000028, desc: INSERT off: 3, flags: 0x00, blkref #0: rel 1663/5/16384 blk 0",
		"rmgr: Transaction len (rec/tot):     34/    34, tx:        745, lsn: 0/030000A0, prev 0/03000060, desc: COMMIT 2026-11-17 10:00:00.123456 UTC",
	}
	for _, l := range lines {
		if reRecord.FindStringSubmatch(l) == nil {
			t.Errorf("no match: %s", l)
		}
	}
	if m := reRel.FindStringSubmatch(lines[0]); m == nil || m[3] != "16384" {
		t.Errorf("rel not found")
	}
}
