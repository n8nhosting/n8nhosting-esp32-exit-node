// store.go - the proxy user store (SQLite users table) with month-based quota
// accounting. Every call reads and writes the database; there is no cache.
package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const pbkdf2Iter = 100_000

// credKey is the AES-256 key for reversible credential storage. From CRED_KEY
// (base64, 32 bytes) if set, else derived from ADMIN_TOKEN. Credentials are
// stored encrypted so the dashboard can show "user:pass@host:port" to hand to
// customers, without keeping plaintext at rest.
var credKey [32]byte

func initCredKey(credKeyB64, adminToken string) {
	if credKeyB64 != "" {
		if b, err := base64.StdEncoding.DecodeString(credKeyB64); err == nil && len(b) == 32 {
			copy(credKey[:], b)
			return
		}
	}
	credKey = sha256.Sum256([]byte("cred:" + adminToken))
}

func encryptCred(pw string) string {
	block, err := aes.NewCipher(credKey[:])
	if err != nil {
		return ""
	}
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	rand.Read(nonce)
	ct := gcm.Seal(nonce, nonce, []byte(pw), nil)
	return base64.StdEncoding.EncodeToString(ct)
}

func decryptCred(enc string) string {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return ""
	}
	block, err := aes.NewCipher(credKey[:])
	if err != nil {
		return ""
	}
	gcm, _ := cipher.NewGCM(block)
	if len(raw) < gcm.NonceSize() {
		return ""
	}
	pt, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return ""
	}
	return string(pt)
}

// hashPassword returns "pbkdf2$<iter>$<salt_b64>$<hash_b64>".
func hashPassword(pw string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	dk, _ := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iter, 32)
	return "pbkdf2$" + strconv.Itoa(pbkdf2Iter) + "$" +
		base64.RawStdEncoding.EncodeToString(salt) + "$" +
		base64.RawStdEncoding.EncodeToString(dk)
}

// verifyPassword accepts a stored hash (or, for legacy records, a plaintext
// password) and reports whether pw matches.
func verifyPassword(stored, pw string) bool {
	if !strings.HasPrefix(stored, "pbkdf2$") {
		return subtle.ConstantTimeCompare([]byte(stored), []byte(pw)) == 1 // legacy plaintext
	}
	parts := strings.Split(stored, "$")
	if len(parts) != 4 {
		return false
	}
	iter, _ := strconv.Atoi(parts[1])
	salt, _ := base64.RawStdEncoding.DecodeString(parts[2])
	want, _ := base64.RawStdEncoding.DecodeString(parts[3])
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

type User struct {
	Username string `json:"username"`
	PassHash string `json:"pass_hash"` // pbkdf2 hash (never returned by the API)
	// LegacyPassword reads v1.0 records that stored a plaintext "password";
	// it is migrated into PassHash on load and never written back.
	LegacyPassword string  `json:"password,omitempty"`
	Enabled        bool    `json:"enabled"`
	ExpiresAt      string  `json:"expires_at,omitempty"` // RFC3339; empty = never
	RateMbps       float64 `json:"rate_mbps"`            // 0 = unlimited
	QuotaGB        float64 `json:"quota_gb"`             // 0 = unlimited (per calendar month)
	// usage (reset each calendar month)
	UsageBytes int64  `json:"usage_bytes"`
	UsageMonth string `json:"usage_month"` // "2026-10"
	CreatedAt  string `json:"created_at"`
	Note       string `json:"note,omitempty"`
	DeviceMAC  string `json:"device_mac,omitempty"` // bound exit device (informational)
	CredEnc    string `json:"cred_enc,omitempty"`   // AES-GCM(password) for display; never returned by the API
}

// PublicUser is what the API returns — never the password hash.
type PublicUser struct {
	Username   string  `json:"username"`
	Enabled    bool    `json:"enabled"`
	ExpiresAt  string  `json:"expires_at,omitempty"`
	RateMbps   float64 `json:"rate_mbps"`
	QuotaGB    float64 `json:"quota_gb"`
	UsageBytes int64   `json:"usage_bytes"`
	UsageGB    float64 `json:"usage_gb"`
	UsageMonth string  `json:"usage_month"`
	CreatedAt  string  `json:"created_at"`
	Note       string  `json:"note,omitempty"`
	DeviceMAC  string  `json:"device_mac,omitempty"`
}

func (u *User) Public() PublicUser {
	return PublicUser{
		Username: u.Username, Enabled: u.Enabled, ExpiresAt: u.ExpiresAt,
		RateMbps: u.RateMbps, QuotaGB: u.QuotaGB, UsageBytes: u.UsageBytes,
		UsageGB:    float64(u.UsageBytes) / (1024 * 1024 * 1024),
		UsageMonth: u.UsageMonth, CreatedAt: u.CreatedAt, Note: u.Note, DeviceMAC: u.DeviceMAC,
	}
}

type Store struct {
	db *sql.DB
}

var ErrNotFound = errors.New("user not found")
var ErrExists = errors.New("user already exists")

// NewStore opens the store. A path ending in .json names the legacy users
// file: the database is platform.db beside it, and the file is imported once
// (importLegacyJSON). Any other path is the database itself.
func NewStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dbPath, legacy := path, ""
	if strings.HasSuffix(path, ".json") {
		dbPath, legacy = filepath.Join(filepath.Dir(path), "platform.db"), path
	}
	db, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}
	if legacy != "" {
		if err := importLegacyJSON(db, legacy); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

// DB is the store's database, shared with the other platform tables.
func (s *Store) DB() *sql.DB { return s.db }

// userCols and userArgs list the users columns in one fixed order.
const userCols = `username, pass_hash, cred_enc, enabled, expires_at, rate_mbps,
	quota_gb, usage_bytes, usage_month, created_at, note, device_mac`

func userArgs(u *User) []any {
	return []any{u.Username, u.PassHash, u.CredEnc, u.Enabled, u.ExpiresAt, u.RateMbps,
		u.QuotaGB, u.UsageBytes, u.UsageMonth, u.CreatedAt, u.Note, u.DeviceMAC}
}

const (
	selectUser = `SELECT ` + userCols + ` FROM users WHERE username = ?`
	insertUser = `INSERT INTO users (` + userCols + `) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (username) DO NOTHING`
	// updateUser takes userArgs minus the username, then the username.
	updateUser = `UPDATE users SET pass_hash = ?, cred_enc = ?, enabled = ?, expires_at = ?,
		rate_mbps = ?, quota_gb = ?, usage_bytes = ?, usage_month = ?, created_at = ?,
		note = ?, device_mac = ? WHERE username = ?`
)

// scanUser reads one userCols row; no row is ErrNotFound.
func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	err := row.Scan(&u.Username, &u.PassHash, &u.CredEnc, &u.Enabled, &u.ExpiresAt, &u.RateMbps,
		&u.QuotaGB, &u.UsageBytes, &u.UsageMonth, &u.CreatedAt, &u.Note, &u.DeviceMAC)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func monthKey(t time.Time) string { return t.UTC().Format("2006-01") }

// rollMonth zeroes usage if we have crossed into a new calendar month.
func (u *User) rollMonth(now time.Time) {
	mk := monthKey(now)
	if u.UsageMonth != mk {
		u.UsageMonth = mk
		u.UsageBytes = 0
	}
}

func (s *Store) List() []*User {
	out := []*User{}
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY username`)
	if err != nil {
		log.Printf("store: list users: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			log.Printf("store: list users: %v", err)
			return out
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		log.Printf("store: list users: %v", err)
	}
	return out
}

func (s *Store) Get(username string) (*User, error) {
	return scanUser(s.db.QueryRow(selectUser, username))
}

func (s *Store) Create(u *User) error {
	if u.CreatedAt == "" {
		u.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	u.UsageMonth = monthKey(time.Now())
	res, err := s.db.Exec(insertUser, userArgs(u)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrExists
	}
	return nil
}

// Update applies a mutator to the stored user and persists it, all inside one
// write transaction so concurrent updates never lose each other's changes.
// fn must not call back into the store.
func (s *Store) Update(username string, fn func(*User) error) error {
	ctx := context.Background()
	return inTx(ctx, s.db, func(c *sql.Conn) error {
		u, err := scanUser(c.QueryRowContext(ctx, selectUser, username))
		if err != nil {
			return err
		}
		if err := fn(u); err != nil {
			return err
		}
		_, err = c.ExecContext(ctx, updateUser, append(userArgs(u)[1:], username)...)
		return err
	})
}

func (s *Store) Delete(username string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE username = ?`, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddUsage records additional bytes for a user, rolling the month if needed.
// Unknown users are ignored.
func (s *Store) AddUsage(username string, bytes int64, now time.Time) {
	err := s.Update(username, func(u *User) error {
		u.rollMonth(now)
		u.UsageBytes += bytes
		return nil
	})
	if err != nil && err != ErrNotFound {
		log.Printf("store: add usage user=%q: %v", username, err)
	}
}

// Authorize reports whether the user may open a new connection now, and why not.
func (s *Store) Authorize(username, password string, now time.Time) (bool, string) {
	u, err := s.Get(username)
	if err == ErrNotFound {
		return false, "unknown user"
	}
	if err != nil {
		return false, "store: " + err.Error()
	}
	if !verifyPassword(u.PassHash, password) {
		return false, "bad password"
	}
	if !u.Enabled {
		return false, "disabled"
	}
	if u.ExpiresAt != "" {
		if exp, err := time.Parse(time.RFC3339, u.ExpiresAt); err == nil && now.After(exp) {
			return false, "expired"
		}
	}
	month := u.UsageMonth
	u.rollMonth(now)
	if u.UsageMonth != month {
		// persist the new month's reset, as the old in-memory store did
		_ = s.Update(username, func(x *User) error { x.rollMonth(now); return nil })
	}
	if u.QuotaGB > 0 {
		quotaBytes := int64(u.QuotaGB * 1024 * 1024 * 1024)
		if u.UsageBytes >= quotaBytes {
			return false, "quota exceeded"
		}
	}
	return true, ""
}

// RateBytesPerSec returns the per-user rate cap in bytes/sec (0 = unlimited).
func (s *Store) RateBytesPerSec(username string) int64 {
	var mbps float64
	err := s.db.QueryRow(`SELECT rate_mbps FROM users WHERE username = ?`, username).Scan(&mbps)
	if err != nil || mbps <= 0 {
		return 0
	}
	return int64(mbps * 1_000_000 / 8)
}

func randToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
