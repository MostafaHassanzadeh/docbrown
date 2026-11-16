package report

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/MostafaHassanzadeh/docbrown/internal/pgstate"
)

// WALTx is one transaction found in the old primary's WAL after the fork.
type WALTx struct {
	XID        uint64         `json:"xid"`
	Status     string         `json:"status"` // committed, aborted, unfinished
	CommitTime string         `json:"commit_time,omitempty"`
	FirstLSN   string         `json:"first_lsn"`
	Changes    map[string]int `json:"changes"` // "schema.table INSERT" -> count
}

// WALAnalysis summarises `pg_waldump` output past the fork point.
type WALAnalysis struct {
	Source       string         `json:"source"`
	FromLSN      string         `json:"from_lsn,omitempty"`
	Records      int            `json:"records_after_fork"`
	Committed    int            `json:"committed"`
	Aborted      int            `json:"aborted"`
	Unfinished   int            `json:"unfinished"`
	Tables       map[string]int `json:"committed_changes_by_table"`
	Transactions []WALTx        `json:"transactions"`
}

var (
	reRecord = regexp.MustCompile(`^rmgr:\s*(\S+)\s+len \(rec/tot\):\s*\d+/\s*\d+, tx:\s*(\d+), lsn: ([0-9A-Fa-f]+/[0-9A-Fa-f]+),.*?desc: (.*)$`)
	reRel    = regexp.MustCompile(`rel (\d+)/(\d+)/(\d+)`)
)

// analyzeWaldump reads `pg_waldump` text output and lists what the old primary
// committed after the fork. Row values are not in this output; tables and
// operation counts are.
func analyzeWaldump(ctx context.Context, db *sql.DB, path, forkLSN string) (*WALAnalysis, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var fork pgstate.LSN
	if forkLSN != "" {
		fork, _ = pgstate.ParseLSN(forkLSN)
	}
	wa := &WALAnalysis{Source: path, FromLSN: forkLSN, Tables: map[string]int{}}
	txs := map[uint64]*WALTx{}
	var order []uint64
	names := newRelNamer(ctx, db)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		m := reRecord.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		rmgr, desc := m[1], m[4]
		lsn, err := pgstate.ParseLSN(m[3])
		if err != nil || (fork > 0 && lsn < fork) {
			continue
		}
		wa.Records++
		xid, _ := strconv.ParseUint(m[2], 10, 64)
		if xid == 0 {
			continue
		}
		tx := txs[xid]
		if tx == nil {
			tx = &WALTx{XID: xid, Status: "unfinished", FirstLSN: lsn.String(), Changes: map[string]int{}}
			txs[xid] = tx
			order = append(order, xid)
		}
		op := strings.Fields(desc)
		if len(op) == 0 {
			continue
		}
		switch {
		case rmgr == "Transaction" && strings.HasPrefix(op[0], "COMMIT"):
			tx.Status = "committed"
			rest := strings.TrimSpace(strings.TrimPrefix(desc, op[0]))
			if i := strings.IndexByte(rest, ';'); i >= 0 {
				rest = rest[:i]
			}
			tx.CommitTime = rest
		case rmgr == "Transaction" && strings.HasPrefix(op[0], "ABORT"):
			tx.Status = "aborted"
		case rmgr == "Heap" || rmgr == "Heap2":
			name := strings.TrimSuffix(op[0], ":")
			if strings.HasPrefix(name, "INSERT") || strings.HasPrefix(name, "MULTI_INSERT") ||
				strings.Contains(name, "UPDATE") || strings.HasPrefix(name, "DELETE") {
				table := "unknown relation"
				if r := reRel.FindStringSubmatch(desc); r != nil {
					table = names.name(r[2], r[3])
				}
				tx.Changes[table+" "+normalizeOp(name)]++
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for _, xid := range order {
		tx := txs[xid]
		switch tx.Status {
		case "committed":
			wa.Committed++
			for k, v := range tx.Changes {
				wa.Tables[k] += v
			}
		case "aborted":
			wa.Aborted++
		default:
			wa.Unfinished++
		}
		wa.Transactions = append(wa.Transactions, *tx)
	}
	sort.SliceStable(wa.Transactions, func(i, j int) bool { return wa.Transactions[i].FirstLSN < wa.Transactions[j].FirstLSN })
	return wa, nil
}

func normalizeOp(op string) string {
	switch {
	case strings.HasPrefix(op, "INSERT"), strings.HasPrefix(op, "MULTI_INSERT"):
		return "INSERT"
	case strings.Contains(op, "UPDATE"):
		return "UPDATE"
	case strings.HasPrefix(op, "DELETE"):
		return "DELETE"
	}
	return op
}

// relNamer maps relfilenodes to table names using the new primary's catalog.
type relNamer struct {
	ctx   context.Context
	db    *sql.DB
	dbOID string
	cache map[string]string
}

func newRelNamer(ctx context.Context, db *sql.DB) *relNamer {
	n := &relNamer{ctx: ctx, db: db, cache: map[string]string{}}
	_ = db.QueryRowContext(ctx, `SELECT oid::text FROM pg_database WHERE datname = current_database()`).Scan(&n.dbOID)
	return n
}

func (n *relNamer) name(dbOID, filenode string) string {
	key := dbOID + "/" + filenode
	if v, ok := n.cache[key]; ok {
		return v
	}
	v := fmt.Sprintf("db %s relfilenode %s", dbOID, filenode)
	if dbOID == n.dbOID {
		var s string
		err := n.db.QueryRowContext(n.ctx, `
			SELECT n.nspname || '.' || c.relname FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE pg_relation_filenode(c.oid) = $1::oid`, filenode).Scan(&s)
		if err == nil {
			v = s
		}
	}
	n.cache[key] = v
	return v
}
