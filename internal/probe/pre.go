package probe

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/MostafaHassanzadeh/docbrown/internal/config"
	"github.com/MostafaHassanzadeh/docbrown/internal/events"
	"github.com/MostafaHassanzadeh/docbrown/internal/pgstate"
)

// PreSnapshot is what `docbrown pre` saves.
type PreSnapshot struct {
	TakenAt time.Time                    `json:"taken_at"`
	Nodes   map[string]*events.NodeState `json:"nodes"`
	Errors  map[string]string            `json:"errors,omitempty"`
	Primary string                       `json:"primary"`
}

// Pre records the cluster state before the switchover and prepares the probe table.
func Pre(cfg *config.Config, cfgPath string, out io.Writer) (events.Run, error) {
	snap := PreSnapshot{TakenAt: time.Now(), Nodes: map[string]*events.NodeState{}, Errors: map[string]string{}}
	var primaries []string
	for _, n := range cfg.Nodes {
		db, err := pgstate.Open(n.DSN)
		if err != nil {
			return events.Run{}, fmt.Errorf("node %s: %w", n.Name, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		st, err := pgstate.Collect(ctx, db, true)
		cancel()
		db.Close()
		if err != nil {
			snap.Errors[n.Name] = err.Error()
			fmt.Fprintf(out, "  %-12s UNREACHABLE: %v\n", n.Name, err)
			continue
		}
		snap.Nodes[n.Name] = st
		role := "primary"
		if st.InRecovery {
			role = "standby"
		} else {
			primaries = append(primaries, n.Name)
		}
		fmt.Fprintf(out, "  %-12s %-8s server=%s lsn=%s tli=%d\n", n.Name, role, st.Server, st.LSN, st.TLI)
		for _, s := range st.Standbys {
			fmt.Fprintf(out, "               standby %s (%s) %s/%s lag=%d bytes\n", s.Name, s.Addr, s.State, s.SyncState, s.LagBytes)
		}
	}
	switch len(primaries) {
	case 0:
		return events.Run{}, fmt.Errorf("no node is a primary right now")
	case 1:
		snap.Primary = primaries[0]
	default:
		return events.Run{}, fmt.Errorf("more than one primary right now: %v (split-brain?)", primaries)
	}

	// Create the probe table through the endpoint, i.e. on the current primary.
	db, err := pgstate.Open(cfg.Endpoint)
	if err != nil {
		return events.Run{}, fmt.Errorf("endpoint: %w", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, pgstate.ProbeTableDDL); err != nil {
		return events.Run{}, fmt.Errorf("create docbrown_probe through endpoint: %w", err)
	}

	run, err := events.NewRun(cfg.RunsDir, snap.TakenAt)
	if err != nil {
		return events.Run{}, err
	}
	if err := events.WriteJSON(run.PrePath(), snap); err != nil {
		return events.Run{}, err
	}
	meta := events.Meta{ID: run.ID, CreatedAt: snap.TakenAt, Config: cfgPath, OldNode: snap.Primary}
	if err := events.WriteJSON(run.MetaPath(), meta); err != nil {
		return events.Run{}, err
	}
	return run, nil
}
