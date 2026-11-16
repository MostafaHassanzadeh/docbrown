// Package probe writes and reads through the endpoint and polls every node
// while a switchover happens.
package probe

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/MostafaHassanzadeh/docbrown/internal/config"
	"github.com/MostafaHassanzadeh/docbrown/internal/events"
	"github.com/MostafaHassanzadeh/docbrown/internal/pgstate"
)

// Run probes until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config, run events.Run, out io.Writer) error {
	w, err := events.Create(run.EventsPath())
	if err != nil {
		return err
	}
	defer w.Close()
	_ = w.Write(events.Event{T: time.Now(), Kind: events.KindMark, Note: "probe-start"})

	con := &console{out: out, last: map[string]string{}}
	var wg sync.WaitGroup
	wg.Add(2 + len(cfg.Nodes))
	go func() { defer wg.Done(); writer(ctx, cfg, run.ID, w, con) }()
	go func() { defer wg.Done(); reader(ctx, cfg, w, con) }()
	for _, n := range cfg.Nodes {
		go func(n config.Node) { defer wg.Done(); poller(ctx, cfg, n, w, con) }(n)
	}
	wg.Wait()
	return w.Write(events.Event{T: time.Now(), Kind: events.KindMark, Note: "probe-stop"})
}

// conn keeps one connection to a DSN and throws it away after any error, so the
// next attempt dials again and follows the endpoint to wherever it now points.
type conn struct {
	dsn string
	db  *sql.DB
}

// get returns a connected handle. A fresh connection is established (and
// pinged) before any probe statement is sent, so a failure here means the
// statement never left the client: it is a plain "fail", never "in-doubt".
func (c *conn) get(timeout time.Duration) (*sql.DB, error) {
	if c.db == nil {
		db, err := pgstate.Open(c.dsn)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err = db.PingContext(ctx)
		cancel()
		if err != nil {
			db.Close()
			return nil, err
		}
		c.db = db
	}
	return c.db, nil
}

func (c *conn) reset() {
	if c.db != nil {
		c.db.Close()
		c.db = nil
	}
}

func writer(ctx context.Context, cfg *config.Config, runID string, w *events.Writer, con *console) {
	c := &conn{dsn: cfg.Endpoint}
	defer c.reset()
	tick := time.NewTicker(cfg.Interval())
	defer tick.Stop()
	var seq int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		seq++
		start := time.Now()
		e := events.Event{T: start, Kind: events.KindWrite, Target: "endpoint", Seq: seq}
		db, err := c.get(cfg.Timeout())
		if err != nil {
			e.Result = events.ResultFail // could not connect: nothing was sent
		} else {
			opCtx, cancel := context.WithTimeout(context.Background(), cfg.Timeout())
			err = db.QueryRowContext(opCtx, pgstate.InsertProbe, runID, seq, start).Scan(&e.Server, &e.LSN, &e.TLI)
			cancel()
			e.Result = pgstate.Classify(err)
		}
		e.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
		if err != nil {
			e.Err = err.Error()
			c.reset()
		}
		_ = w.Write(e)
		con.change("write", e.Result+e.Server, fmt.Sprintf("write %-8s %s", e.Result, detail(e)))
	}
}

func reader(ctx context.Context, cfg *config.Config, w *events.Writer, con *console) {
	c := &conn{dsn: cfg.Endpoint}
	defer c.reset()
	tick := time.NewTicker(cfg.Interval())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		start := time.Now()
		e := events.Event{T: start, Kind: events.KindRead, Target: "endpoint"}
		var standby bool
		db, err := c.get(cfg.Timeout())
		if err == nil {
			opCtx, cancel := context.WithTimeout(context.Background(), cfg.Timeout())
			err = db.QueryRowContext(opCtx, pgstate.ReadProbe).Scan(&standby, &e.Server)
			cancel()
		}
		e.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
		if err != nil {
			e.Result = events.ResultFail
			e.Err = err.Error()
			c.reset()
		} else {
			e.Result = events.ResultOK
			e.Standby = &standby
		}
		_ = w.Write(e)
		con.change("read", e.Result+e.Server, fmt.Sprintf("read  %-8s %s", e.Result, detail(e)))
	}
}

func poller(ctx context.Context, cfg *config.Config, n config.Node, w *events.Writer, con *console) {
	c := &conn{dsn: n.DSN}
	defer c.reset()
	tick := time.NewTicker(cfg.PollInterval())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		start := time.Now()
		e := events.Event{T: start, Kind: events.KindPoll, Target: n.Name}
		var st *events.NodeState
		db, err := c.get(cfg.Timeout())
		if err == nil {
			opCtx, cancel := context.WithTimeout(context.Background(), cfg.Timeout())
			st, err = pgstate.Collect(opCtx, db, false)
			cancel()
		}
		e.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
		role := "down"
		if err != nil {
			e.Err = err.Error()
			e.Poll = &events.NodeState{Reachable: false}
			c.reset()
		} else {
			e.Poll = st
			role = "primary"
			if st.InRecovery {
				role = "standby"
			}
			role = fmt.Sprintf("%s tli=%d", role, st.TLI)
		}
		_ = w.Write(e)
		con.change("node:"+n.Name, role, fmt.Sprintf("node  %-12s %s", n.Name, role))
	}
}

func detail(e events.Event) string {
	if e.Err != "" {
		return e.Err
	}
	s := "via " + e.Server
	if e.TLI > 0 {
		s += fmt.Sprintf(" tli=%d", e.TLI)
	}
	return s
}

// console prints only state changes, so the terminal stays readable at 20 writes/s.
type console struct {
	mu   sync.Mutex
	out  io.Writer
	last map[string]string
}

func (c *console) change(key, state, line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last[key] == state {
		return
	}
	c.last[key] = state
	fmt.Fprintf(c.out, "%s  %s\n", time.Now().Format("15:04:05.000"), line)
}
