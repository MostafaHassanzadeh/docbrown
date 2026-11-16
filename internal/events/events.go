// Package events defines what docbrown records during a run and how it is stored.
//
// Everything is written to disk on the machine running docbrown, never to the
// database under test: during a switchover the database is the thing we cannot trust.
package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Kinds of events.
const (
	KindWrite = "write" // probe INSERT through the endpoint
	KindRead  = "read"  // probe SELECT through the endpoint
	KindPoll  = "poll"  // direct poll of one node
	KindMark  = "mark"  // start/stop markers
)

// Results of a write or read.
const (
	ResultAck     = "ack"      // commit confirmed by the server
	ResultFail    = "fail"     // definitely not committed (rejected, or never sent)
	ResultInDoubt = "in-doubt" // sent, but no reliable answer: may or may not have committed
	ResultOK      = "ok"       // read succeeded
)

// Event is one line of events.jsonl. Times are the client's clock.
type Event struct {
	T         time.Time  `json:"t"`             // when the attempt started
	LatencyMS float64    `json:"ms,omitempty"`  // how long it took
	Kind      string     `json:"kind"`          // write, read, poll, mark
	Target    string     `json:"target"`        // "endpoint" or a node name
	Seq       int64      `json:"seq,omitempty"` // write sequence number
	Result    string     `json:"result,omitempty"`
	Err       string     `json:"err,omitempty"`
	Server    string     `json:"server,omitempty"` // inet_server_addr() that answered
	LSN       string     `json:"lsn,omitempty"`    // pg_current_wal_lsn() at write time
	TLI       int        `json:"tli,omitempty"`    // timeline of the server that answered
	Standby   *bool      `json:"standby,omitempty"`
	Poll      *NodeState `json:"poll,omitempty"`
	Note      string     `json:"note,omitempty"`
}

// End returns when the attempt finished.
func (e Event) End() time.Time {
	return e.T.Add(time.Duration(e.LatencyMS * float64(time.Millisecond)))
}

// NodeState is what one poll of a node saw.
type NodeState struct {
	Reachable   bool       `json:"reachable"`
	InRecovery  bool       `json:"in_recovery"`
	Server      string     `json:"server,omitempty"`
	LSN         string     `json:"lsn,omitempty"` // current (primary) or replayed (standby)
	TLI         int        `json:"tli,omitempty"` // primary: current timeline; standby: received timeline
	Receiver    string     `json:"receiver,omitempty"`
	Standbys    []Standby  `json:"standbys,omitempty"`
	Archiver    *Archiver  `json:"archiver,omitempty"`
	Settings    []Setting  `json:"settings,omitempty"`
	Version     string     `json:"version,omitempty"`
	CollectedAt *time.Time `json:"collected_at,omitempty"`
}

// Standby is one row of pg_stat_replication.
type Standby struct {
	Name      string `json:"name"`
	Addr      string `json:"addr"`
	State     string `json:"state"`
	SyncState string `json:"sync_state"`
	LagBytes  int64  `json:"lag_bytes"`
}

// Archiver is pg_stat_archiver.
type Archiver struct {
	ArchivedCount int64      `json:"archived_count"`
	LastArchived  string     `json:"last_archived_wal,omitempty"`
	LastTime      *time.Time `json:"last_archived_time,omitempty"`
	FailedCount   int64      `json:"failed_count"`
	LastFailed    string     `json:"last_failed_wal,omitempty"`
}

// Setting is one pg_settings row.
type Setting struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Writer appends events to a JSONL file. Safe for concurrent use.
type Writer struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

// Create opens path for appending.
func Create(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f, w: bufio.NewWriter(f)}, nil
}

// Write appends one event and flushes it, so a crash loses at most this line.
func (w *Writer) Write(e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return w.w.Flush()
}

// Close flushes, syncs and closes the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.w.Flush(); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		return err
	}
	return w.f.Close()
}

// ReadAll loads every event from a JSONL file. A torn last line is ignored.
func ReadAll(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			// Only tolerate a broken final line (interrupted write).
			if sc.Scan() {
				return nil, fmt.Errorf("%s:%d: %w", path, line, err)
			}
			break
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Run is one docbrown measurement session on disk.
type Run struct {
	ID  string
	Dir string
}

// Paths inside a run directory.
func (r Run) EventsPath() string { return filepath.Join(r.Dir, "events.jsonl") }
func (r Run) PrePath() string    { return filepath.Join(r.Dir, "pre.json") }
func (r Run) MetaPath() string   { return filepath.Join(r.Dir, "meta.json") }

// Meta describes a run.
type Meta struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Config    string    `json:"config"`
	OldNode   string    `json:"primary_before,omitempty"`
}

// NewRun creates a fresh run directory under runsDir and points runs/latest at it.
func NewRun(runsDir string, now time.Time) (Run, error) {
	id := now.UTC().Format("20060102-150405")
	dir := filepath.Join(runsDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Run{}, err
	}
	if err := os.WriteFile(filepath.Join(runsDir, "latest"), []byte(id+"\n"), 0o600); err != nil {
		return Run{}, err
	}
	return Run{ID: id, Dir: dir}, nil
}

// OpenRun finds an existing run. An empty id means the latest one.
func OpenRun(runsDir, id string) (Run, error) {
	if id == "" {
		b, err := os.ReadFile(filepath.Join(runsDir, "latest"))
		if err != nil {
			return Run{}, fmt.Errorf("no run given and no %s/latest; run `docbrown pre` first", runsDir)
		}
		id = string(trimNL(b))
	}
	// Accept a path to the run directory as well as a bare id.
	if st, err := os.Stat(id); err == nil && st.IsDir() {
		return Run{ID: filepath.Base(filepath.Clean(id)), Dir: id}, nil
	}
	dir := filepath.Join(runsDir, id)
	if _, err := os.Stat(dir); err != nil {
		return Run{}, fmt.Errorf("run %q not found in %s", id, runsDir)
	}
	return Run{ID: id, Dir: dir}, nil
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}

// WriteJSON writes v as indented JSON.
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// ReadJSON reads JSON into v.
func ReadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
