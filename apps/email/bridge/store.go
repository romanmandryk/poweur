package bridge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the bridge's own small state. It never holds message content:
// only hashes, counters, ids and the envelopes waiting to be processed (which
// are still encrypted to the bridge).
//
// Addresses are stored as HMACs keyed by a secret derived from the bridge's
// seed, so the database is not an address book: it can answer "has U written
// to X before?" and "is X suppressed?" without listing anyone.
type Store struct {
	db  *sql.DB
	key []byte
	now func() time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS inbound_seen (k TEXT PRIMARY KEY, at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS counters (k TEXT PRIMARY KEY, n INTEGER NOT NULL, day TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sends (
	sender TEXT NOT NULL, send_id TEXT NOT NULL, state TEXT NOT NULL,
	message_id TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL, updated INTEGER NOT NULL,
	PRIMARY KEY (sender, send_id));
CREATE INDEX IF NOT EXISTS sends_message_id ON sends (message_id);
CREATE TABLE IF NOT EXISTS ledger (k TEXT PRIMARY KEY, at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS suppressed (k TEXT PRIMARY KEY, reason TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS queue (id TEXT PRIMARY KEY, envelope TEXT NOT NULL, received INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, next INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS files (id TEXT PRIMARY KEY, kind TEXT NOT NULL, owner TEXT NOT NULL, size INTEGER NOT NULL, expires INTEGER NOT NULL);
`

// OpenStore opens (or creates) the database at path.
func OpenStore(path string, ledgerKey []byte) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init store: %w", err)
	}
	return &Store{db: db, key: ledgerKey, now: time.Now}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// hash keys an address or pair so the table holds no plaintext.
func (s *Store) hash(parts ...string) string {
	m := hmac.New(sha256.New, s.key)
	for _, p := range parts {
		m.Write([]byte(strings.ToLower(strings.TrimSpace(p))))
		m.Write([]byte{0})
	}
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Store) day() string { return s.now().UTC().Format("2006-01-02") }

// Get and Set are a small key/value store (the inbox cursor).
func (s *Store) Get(ctx context.Context, k string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT v FROM kv WHERE k = ?`, k).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) Set(ctx context.Context, k, v string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO kv (k, v) VALUES (?, ?) ON CONFLICT (k) DO UPDATE SET v = excluded.v`, k, v)
	return err
}

// SeenInbound reports whether a (message, recipient) was already delivered,
// so a mail server's retry after a partial failure delivers nothing twice.
func (s *Store) SeenInbound(ctx context.Context, key string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbound_seen WHERE k = ?`, s.hash("in", key)).Scan(&n)
	return n > 0, err
}

// MarkInbound records a delivery.
func (s *Store) MarkInbound(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO inbound_seen (k, at) VALUES (?, ?)`, s.hash("in", key), s.now().Unix())
	return err
}

// Count increments a daily counter and returns its new value; Peek reads it.
func (s *Store) Count(ctx context.Context, name string, by int) (int, error) {
	k := s.hash("count", name)
	day := s.day()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var n int
	var d string
	err = tx.QueryRowContext(ctx, `SELECT n, day FROM counters WHERE k = ?`, k).Scan(&n, &d)
	if errors.Is(err, sql.ErrNoRows) || d != day {
		n = 0
	} else if err != nil {
		return 0, err
	}
	n += by
	if _, err := tx.ExecContext(ctx, `INSERT INTO counters (k, n, day) VALUES (?, ?, ?) ON CONFLICT (k) DO UPDATE SET n = excluded.n, day = excluded.day`, k, n, day); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

func (s *Store) Peek(ctx context.Context, name string) (int, error) {
	var n int
	var d string
	err := s.db.QueryRowContext(ctx, `SELECT n, day FROM counters WHERE k = ?`, s.hash("count", name)).Scan(&n, &d)
	if errors.Is(err, sql.ErrNoRows) || d != s.day() {
		return 0, nil
	}
	return n, err
}

// Correspondents: whether identity and address have exchanged mail before
// (either direction). Used for the new-recipient quota (replies to someone
// who wrote first are not new) and the once-per-recipient footer.
func (s *Store) Known(ctx context.Context, identityName, address string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ledger WHERE k = ?`, s.hash("pair", identityName, address)).Scan(&n)
	return n > 0, err
}

func (s *Store) Remember(ctx context.Context, identityName, address string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO ledger (k, at) VALUES (?, ?)`, s.hash("pair", identityName, address), s.now().Unix())
	return err
}

// FooterSent reports and records the once-per-recipient growth footer.
func (s *Store) FooterSent(ctx context.Context, identityName, address string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO ledger (k, at) VALUES (?, ?)`, s.hash("footer", identityName, address), s.now().Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 0, nil
}

// Suppress stops sends to an address (hard bounce, complaint).
func (s *Store) Suppress(ctx context.Context, address, reason string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO suppressed (k, reason, at) VALUES (?, ?, ?) ON CONFLICT (k) DO UPDATE SET reason = excluded.reason, at = excluded.at`,
		s.hash("addr", address), reason, s.now().Unix())
	return err
}

func (s *Store) Suppressed(ctx context.Context, address string) (string, bool, error) {
	var reason string
	err := s.db.QueryRowContext(ctx, `SELECT reason FROM suppressed WHERE k = ?`, s.hash("addr", address)).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return reason, err == nil, err
}

// Send records. A send id is the client's idempotency key: the second
// `email.send` with one already seen is answered with the state on record.
type SendRecord struct {
	Sender, SendID, State, MessageID string
}

// ClaimSend records a new send, or returns the existing one (fresh=false).
func (s *Store) ClaimSend(ctx context.Context, sender, sendID string) (SendRecord, bool, error) {
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO sends (sender, send_id, state, created, updated) VALUES (?, ?, 'accepted', ?, ?)`,
		strings.ToLower(sender), sendID, now, now)
	if err != nil {
		return SendRecord{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return SendRecord{Sender: sender, SendID: sendID, State: "accepted"}, true, nil
	}
	var r SendRecord
	err = s.db.QueryRowContext(ctx, `SELECT sender, send_id, state, message_id FROM sends WHERE sender = ? AND send_id = ?`,
		strings.ToLower(sender), sendID).Scan(&r.Sender, &r.SendID, &r.State, &r.MessageID)
	return r, false, err
}

// UpdateSend sets a send's state (and Message-ID when known).
func (s *Store) UpdateSend(ctx context.Context, sender, sendID, state, messageID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sends SET state = ?, message_id = CASE WHEN ? = '' THEN message_id ELSE ? END, updated = ? WHERE sender = ? AND send_id = ?`,
		state, messageID, messageID, s.now().Unix(), strings.ToLower(sender), sendID)
	return err
}

// SendByMessageID finds the send a delivery report is about.
func (s *Store) SendByMessageID(ctx context.Context, messageID string) (SendRecord, bool, error) {
	var r SendRecord
	err := s.db.QueryRowContext(ctx, `SELECT sender, send_id, state, message_id FROM sends WHERE message_id = ?`, messageID).
		Scan(&r.Sender, &r.SendID, &r.State, &r.MessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return SendRecord{}, false, nil
	}
	return r, err == nil, err
}

// Queue holds envelopes picked up from the bridge's inbox until processed,
// so consuming them from the relay can happen before the work is done.
func (s *Store) Enqueue(ctx context.Context, id, envelope string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO queue (id, envelope, received, next) VALUES (?, ?, ?, 0)`, id, envelope, s.now().Unix())
	return err
}

type QueueItem struct {
	ID       string
	Envelope string
	Attempts int
	Received time.Time
}

// Due returns queued items whose next attempt has come.
func (s *Store) Due(ctx context.Context, limit int) ([]QueueItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, envelope, attempts, received FROM queue WHERE next <= ? ORDER BY received LIMIT ?`, s.now().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueItem
	for rows.Next() {
		var it QueueItem
		var received int64
		if err := rows.Scan(&it.ID, &it.Envelope, &it.Attempts, &received); err != nil {
			return nil, err
		}
		it.Received = time.Unix(received, 0)
		out = append(out, it)
	}
	return out, rows.Err()
}

// Retry schedules another attempt after delay.
func (s *Store) Retry(ctx context.Context, id string, delay time.Duration) error {
	_, err := s.db.ExecContext(ctx, `UPDATE queue SET attempts = attempts + 1, next = ? WHERE id = ?`, s.now().Add(delay).Unix(), id)
	return err
}

// Dequeue removes a processed item.
func (s *Store) Dequeue(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM queue WHERE id = ?`, id)
	return err
}

// File bookkeeping for blobs and uploads (the bytes live on disk).
func (s *Store) AddFile(ctx context.Context, id, kind, owner string, size int64, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO files (id, kind, owner, size, expires) VALUES (?, ?, ?, ?, ?)`, id, kind, strings.ToLower(owner), size, expires.Unix())
	return err
}

type FileRecord struct {
	ID, Kind, Owner string
	Size            int64
	Expires         time.Time
}

func (s *Store) File(ctx context.Context, id string) (FileRecord, bool, error) {
	var f FileRecord
	var exp int64
	err := s.db.QueryRowContext(ctx, `SELECT id, kind, owner, size, expires FROM files WHERE id = ?`, id).Scan(&f.ID, &f.Kind, &f.Owner, &f.Size, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return FileRecord{}, false, nil
	}
	f.Expires = time.Unix(exp, 0)
	return f, err == nil, err
}

// UploadBytes is how much an identity has uploaded and not yet sent.
func (s *Store) UploadBytes(ctx context.Context, owner string) (int64, error) {
	var n sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT SUM(size) FROM files WHERE kind = 'upload' AND owner = ? AND expires > ?`, strings.ToLower(owner), s.now().Unix()).Scan(&n)
	return n.Int64, err
}

func (s *Store) DeleteFile(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, id)
	return err
}

// Expired lists files past their expiry.
func (s *Store) Expired(ctx context.Context, limit int) ([]FileRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, owner, size, expires FROM files WHERE expires <= ? LIMIT ?`, s.now().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRecord
	for rows.Next() {
		var f FileRecord
		var exp int64
		if err := rows.Scan(&f.ID, &f.Kind, &f.Owner, &f.Size, &exp); err != nil {
			return nil, err
		}
		f.Expires = time.Unix(exp, 0)
		out = append(out, f)
	}
	return out, rows.Err()
}

// Prune forgets dedupe keys older than a week: a mail server stops retrying
// long before that.
func (s *Store) Prune(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM inbound_seen WHERE at < ?`, s.now().Add(-7*24*time.Hour).Unix())
	return err
}
