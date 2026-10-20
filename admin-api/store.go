// store.go - a small, dependency-free JSON-file user store with month-based
// quota accounting. Suitable for the modest user counts of a single node.
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	mu    sync.RWMutex
	path  string
	users map[string]*User
}

var ErrNotFound = errors.New("user not found")
var ErrExists = errors.New("user already exists")

func NewStore(path string) (*Store, error) {
	s := &Store{path: path, users: map[string]*User{}}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err == nil {
		var list []*User
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, err
		}
		migrated := false
		for _, u := range list {
			if u.PassHash == "" && u.LegacyPassword != "" {
				u.PassHash = hashPassword(u.LegacyPassword) // upgrade v1.0 plaintext
				u.LegacyPassword = ""
				migrated = true
			}
			s.users[u.Username] = u
		}
		if migrated {
			_ = s.flush()
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// flush writes the store atomically (temp file + rename).
func (s *Store) flush() error {
	list := make([]*User, 0, len(s.users))
	for _, u := range s.users {
		list = append(list, u)
	}
	data, _ := json.MarshalIndent(list, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*User, 0, len(s.users))
	for _, u := range s.users {
		cp := *u
		out = append(out, &cp)
	}
	return out
}

func (s *Store) Get(username string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[username]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (s *Store) Create(u *User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[u.Username]; ok {
		return ErrExists
	}
	if u.CreatedAt == "" {
		u.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	u.UsageMonth = monthKey(time.Now())
	s.users[u.Username] = u
	return s.flush()
}

// Update applies a mutator under the lock and persists.
func (s *Store) Update(username string, fn func(*User) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[username]
	if !ok {
		return ErrNotFound
	}
	if err := fn(u); err != nil {
		return err
	}
	return s.flush()
}

func (s *Store) Delete(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[username]; !ok {
		return ErrNotFound
	}
	delete(s.users, username)
	return s.flush()
}

// AddUsage records additional bytes for a user, rolling the month if needed.
// Returns the user's post-update usage and quota state.
func (s *Store) AddUsage(username string, bytes int64, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[username]
	if !ok {
		return
	}
	u.rollMonth(now)
	u.UsageBytes += bytes
	// best-effort flush; usage writes are frequent so ignore the error here
	_ = s.flush()
}

// Authorize reports whether the user may open a new connection now, and why not.
func (s *Store) Authorize(username, password string, now time.Time) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[username]
	if !ok {
		return false, "unknown user"
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
	u.rollMonth(now)
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[username]
	if !ok || u.RateMbps <= 0 {
		return 0
	}
	return int64(u.RateMbps * 1_000_000 / 8)
}

func randToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
