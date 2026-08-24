package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const (
	StateQueued  = "queued"
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
)

const AllKey = "*"

type Pair struct {
	ID           int64
	GitHubID     int64
	ForgejoID    int64
	GitHubOwner  string
	GitHubName   string
	ForgejoOwner string
	ForgejoName  string
	PeerGone     bool
	GoneSide     string
	Established  bool
	LastMeta     string
}

type Job struct {
	ID      int64
	PairKey string
	State   string
	Error   string
}

type DB struct {
	sql *sql.DB
}

func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=FULL",
	} {
		if _, err := conn.Exec(pragma); err != nil {
			conn.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	if err := migrate(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return &DB{sql: conn}, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS pairs (
  id INTEGER PRIMARY KEY,
  github_id INTEGER NOT NULL DEFAULT 0,
  forgejo_id INTEGER NOT NULL DEFAULT 0,
  github_owner TEXT NOT NULL,
  github_name TEXT NOT NULL,
  forgejo_owner TEXT NOT NULL,
  forgejo_name TEXT NOT NULL,
  peer_gone INTEGER NOT NULL DEFAULT 0,
  gone_side TEXT NOT NULL DEFAULT '',
  established INTEGER NOT NULL DEFAULT 0,
  last_meta TEXT NOT NULL DEFAULT '',
  UNIQUE(github_owner, github_name),
  UNIQUE(forgejo_owner, forgejo_name)
);
CREATE TABLE IF NOT EXISTS seen (
  sha TEXT PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS queue (
  id INTEGER PRIMARY KEY,
  pair_key TEXT NOT NULL,
  state TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS queue_state ON queue(state, id);
`); err != nil {
		return err
	}
	if err := ensurePairColumn(db, "established", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return ensurePairColumn(db, "last_meta", "TEXT NOT NULL DEFAULT ''")
}

func ensurePairColumn(db *sql.DB, name, decl string) error {
	rows, err := db.Query(`PRAGMA table_info(pairs)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var col, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &col, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if col == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE pairs ADD COLUMN ` + name + ` ` + decl)
	return err
}

func (d *DB) Close() error { return d.sql.Close() }

func (d *DB) UpsertPair(p Pair) (int64, error) {
	existing, err := d.FindPair(p.GitHubOwner, p.GitHubName, p.ForgejoOwner, p.ForgejoName)
	if err != nil {
		return 0, err
	}
	if existing != nil {
		established := existing.Established || p.Established
		lastMeta := p.LastMeta
		if lastMeta == "" {
			lastMeta = existing.LastMeta
		}
		_, err := d.sql.Exec(`
UPDATE pairs SET github_id=?, forgejo_id=?, github_owner=?, github_name=?, forgejo_owner=?, forgejo_name=?, established=?, last_meta=?
WHERE id=?`,
			p.GitHubID, p.ForgejoID, p.GitHubOwner, p.GitHubName, p.ForgejoOwner, p.ForgejoName, boolInt(established), lastMeta, existing.ID)
		return existing.ID, err
	}
	res, err := d.sql.Exec(`
INSERT INTO pairs (github_id, forgejo_id, github_owner, github_name, forgejo_owner, forgejo_name, established, last_meta)
VALUES (?,?,?,?,?,?,?,?)`,
		p.GitHubID, p.ForgejoID, p.GitHubOwner, p.GitHubName, p.ForgejoOwner, p.ForgejoName, boolInt(p.Established), p.LastMeta)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const pairCols = `id, github_id, forgejo_id, github_owner, github_name, forgejo_owner, forgejo_name, peer_gone, gone_side, established, last_meta`

func (d *DB) FindPair(ghOwner, ghName, fjOwner, fjName string) (*Pair, error) {
	return d.scanPair(`SELECT `+pairCols+` FROM pairs WHERE (github_owner=? AND github_name=?) OR (forgejo_owner=? AND forgejo_name=?)`,
		ghOwner, ghName, fjOwner, fjName)
}

func (d *DB) PairByGitHub(owner, name string) (*Pair, error) {
	return d.scanPair(`SELECT `+pairCols+` FROM pairs WHERE github_owner=? AND github_name=?`, owner, name)
}

func (d *DB) PairByForgejo(owner, name string) (*Pair, error) {
	return d.scanPair(`SELECT `+pairCols+` FROM pairs WHERE forgejo_owner=? AND forgejo_name=?`, owner, name)
}

func (d *DB) PairByGitHubID(id int64) (*Pair, error) {
	if id == 0 {
		return nil, nil
	}
	return d.scanPair(`SELECT `+pairCols+` FROM pairs WHERE github_id=?`, id)
}

func (d *DB) PairByForgejoID(id int64) (*Pair, error) {
	if id == 0 {
		return nil, nil
	}
	return d.scanPair(`SELECT `+pairCols+` FROM pairs WHERE forgejo_id=?`, id)
}

func (d *DB) ListPairs() ([]Pair, error) {
	rows, err := d.sql.Query(`SELECT ` + pairCols + ` FROM pairs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pair
	for rows.Next() {
		p, err := scanPairRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) MarkPeerGone(id int64, side string) error {
	_, err := d.sql.Exec(`UPDATE pairs SET peer_gone=1, gone_side=? WHERE id=?`, side, id)
	return err
}

func (d *DB) RememberSHA(sha string) error {
	if sha == "" {
		return nil
	}
	_, err := d.sql.Exec(`INSERT OR IGNORE INTO seen (sha) VALUES (?)`, sha)
	return err
}

func (d *DB) SeenSHA(sha string) (bool, error) {
	if sha == "" {
		return false, nil
	}
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM seen WHERE sha=?`, sha).Scan(&n)
	return n > 0, err
}

func (d *DB) Enqueue(pairKey string) (already bool, err error) {
	if pairKey == "" {
		pairKey = AllKey
	}
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM queue WHERE pair_key=? AND state=?`, pairKey, StateQueued).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	if pairKey != AllKey {
		if err := d.sql.QueryRow(`SELECT COUNT(*) FROM queue WHERE pair_key=? AND state=?`, AllKey, StateQueued).Scan(&n); err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	_, err = d.sql.Exec(`INSERT INTO queue (pair_key, state, created_at) VALUES (?,?,?)`, pairKey, StateQueued, time.Now().Unix())
	return false, err
}

func (d *DB) NextQueued() (*Job, error) {
	row := d.sql.QueryRow(`SELECT id FROM queue WHERE state=? ORDER BY id ASC LIMIT 1`, StateQueued)
	var id int64
	err := row.Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j := &Job{}
	err = d.sql.QueryRow(`SELECT id, pair_key, state, error FROM queue WHERE id=?`, id).Scan(&j.ID, &j.PairKey, &j.State, &j.Error)
	if err != nil {
		return nil, err
	}
	if _, err := d.sql.Exec(`UPDATE queue SET state=? WHERE id=?`, StateRunning, id); err != nil {
		return nil, err
	}
	j.State = StateRunning
	return j, nil
}

func (d *DB) Finish(id int64, failed error) error {
	state := StateDone
	msg := ""
	if failed != nil {
		state = StateFailed
		msg = failed.Error()
	}
	_, err := d.sql.Exec(`UPDATE queue SET state=?, error=? WHERE id=?`, state, msg, id)
	return err
}

func (d *DB) scanPair(q string, args ...any) (*Pair, error) {
	p, err := scanPairRow(d.sql.QueryRow(q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPairRow(row rowScanner) (Pair, error) {
	p := Pair{}
	var gone, established int
	err := row.Scan(&p.ID, &p.GitHubID, &p.ForgejoID, &p.GitHubOwner, &p.GitHubName, &p.ForgejoOwner, &p.ForgejoName, &gone, &p.GoneSide, &established, &p.LastMeta)
	if err != nil {
		return Pair{}, err
	}
	p.PeerGone = gone == 1
	p.Established = established == 1
	return p, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
