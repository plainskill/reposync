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
	_, err := db.Exec(`
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
`)
	return err
}

func (d *DB) Close() error { return d.sql.Close() }

func (d *DB) UpsertPair(p Pair) (int64, error) {
	existing, err := d.FindPair(p.GitHubOwner, p.GitHubName, p.ForgejoOwner, p.ForgejoName)
	if err != nil {
		return 0, err
	}
	if existing != nil {
		_, err := d.sql.Exec(`
UPDATE pairs SET github_id=?, forgejo_id=?, github_owner=?, github_name=?, forgejo_owner=?, forgejo_name=?
WHERE id=?`,
			p.GitHubID, p.ForgejoID, p.GitHubOwner, p.GitHubName, p.ForgejoOwner, p.ForgejoName, existing.ID)
		return existing.ID, err
	}
	res, err := d.sql.Exec(`
INSERT INTO pairs (github_id, forgejo_id, github_owner, github_name, forgejo_owner, forgejo_name)
VALUES (?,?,?,?,?,?)`,
		p.GitHubID, p.ForgejoID, p.GitHubOwner, p.GitHubName, p.ForgejoOwner, p.ForgejoName)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) FindPair(ghOwner, ghName, fjOwner, fjName string) (*Pair, error) {
	return d.scanPair(`SELECT id, github_id, forgejo_id, github_owner, github_name, forgejo_owner, forgejo_name, peer_gone, gone_side
FROM pairs WHERE (github_owner=? AND github_name=?) OR (forgejo_owner=? AND forgejo_name=?)`,
		ghOwner, ghName, fjOwner, fjName)
}

func (d *DB) PairByGitHub(owner, name string) (*Pair, error) {
	return d.scanPair(`SELECT id, github_id, forgejo_id, github_owner, github_name, forgejo_owner, forgejo_name, peer_gone, gone_side
FROM pairs WHERE github_owner=? AND github_name=?`, owner, name)
}

func (d *DB) PairByForgejo(owner, name string) (*Pair, error) {
	return d.scanPair(`SELECT id, github_id, forgejo_id, github_owner, github_name, forgejo_owner, forgejo_name, peer_gone, gone_side
FROM pairs WHERE forgejo_owner=? AND forgejo_name=?`, owner, name)
}

func (d *DB) PairByGitHubID(id int64) (*Pair, error) {
	if id == 0 {
		return nil, nil
	}
	return d.scanPair(`SELECT id, github_id, forgejo_id, github_owner, github_name, forgejo_owner, forgejo_name, peer_gone, gone_side
FROM pairs WHERE github_id=?`, id)
}

func (d *DB) PairByForgejoID(id int64) (*Pair, error) {
	if id == 0 {
		return nil, nil
	}
	return d.scanPair(`SELECT id, github_id, forgejo_id, github_owner, github_name, forgejo_owner, forgejo_name, peer_gone, gone_side
FROM pairs WHERE forgejo_id=?`, id)
}

func (d *DB) ListPairs() ([]Pair, error) {
	rows, err := d.sql.Query(`SELECT id, github_id, forgejo_id, github_owner, github_name, forgejo_owner, forgejo_name, peer_gone, gone_side FROM pairs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pair
	for rows.Next() {
		var p Pair
		var gone int
		if err := rows.Scan(&p.ID, &p.GitHubID, &p.ForgejoID, &p.GitHubOwner, &p.GitHubName, &p.ForgejoOwner, &p.ForgejoName, &gone, &p.GoneSide); err != nil {
			return nil, err
		}
		p.PeerGone = gone == 1
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
	p := &Pair{}
	var gone int
	err := d.sql.QueryRow(q, args...).Scan(&p.ID, &p.GitHubID, &p.ForgejoID, &p.GitHubOwner, &p.GitHubName, &p.ForgejoOwner, &p.ForgejoName, &gone, &p.GoneSide)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.PeerGone = gone == 1
	return p, nil
}
