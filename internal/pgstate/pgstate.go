// Package pgstate talks to PostgreSQL: connections, node state, LSNs and timelines.
package pgstate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/MostafaHassanzadeh/docbrown/internal/events"
)

// Open returns a single-connection handle for dsn. No connection is made yet.
func Open(dsn string) (*sql.DB, error) {
	connector, err := pq.NewConnector(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid dsn: %w", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	return db, nil
}

// timelineExpr turns the current WAL file name into its timeline number (primary only).
const timelineExpr = `('x' || substr(pg_walfile_name(pg_current_wal_lsn()), 1, 8))::bit(32)::int`

// settingsOfInterest are recorded by `docbrown pre`.
var settingsOfInterest = []string{
	"server_version", "synchronous_commit", "synchronous_standby_names",
	"wal_level", "archive_mode", "archive_timeout", "wal_log_hints",
	"data_checksums", "full_page_writes", "hot_standby",
}

// Collect reads the state of one node. Errors after the first query are
// tolerated: a partial picture is better than none in the middle of a switchover.
func Collect(ctx context.Context, db *sql.DB, withSettings bool) (*events.NodeState, error) {
	st := &events.NodeState{}
	var lsn sql.NullString
	var tli sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT pg_is_in_recovery(),
		       coalesce(host(inet_server_addr()), 'local'),
		       CASE WHEN pg_is_in_recovery() THEN pg_last_wal_replay_lsn()::text
		            ELSE pg_current_wal_lsn()::text END,
		       CASE WHEN pg_is_in_recovery() THEN NULL ELSE `+timelineExpr+` END`).
		Scan(&st.InRecovery, &st.Server, &lsn, &tli)
	if err != nil {
		return nil, err
	}
	st.Reachable = true
	st.LSN = lsn.String
	st.TLI = int(tli.Int64)

	if st.InRecovery {
		var rtli sql.NullInt64
		var status sql.NullString
		err := db.QueryRowContext(ctx, `SELECT received_tli, status FROM pg_stat_wal_receiver`).Scan(&rtli, &status)
		if err == nil {
			st.TLI = int(rtli.Int64)
			st.Receiver = status.String
		} else if errors.Is(err, sql.ErrNoRows) {
			st.Receiver = "none"
		}
	} else {
		rows, err := db.QueryContext(ctx, `
			SELECT coalesce(application_name, ''), coalesce(client_addr::text, 'local'),
			       coalesce(state, ''), coalesce(sync_state, ''),
			       coalesce(pg_wal_lsn_diff(pg_current_wal_lsn(), coalesce(replay_lsn, flush_lsn, sent_lsn)), -1)::bigint
			FROM pg_stat_replication`)
		if err == nil {
			for rows.Next() {
				var s events.Standby
				if rows.Scan(&s.Name, &s.Addr, &s.State, &s.SyncState, &s.LagBytes) == nil {
					st.Standbys = append(st.Standbys, s)
				}
			}
			rows.Close()
		}
	}

	var a events.Archiver
	var lastWal, lastFailed sql.NullString
	var lastTime sql.NullTime
	if err := db.QueryRowContext(ctx, `
		SELECT archived_count, last_archived_wal, last_archived_time, failed_count, last_failed_wal
		FROM pg_stat_archiver`).Scan(&a.ArchivedCount, &lastWal, &lastTime, &a.FailedCount, &lastFailed); err == nil {
		a.LastArchived = lastWal.String
		a.LastFailed = lastFailed.String
		if lastTime.Valid {
			t := lastTime.Time
			a.LastTime = &t
		}
		st.Archiver = &a
	}

	if withSettings {
		rows, err := db.QueryContext(ctx, `SELECT name, setting FROM pg_settings WHERE name = ANY($1) ORDER BY name`,
			pq.Array(settingsOfInterest))
		if err == nil {
			for rows.Next() {
				var s events.Setting
				if rows.Scan(&s.Name, &s.Value) == nil {
					st.Settings = append(st.Settings, s)
				}
			}
			rows.Close()
		}
		_ = db.QueryRowContext(ctx, `SELECT version()`).Scan(&st.Version)
	}
	return st, nil
}

// ProbeTableDDL creates the table docbrown writes into. It never touches anything else.
const ProbeTableDDL = `
CREATE TABLE IF NOT EXISTS docbrown_probe (
    run_id    text        NOT NULL,
    seq       bigint      NOT NULL,
    client_ts timestamptz NOT NULL,
    server_ts timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (run_id, seq)
)`

// InsertProbe is the write docbrown performs on every tick.
const InsertProbe = `
INSERT INTO docbrown_probe (run_id, seq, client_ts) VALUES ($1, $2, $3)
RETURNING coalesce(host(inet_server_addr()), 'local'), pg_current_wal_lsn()::text, ` + timelineExpr

// ReadProbe is the read docbrown performs on every tick.
const ReadProbe = `SELECT pg_is_in_recovery(), coalesce(host(inet_server_addr()), 'local')`

// Classify decides what a failed write means for durability.
//
//   - fail:     the server answered with an error, or the request never left
//     the client. The row was not committed.
//   - in-doubt: the request may have reached the server and we got no reliable
//     answer (timeout, connection cut mid-flight). It may or may not be there.
func Classify(err error) string {
	if err == nil {
		return events.ResultAck
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		// 57014 = query_canceled: our own timeout fired. The cancel can race the
		// commit, so we cannot be sure.
		if pqErr.Code == "57014" {
			return events.ResultInDoubt
		}
		return events.ResultFail
	}
	if errors.Is(err, driver.ErrBadConn) {
		return events.ResultFail // database/sql guarantees nothing was sent
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return events.ResultFail
	}
	if strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "no such host") {
		return events.ResultFail
	}
	return events.ResultInDoubt
}

// LSN is a WAL position.
type LSN uint64

// ParseLSN parses "16/B374D848".
func ParseLSN(s string) (LSN, error) {
	hi, lo, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok {
		return 0, fmt.Errorf("invalid LSN %q", s)
	}
	h, err := strconv.ParseUint(hi, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q", s)
	}
	l, err := strconv.ParseUint(lo, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q", s)
	}
	return LSN(h<<32 | l), nil
}

func (l LSN) String() string { return fmt.Sprintf("%X/%X", uint64(l)>>32, uint64(l)&0xFFFFFFFF) }

// HistoryEntry is one line of a timeline .history file.
type HistoryEntry struct {
	ParentTLI   int
	SwitchPoint LSN
	Reason      string
}

// ParseHistory parses the content of a NNNNNNNN.history file.
func ParseHistory(content string) ([]HistoryEntry, error) {
	var out []HistoryEntry
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return nil, fmt.Errorf("bad history line %q", line)
		}
		tli, err := strconv.Atoi(f[0])
		if err != nil {
			return nil, fmt.Errorf("bad history line %q", line)
		}
		lsn, err := ParseLSN(f[1])
		if err != nil {
			return nil, err
		}
		out = append(out, HistoryEntry{ParentTLI: tli, SwitchPoint: lsn, Reason: strings.Join(f[2:], " ")})
	}
	return out, nil
}

// ReadHistory asks the server for its timeline history file. It needs
// superuser or pg_read_server_files; callers fall back to a file on disk.
func ReadHistory(ctx context.Context, db *sql.DB, tli int) (string, error) {
	var content string
	err := db.QueryRowContext(ctx, `SELECT pg_read_file('pg_wal/' || lpad(to_hex($1::int), 8, '0') || '.history')`, tli).Scan(&content)
	return content, err
}

// WithTimeout is a small helper.
func WithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
