// Package store persists only the state whose recovery semantics are
// specified: endpoint registration, replay cursors, and generation fences.
// Message bodies and in-flight turns are deliberately not persisted; a crash
// loses a turn in progress and it restarts only from fresh live events.
package store

import (
	"database/sql"
	"fmt"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/protocol"

	_ "modernc.org/sqlite"
)

// Cursor is the persisted replay state for one endpoint.
type Cursor struct {
	IDs       []string
	Watermark int64
}

// Store is a SQLite-backed persistence layer. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open creates or opens the database at path and applies schema migrations.
// The file is created with mode 0600.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite is single-writer; one connection avoids lock surprises.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS endpoints (
	id         TEXT PRIMARY KEY,
	harness    TEXT NOT NULL,
	session_id TEXT NOT NULL,
	nick       TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS seen_events (
	endpoint_id TEXT NOT NULL,
	event_id    TEXT NOT NULL,
	sent_at     INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (endpoint_id, event_id)
);
CREATE TABLE IF NOT EXISTS cursors (
	endpoint_id TEXT PRIMARY KEY,
	watermark   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS generations (
	endpoint_id TEXT PRIMARY KEY,
	gen         INTEGER NOT NULL DEFAULT 0
);
INSERT INTO schema_version (version) SELECT 1
WHERE NOT EXISTS (SELECT 1 FROM schema_version);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// SaveEndpoint inserts or updates one endpoint registration.
func (s *Store) SaveEndpoint(ep core.Endpoint) error {
	_, err := s.db.Exec(
		`INSERT INTO endpoints (id, harness, session_id, nick) VALUES (?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET harness=excluded.harness,
		 session_id=excluded.session_id, nick=excluded.nick`,
		string(ep.ID), ep.Harness, ep.SessionID, ep.Nick,
	)
	return err
}

// LoadEndpoints returns all registered endpoints. An empty database yields an
// empty slice, not an error.
func (s *Store) LoadEndpoints() ([]core.Endpoint, error) {
	rows, err := s.db.Query(`SELECT id, harness, session_id, nick FROM endpoints ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []core.Endpoint
	for rows.Next() {
		var ep core.Endpoint
		var id string
		if err := rows.Scan(&id, &ep.Harness, &ep.SessionID, &ep.Nick); err != nil {
			return nil, err
		}
		ep.ID = protocol.EndpointID(id)
		out = append(out, ep)
	}
	return out, rows.Err()
}

// SaveCursor records one applied event and advances the endpoint watermark.
func (s *Store) SaveCursor(id protocol.EndpointID, eventID string, sentAt int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO seen_events (endpoint_id, event_id, sent_at) VALUES (?, ?, ?)`,
		string(id), eventID, sentAt,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO cursors (endpoint_id, watermark) VALUES (?, ?)
		 ON CONFLICT(endpoint_id) DO UPDATE SET watermark=MAX(watermark, excluded.watermark)`,
		string(id), sentAt,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// LoadCursor returns the persisted replay state for one endpoint.
func (s *Store) LoadCursor(id protocol.EndpointID) (Cursor, error) {
	var c Cursor
	err := s.db.QueryRow(
		`SELECT COALESCE(watermark, 0) FROM cursors WHERE endpoint_id = ?`, string(id),
	).Scan(&c.Watermark)
	if err == sql.ErrNoRows {
		return Cursor{}, nil
	}
	if err != nil {
		return Cursor{}, err
	}

	rows, err := s.db.Query(
		`SELECT event_id FROM seen_events WHERE endpoint_id = ?`, string(id),
	)
	if err != nil {
		return Cursor{}, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var ev string
		if err := rows.Scan(&ev); err != nil {
			return Cursor{}, err
		}
		c.IDs = append(c.IDs, ev)
	}
	return c, rows.Err()
}

// SaveGeneration overwrites an endpoint's persisted generation fence.
func (s *Store) SaveGeneration(id protocol.EndpointID, gen uint64) error {
	_, err := s.db.Exec(
		`INSERT INTO generations (endpoint_id, gen) VALUES (?, ?)
		 ON CONFLICT(endpoint_id) DO UPDATE SET gen=excluded.gen`,
		string(id), gen,
	)
	return err
}

// LoadGeneration returns an endpoint's persisted generation, or 0 when unset.
func (s *Store) LoadGeneration(id protocol.EndpointID) (uint64, error) {
	var gen uint64
	err := s.db.QueryRow(
		`SELECT gen FROM generations WHERE endpoint_id = ?`, string(id),
	).Scan(&gen)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return gen, nil
}

// BumpGeneration atomically increments and returns an endpoint's generation.
// This is the persistence-side counterpart of Disable's fence.
func (s *Store) BumpGeneration(id protocol.EndpointID) (uint64, error) {
	res, err := s.db.Exec(
		`INSERT INTO generations (endpoint_id, gen) VALUES (?, 1)
		 ON CONFLICT(endpoint_id) DO UPDATE SET gen=gen+1`,
		string(id),
	)
	if err != nil {
		return 0, err
	}
	_ = res
	return s.LoadGeneration(id)
}
