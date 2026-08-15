// Package auth is the host login: accounts and sessions for whoever's
// allowed to run the shared screen. Passwords are bcrypt-hashed; accounts
// persist to a flat JSON file (no database, same as the rest of the app),
// while sessions are memory-only and meant to last indefinitely once
// issued - good until SignOut or a server restart.
package auth

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"

	"imposter/internal/token"
)

var (
	ErrBadEmail       = errors.New("enter a valid email")
	ErrWeakPassword   = errors.New("password must be at least 8 characters")
	ErrEmailTaken     = errors.New("an account with that email already exists")
	ErrBadCredentials = errors.New("wrong email or password")
	ErrSignupsClosed  = errors.New("sign in as an existing host to add another account")
)

// Account is one host login. Passwords never leave this file in the clear -
// only the bcrypt hash is ever stored or compared.
type Account struct {
	Email        string `json:"email"`
	PasswordHash string `json:"passwordHash"`
}

// Store is every host account plus every signed-in session, backed by a
// flat JSON file - consistent with the rest of the app (topics.csv, no
// database). Sessions are intentionally not persisted: they're meant to
// last indefinitely once issued, but a server restart is a fine place to
// draw that line, same as everything else in Room resetting on restart.
type Store struct {
	mu       sync.Mutex
	path     string
	accounts map[string]*Account // key: lowercased email
	sessions map[string]string   // token -> lowercased email
}

func Load(path string) (*Store, error) {
	a := &Store{
		path:     path,
		accounts: map[string]*Account{},
		sessions: map[string]string{},
	}

	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil // no accounts yet - the first signup creates the file
	}
	if err != nil {
		return nil, err
	}

	var list []*Account
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	for _, acct := range list {
		a.accounts[acct.Email] = acct
	}
	return a, nil
}

func (a *Store) saveLocked() error {
	list := make([]*Account, 0, len(a.accounts))
	for _, acct := range a.accounts {
		list = append(list, acct)
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}

// Empty reports whether any account exists yet - true only for a fresh
// deployment that hasn't been through its first sign-up.
func (a *Store) Empty() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.accounts) == 0
}

// SignUp registers a new host account. Once at least one account exists,
// callers are expected to gate this behind an existing signed-in session
// themselves (see internal/server's handleSignUp) - Store doesn't know
// about cookies, so it can't enforce that part on its own.
func (a *Store) SignUp(email, password string) error {
	email = normalizeEmail(email)
	if !looksLikeEmail(email) {
		return ErrBadEmail
	}
	if len(password) < 8 {
		return ErrWeakPassword
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if _, exists := a.accounts[email]; exists {
		return ErrEmailTaken
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	a.accounts[email] = &Account{Email: email, PasswordHash: string(hash)}
	return a.saveLocked()
}

// SignIn checks the password and, on success, mints a session token - 32
// random hex characters (same generator as player and room IDs), not a
// UUID. The cookie it goes into is set to last a very long time; there's no
// server-side expiry check here, so a token is good until SignOut deletes
// it or the server restarts.
func (a *Store) SignIn(email, password string) (string, error) {
	email = normalizeEmail(email)

	a.mu.Lock()
	defer a.mu.Unlock()

	acct, ok := a.accounts[email]
	if !ok {
		// Hash something anyway so a valid vs. invalid email can't be timed apart.
		bcrypt.CompareHashAndPassword([]byte("$2a$10$"+strings.Repeat("a", 53)), []byte(password))
		return "", ErrBadCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(acct.PasswordHash), []byte(password)); err != nil {
		return "", ErrBadCredentials
	}

	tok := token.New()
	a.sessions[tok] = email
	return tok, nil
}

// SignOut invalidates a session token. A missing or already-invalid token
// is a no-op, same as leaving a room you were never in.
func (a *Store) SignOut(tok string) {
	if tok == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, tok)
}

// IsSignedIn reports whether tok is a live session.
func (a *Store) IsSignedIn(tok string) bool {
	_, ok := a.Email(tok)
	return ok
}

// Email resolves a session token to the account email that owns it - used
// at claim time to decide which room an account owns, without needing a
// separate room-scoped cookie for the mapping.
func (a *Store) Email(tok string) (string, bool) {
	if tok == "" {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	email, ok := a.sessions[tok]
	return email, ok
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// looksLikeEmail is a light sanity check, not a validator - the only thing
// that actually matters is that sign-in later uses the same normalization.
func looksLikeEmail(email string) bool {
	at := strings.IndexByte(email, '@')
	return at > 0 && at < len(email)-1 && !strings.ContainsAny(email, " \t")
}
