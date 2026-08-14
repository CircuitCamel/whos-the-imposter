package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"
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

// AuthStore is every host account plus every signed-in session, backed by a
// flat JSON file - consistent with the rest of the app (topics.csv, no
// database). Sessions are intentionally not persisted: they're meant to
// last indefinitely once issued, but a server restart is a fine place to
// draw that line, same as everything else in Room resetting on restart.
type AuthStore struct {
	mu       sync.Mutex
	path     string
	accounts map[string]*Account // key: lowercased email
	sessions map[string]string   // token -> lowercased email
}

func loadAuthStore(path string) (*AuthStore, error) {
	a := &AuthStore{
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

func (a *AuthStore) saveLocked() error {
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
func (a *AuthStore) Empty() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.accounts) == 0
}

// SignUp registers a new host account. Once at least one account exists,
// callers are expected to gate this behind an existing signed-in session
// themselves (see main.go's handleSignUp) - AuthStore doesn't know about
// cookies, so it can't enforce that part on its own.
func (a *AuthStore) SignUp(email, password string) error {
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
func (a *AuthStore) SignIn(email, password string) (string, error) {
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

	token := newID()
	a.sessions[token] = email
	return token, nil
}

// SignOut invalidates a session token. A missing or already-invalid token
// is a no-op, same as leaving a room you were never in.
func (a *AuthStore) SignOut(token string) {
	if token == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, token)
}

// IsSignedIn reports whether token is a live session.
func (a *AuthStore) IsSignedIn(token string) bool {
	if token == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.sessions[token]
	return ok
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
