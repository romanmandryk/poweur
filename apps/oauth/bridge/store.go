package bridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo in the image
)

// ErrNotFound is returned when a row does not exist (or has expired).
var ErrNotFound = errors.New("not found")

// Store is the bridge's only state: transactions, replay guards, browser
// sessions, codes, tokens, consents, clients, signing keys and audit events.
// It never holds an identity private key, a relay credential or a DAV token.
//
// v1 is SQLite with a single connection, which makes every read-modify-write
// below atomic without further ceremony. A multi-instance deployment needs a
// shared database behind the same methods (EPIC-022 E22-T8).
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// OpenStore opens (creating if needed) the database at path. ":memory:" gives
// a private in-memory database, which is what the tests use.
func OpenStore(ctx context.Context, path string) (*Store, error) {
	dsn := path
	if path != ":memory:" {
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	// One connection: SQLite has one writer anyway, an in-memory database is
	// per-connection, and it makes updateJSON's read-modify-write atomic.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Check reads from the database: schema, transactions and the pairwise
// secret's table, so a missing or unreadable file fails.
func (s *Store) Check(ctx context.Context) error {
	var version, n int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM secrets`).Scan(&n); err != nil {
		return err
	}
	return s.db.QueryRowContext(ctx, `SELECT count(*) FROM transactions WHERE expires_at > ?`, unix(s.now())).Scan(&n)
}

// Backup writes a consistent copy of the database to path (VACUUM INTO),
// which must not exist. The copy holds sealed signing keys and the pairwise
// subject secret: restore it with the same key-encryption key and every
// client keeps seeing the same subjects and verifying the same kids.
func (s *Store) Backup(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS transactions (
		id          TEXT PRIMARY KEY,
		request_id  TEXT NOT NULL DEFAULT '',
		resume_hash TEXT NOT NULL DEFAULT '',
		expires_at  INTEGER NOT NULL,
		data        TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS transactions_request ON transactions(request_id)`,
	`CREATE INDEX IF NOT EXISTS transactions_resume ON transactions(resume_hash)`,
	`CREATE INDEX IF NOT EXISTS transactions_expiry ON transactions(expires_at)`,
	`CREATE TABLE IF NOT EXISTS nonces (
		key        TEXT PRIMARY KEY,
		expires_at INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS sessions (
		id_hash    TEXT PRIMARY KEY,
		identity   TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		data       TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS sessions_identity ON sessions(identity)`,
	`CREATE TABLE IF NOT EXISTS codes (
		code_hash  TEXT PRIMARY KEY,
		expires_at INTEGER NOT NULL,
		used       INTEGER NOT NULL DEFAULT 0,
		data       TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS access_tokens (
		token_hash TEXT PRIMARY KEY,
		code_hash  TEXT NOT NULL DEFAULT '',
		identity   TEXT NOT NULL,
		client_id  TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		revoked    INTEGER NOT NULL DEFAULT 0,
		data       TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS access_tokens_code ON access_tokens(code_hash)`,
	`CREATE INDEX IF NOT EXISTS access_tokens_grant ON access_tokens(identity, client_id)`,
	`CREATE TABLE IF NOT EXISTS consents (
		identity   TEXT NOT NULL,
		client_id  TEXT NOT NULL,
		updated_at INTEGER NOT NULL,
		data       TEXT NOT NULL,
		PRIMARY KEY (identity, client_id)
	)`,
	`CREATE TABLE IF NOT EXISTS clients (
		client_id  TEXT PRIMARY KEY,
		created_at INTEGER NOT NULL,
		data       TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS client_owners (
		client_id TEXT NOT NULL,
		identity  TEXT NOT NULL,
		PRIMARY KEY (client_id, identity)
	)`,
	`CREATE INDEX IF NOT EXISTS client_owners_identity ON client_owners(identity)`,
	`CREATE TABLE IF NOT EXISTS signing_keys (
		kid        TEXT PRIMARY KEY,
		created_at INTEGER NOT NULL,
		retired_at INTEGER NOT NULL DEFAULT 0,
		data       BLOB NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS secrets (
		name  TEXT PRIMARY KEY,
		value BLOB NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS audit (
		id    INTEGER PRIMARY KEY AUTOINCREMENT,
		at    INTEGER NOT NULL,
		event TEXT NOT NULL,
		data  TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS audit_at ON audit(at)`,
	`CREATE TABLE IF NOT EXISTS signins (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		identity  TEXT NOT NULL,
		at        INTEGER NOT NULL,
		data      TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS signins_identity ON signins(identity, at)`,
}

func (s *Store) migrate(ctx context.Context) error {
	for _, stmt := range migrations {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

func unix(t time.Time) int64 { return t.UTC().Unix() }

// --- Nonces ------------------------------------------------------------------

// Use implements signin.NonceCache atomically: the primary key is the guard,
// so two concurrent presentations of one approval cannot both succeed.
func (s *Store) Use(ctx context.Context, key string, expiresAt time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO nonces(key, expires_at) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`,
		key, unix(expiresAt))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// --- Transactions ------------------------------------------------------------

// CreateTxn inserts a new transaction.
func (s *Store) CreateTxn(ctx context.Context, t *Txn) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO transactions(id, request_id, resume_hash, expires_at, data) VALUES(?, ?, ?, ?, ?)`,
		t.ID, t.RequestID, t.ResumeHash, unix(t.ExpiresAt), string(raw))
	return err
}

// GetTxn loads a live transaction by id.
func (s *Store) GetTxn(ctx context.Context, id string) (*Txn, error) {
	return s.txnWhere(ctx, `id = ?`, id)
}

// TxnIDByRequest finds the transaction a native approval answers.
func (s *Store) TxnIDByRequest(ctx context.Context, requestID string) (string, error) {
	if requestID == "" {
		return "", ErrNotFound
	}
	t, err := s.txnWhere(ctx, `request_id = ?`, requestID)
	if err != nil {
		return "", err
	}
	return t.ID, nil
}

// TxnIDByResume finds the transaction a resume code belongs to.
func (s *Store) TxnIDByResume(ctx context.Context, resumeHash string) (string, error) {
	if resumeHash == "" {
		return "", ErrNotFound
	}
	t, err := s.txnWhere(ctx, `resume_hash = ?`, resumeHash)
	if err != nil {
		return "", err
	}
	return t.ID, nil
}

func (s *Store) txnWhere(ctx context.Context, where string, arg any) (*Txn, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM transactions WHERE `+where+` AND expires_at > ?`, arg, unix(s.now())).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var t Txn
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// UpdateTxn applies fn to a live transaction atomically. fn must not call the
// store. Returning an error from fn aborts the update; the error is returned.
func (s *Store) UpdateTxn(ctx context.Context, id string, fn func(*Txn) error) (*Txn, error) {
	return s.updateTxn(ctx, id, fn, nil)
}

// updateTxn is UpdateTxn with also, a further write committed with the
// transaction's own — and only when fn accepted the change.
func (s *Store) updateTxn(ctx context.Context, id string, fn func(*Txn) error, also func(*sql.Tx) error) (*Txn, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRowContext(ctx,
		`SELECT data FROM transactions WHERE id = ? AND expires_at > ?`, id, unix(s.now())).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var t Txn
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return nil, err
	}
	if err := fn(&t); err != nil {
		// A state change recorded alongside a refusal (a failed approval, a
		// spent resume code) must still be saved.
		var keep *keepError
		if !errors.As(err, &keep) {
			return nil, err
		}
		if serr := saveTxn(ctx, tx, &t); serr != nil {
			return nil, serr
		}
		if cerr := tx.Commit(); cerr != nil {
			return nil, cerr
		}
		return &t, keep.err
	}
	if err := saveTxn(ctx, tx, &t); err != nil {
		return nil, err
	}
	if also != nil {
		if err := also(tx); err != nil {
			return nil, err
		}
	}
	return &t, tx.Commit()
}

func saveTxn(ctx context.Context, tx *sql.Tx, t *Txn) error {
	out, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE transactions SET request_id = ?, resume_hash = ?, expires_at = ?, data = ? WHERE id = ?`,
		t.RequestID, t.ResumeHash, unix(t.ExpiresAt), string(out), t.ID)
	return err
}

// keepError marks an error returned from an UpdateTxn callback whose state
// changes must be committed anyway.
type keepError struct{ err error }

func (k *keepError) Error() string { return k.err.Error() }
func (k *keepError) Unwrap() error { return k.err }

// commitAnd wraps err so UpdateTxn saves the transaction before returning it.
func commitAnd(err error) error { return &keepError{err: err} }

// --- Browser sessions ----------------------------------------------------------

// BrowserSession is a browser signed in to the bridge itself.
type BrowserSession struct {
	Identity         string    `json:"identity"`
	AuthTime         time.Time `json:"auth_time"`
	SessionDelegated bool      `json:"session_delegated,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	UserAgent        string    `json:"user_agent,omitempty"`
}

// CreateSession stores a session under the hash of its cookie value.
func (s *Store) CreateSession(ctx context.Context, idHash string, sess BrowserSession) error {
	raw, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO sessions(id_hash, identity, expires_at, data) VALUES(?, ?, ?, ?)`,
		idHash, sess.Identity, unix(sess.ExpiresAt), string(raw))
	return err
}

// GetSession loads a live session.
func (s *Store) GetSession(ctx context.Context, idHash string) (*BrowserSession, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM sessions WHERE id_hash = ? AND expires_at > ?`, idHash, unix(s.now())).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var sess BrowserSession
	if err := json.Unmarshal([]byte(raw), &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

// DeleteSession signs a browser out.
func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash)
	return err
}

// --- Sign-in history -----------------------------------------------------------

// SignInRecord is one completed authentication, for the user's own view.
type SignInRecord struct {
	At               time.Time `json:"at"`
	ClientID         string    `json:"client_id,omitempty"`
	ClientName       string    `json:"client_name,omitempty"`
	SessionDelegated bool      `json:"session_delegated,omitempty"`
	Finish           string    `json:"finish,omitempty"`
	UserAgent        string    `json:"user_agent,omitempty"`
}

// RecordSignIn appends to an identity's sign-in history.
func (s *Store) RecordSignIn(ctx context.Context, identity string, rec SignInRecord) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO signins(identity, at, data) VALUES(?, ?, ?)`, identity, unix(rec.At), string(raw))
	return err
}

// RecentSignIns lists an identity's newest sign-ins.
func (s *Store) RecentSignIns(ctx context.Context, identity string, limit int) ([]SignInRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT data FROM signins WHERE identity = ? ORDER BY at DESC, id DESC LIMIT ?`, identity, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SignInRecord
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var rec SignInRecord
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// --- Audit -------------------------------------------------------------------

// Audit appends a structured event. Callers pass no auth payloads, secrets or
// more identifiers than the event needs.
func (s *Store) Audit(ctx context.Context, event string, fields map[string]any) error {
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO audit(at, event, data) VALUES(?, ?, ?)`, unix(s.now()), event, string(raw))
	return err
}

// AuditEvent is one row of the audit log.
type AuditEvent struct {
	At     time.Time
	Event  string
	Fields map[string]any
}

// AuditSince lists events at or after t, oldest first.
func (s *Store) AuditSince(ctx context.Context, t time.Time) ([]AuditEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT at, event, data FROM audit WHERE at >= ? ORDER BY id`, unix(t))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var at int64
		var ev AuditEvent
		var raw string
		if err := rows.Scan(&at, &ev.Event, &raw); err != nil {
			return nil, err
		}
		ev.At = time.Unix(at, 0).UTC()
		_ = json.Unmarshal([]byte(raw), &ev.Fields)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// --- Retention ---------------------------------------------------------------

// Retention bounds how long the bridge keeps what it does not need.
type Retention struct {
	Audit    time.Duration // default 90 days
	SignIns  time.Duration // default 90 days
	Consents time.Duration // unused consents; default 1 year
}

func (r Retention) withDefaults() Retention {
	if r.Audit == 0 {
		r.Audit = 90 * 24 * time.Hour
	}
	if r.SignIns == 0 {
		r.SignIns = 90 * 24 * time.Hour
	}
	if r.Consents == 0 {
		r.Consents = 365 * 24 * time.Hour
	}
	return r
}

// Prune deletes expired and out-of-retention rows.
func (s *Store) Prune(ctx context.Context, r Retention) error {
	r = r.withDefaults()
	now := s.now()
	stmts := []struct {
		q   string
		arg int64
	}{
		{`DELETE FROM transactions WHERE expires_at <= ?`, unix(now)},
		{`DELETE FROM nonces WHERE expires_at <= ?`, unix(now)},
		{`DELETE FROM sessions WHERE expires_at <= ?`, unix(now)},
		{`DELETE FROM codes WHERE expires_at <= ?`, unix(now.Add(-time.Hour))},
		{`DELETE FROM access_tokens WHERE expires_at <= ?`, unix(now.Add(-time.Hour))},
		{`DELETE FROM audit WHERE at <= ?`, unix(now.Add(-r.Audit))},
		{`DELETE FROM signins WHERE at <= ?`, unix(now.Add(-r.SignIns))},
		{`DELETE FROM consents WHERE updated_at <= ?`, unix(now.Add(-r.Consents))},
	}
	for _, st := range stmts {
		if _, err := s.db.ExecContext(ctx, st.q, st.arg); err != nil {
			return fmt.Errorf("prune %s: %w", strings.Fields(st.q)[2], err)
		}
	}
	return nil
}

// --- Secrets -----------------------------------------------------------------

// getOrCreateSecret returns the named secret, creating it with gen on first
// use. Used for the pairwise-subject secret, which must never change.
func (s *Store) getOrCreateSecret(ctx context.Context, name string, gen func() ([]byte, error)) ([]byte, error) {
	var v []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM secrets WHERE name = ?`, name).Scan(&v)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	v, err = gen()
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO secrets(name, value) VALUES(?, ?) ON CONFLICT(name) DO NOTHING`, name, v); err != nil {
		return nil, err
	}
	// Re-read: a concurrent creator may have won.
	err = s.db.QueryRowContext(ctx, `SELECT value FROM secrets WHERE name = ?`, name).Scan(&v)
	return v, err
}
