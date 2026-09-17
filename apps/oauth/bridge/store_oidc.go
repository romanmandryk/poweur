package bridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// --- Authorization codes ---------------------------------------------------------

// CodeGrant is everything a code redeems into. The code itself is stored
// only as a hash.
type CodeGrant struct {
	ClientID      string    `json:"client_id"`
	RedirectURI   string    `json:"redirect_uri"`
	CodeChallenge string    `json:"code_challenge"`
	Nonce         string    `json:"nonce,omitempty"`
	Identity      string    `json:"identity"`
	Scopes        []string  `json:"scopes"`
	Claims        Claims    `json:"claims"`
	AuthTime      time.Time `json:"auth_time"`
	AMR           []string  `json:"amr"`
	IssuedAt      time.Time `json:"issued_at"`
	Surface       string    `json:"surface"`
}

// Claims are the released identity claims, frozen at consent time.
type Claims struct {
	PoweurID       string `json:"poweur_id,omitempty"`
	KeyFingerprint string `json:"poweur_key_fingerprint,omitempty"`
	PoweurIDURL    string `json:"poweur_id_url,omitempty"`
	Name           string `json:"name,omitempty"`
	Picture        string `json:"picture,omitempty"`
	Profile        string `json:"profile,omitempty"`
}

// PutCode stores a new code.
func (s *Store) PutCode(ctx context.Context, codeHash string, g CodeGrant, expiresAt time.Time) error {
	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO codes(code_hash, expires_at, used, data) VALUES(?, ?, 0, ?)`,
		codeHash, unix(expiresAt), string(raw))
	return err
}

var errCodeReused = errors.New("authorization code already used")

// RedeemCode marks a code used and returns its grant. A second redemption
// returns errCodeReused and revokes every token the first one produced
// (RFC 6749 §4.1.2).
func (s *Store) RedeemCode(ctx context.Context, codeHash string) (*CodeGrant, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var raw string
	var used int
	var expires int64
	err = tx.QueryRowContext(ctx,
		`SELECT data, used, expires_at FROM codes WHERE code_hash = ?`, codeHash).Scan(&raw, &used, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if used != 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE access_tokens SET revoked = 1 WHERE code_hash = ?`, codeHash); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, errCodeReused
	}
	if expires <= unix(s.now()) {
		return nil, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE codes SET used = 1 WHERE code_hash = ?`, codeHash); err != nil {
		return nil, err
	}
	var g CodeGrant
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		return nil, err
	}
	return &g, tx.Commit()
}

// --- Access tokens -----------------------------------------------------------------

// AccessGrant is what an access token lets its bearer read at /userinfo.
type AccessGrant struct {
	ClientID string    `json:"client_id"`
	Identity string    `json:"identity"`
	Subject  string    `json:"sub"`
	Scopes   []string  `json:"scopes"`
	Claims   Claims    `json:"claims"`
	IssuedAt time.Time `json:"issued_at"`
}

// PutAccessToken stores a token hash.
func (s *Store) PutAccessToken(ctx context.Context, tokenHash, codeHash string, g AccessGrant, expiresAt time.Time) error {
	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO access_tokens(token_hash, code_hash, identity, client_id, expires_at, revoked, data)
		 VALUES(?, ?, ?, ?, ?, 0, ?)`,
		tokenHash, codeHash, g.Identity, g.ClientID, unix(expiresAt), string(raw))
	return err
}

// GetAccessToken loads a live, unrevoked token.
func (s *Store) GetAccessToken(ctx context.Context, tokenHash string) (*AccessGrant, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM access_tokens WHERE token_hash = ? AND revoked = 0 AND expires_at > ?`,
		tokenHash, unix(s.now())).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var g AccessGrant
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// RevokeAccessToken revokes one token if it belongs to clientID.
func (s *Store) RevokeAccessToken(ctx context.Context, tokenHash, clientID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE access_tokens SET revoked = 1 WHERE token_hash = ? AND client_id = ?`, tokenHash, clientID)
	return err
}

// RevokeGrant revokes every token an identity holds for a client.
func (s *Store) RevokeGrant(ctx context.Context, identity, clientID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE access_tokens SET revoked = 1 WHERE identity = ? AND client_id = ?`, identity, clientID)
	return err
}

// --- Consents -------------------------------------------------------------------

// Consent is an identity's standing decision about one client.
type Consent struct {
	ClientID   string `json:"client_id"`
	ClientName string `json:"client_name"`
	ClientHost string `json:"client_host,omitempty"`
	// Decided lists every optional scope the user was asked about; Granted
	// the ones they allowed. A request within Decided is not asked again.
	Decided  []string  `json:"decided"`
	Granted  []string  `json:"granted"`
	FirstAt  time.Time `json:"first_at"`
	LastUsed time.Time `json:"last_used"`
}

// GetConsent loads a consent.
func (s *Store) GetConsent(ctx context.Context, identity, clientID string) (*Consent, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM consents WHERE identity = ? AND client_id = ?`, identity, clientID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var c Consent
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// PutConsent stores a consent.
func (s *Store) PutConsent(ctx context.Context, identity string, c Consent) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO consents(identity, client_id, updated_at, data) VALUES(?, ?, ?, ?)
		 ON CONFLICT(identity, client_id) DO UPDATE SET updated_at = excluded.updated_at, data = excluded.data`,
		identity, c.ClientID, unix(s.now()), string(raw))
	return err
}

// ListConsents lists an identity's consents, most recently used first.
func (s *Store) ListConsents(ctx context.Context, identity string) ([]Consent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT data FROM consents WHERE identity = ? ORDER BY updated_at DESC`, identity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Consent
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var c Consent
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteConsent forgets a consent.
func (s *Store) DeleteConsent(ctx context.Context, identity, clientID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM consents WHERE identity = ? AND client_id = ?`, identity, clientID)
	return err
}

// --- Registered clients ------------------------------------------------------------

// GetClient loads a registered client.
func (s *Store) GetClient(ctx context.Context, id string) (*Client, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM clients WHERE client_id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var c Client
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, err
	}
	c.Source = ClientRegistered
	return &c, nil
}

// PutClient creates or replaces a registered client and its owner index.
func (s *Store) PutClient(ctx context.Context, c *Client) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO clients(client_id, created_at, data) VALUES(?, ?, ?)
		 ON CONFLICT(client_id) DO UPDATE SET data = excluded.data`,
		c.ID, unix(c.CreatedAt), string(raw)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM client_owners WHERE client_id = ?`, c.ID); err != nil {
		return err
	}
	for _, owner := range c.Owners {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO client_owners(client_id, identity) VALUES(?, ?)`, c.ID, owner); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteClient removes a registered client, its owners, consents and tokens.
func (s *Store) DeleteClient(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM clients WHERE client_id = ?`,
		`DELETE FROM client_owners WHERE client_id = ?`,
		`DELETE FROM consents WHERE client_id = ?`,
		`UPDATE access_tokens SET revoked = 1 WHERE client_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClientsOwnedBy lists the registered clients an identity may manage.
func (s *Store) ClientsOwnedBy(ctx context.Context, identity string) ([]*Client, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.data FROM clients c JOIN client_owners o ON o.client_id = c.client_id
		 WHERE o.identity = ? ORDER BY c.created_at`, identity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Client
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var c Client
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
		c.Source = ClientRegistered
		out = append(out, &c)
	}
	return out, rows.Err()
}

// CountClientsCreatedBy counts clients an identity created, for the limit.
func (s *Store) CountClientsCreatedBy(ctx context.Context, identity string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM clients WHERE json_extract(data, '$.created_by') = ?`, identity).Scan(&n)
	return n, err
}

// CountClientsCreatedSince counts an identity's recent creations, for the
// creation rate limit.
func (s *Store) CountClientsCreatedSince(ctx context.Context, identity string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM clients WHERE json_extract(data, '$.created_by') = ? AND created_at >= ?`,
		identity, unix(since)).Scan(&n)
	return n, err
}

// AllClients lists every registered client (operator CLI).
func (s *Store) AllClients(ctx context.Context) ([]*Client, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM clients ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Client
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var c Client
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
		c.Source = ClientRegistered
		out = append(out, &c)
	}
	return out, rows.Err()
}
