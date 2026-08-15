package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	a, err := Load(filepath.Join(t.TempDir(), "accounts.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return a
}

func TestSignUpThenSignIn(t *testing.T) {
	a := newTestStore(t)

	if !a.Empty() {
		t.Fatal("a fresh store should start empty")
	}
	if err := a.SignUp("Host@Example.com", "correct horse"); err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if a.Empty() {
		t.Fatal("store should not be empty after a sign-up")
	}

	// Email matching is case-insensitive.
	tok, err := a.SignIn("host@example.com", "correct horse")
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if len(tok) != 32 {
		t.Fatalf("session token should be 32 characters (16 random bytes, hex), got %d: %q", len(tok), tok)
	}
	if strings.Contains(tok, "-") {
		t.Fatalf("session token should not look like a UUID, got %q", tok)
	}
	if !a.IsSignedIn(tok) {
		t.Fatal("a token just issued by SignIn should be signed in")
	}
}

func TestSignUpRejectsBadInput(t *testing.T) {
	a := newTestStore(t)

	cases := []struct {
		name, email, password string
		wantErr               error
	}{
		{"no @", "not-an-email", "longenough", ErrBadEmail},
		{"empty", "", "longenough", ErrBadEmail},
		{"short password", "a@b.com", "short", ErrWeakPassword},
	}
	for _, c := range cases {
		if err := a.SignUp(c.email, c.password); err != c.wantErr {
			t.Errorf("%s: want %v, got %v", c.name, c.wantErr, err)
		}
	}

	if err := a.SignUp("dup@example.com", "longenough"); err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if err := a.SignUp("dup@example.com", "different-password"); err != ErrEmailTaken {
		t.Fatalf("signing up the same email twice: want ErrEmailTaken, got %v", err)
	}
}

func TestSignInRejectsWrongCredentials(t *testing.T) {
	a := newTestStore(t)
	if err := a.SignUp("host@example.com", "the-real-password"); err != nil {
		t.Fatal(err)
	}

	if _, err := a.SignIn("host@example.com", "wrong-password"); err != ErrBadCredentials {
		t.Errorf("wrong password: want ErrBadCredentials, got %v", err)
	}
	// Deliberately the same error as a wrong password, so a login form can't
	// be used to enumerate which emails have accounts.
	if _, err := a.SignIn("nobody@example.com", "anything-at-all"); err != ErrBadCredentials {
		t.Errorf("unknown email: want ErrBadCredentials, got %v", err)
	}
}

func TestSignOutInvalidatesTheSession(t *testing.T) {
	a := newTestStore(t)
	if err := a.SignUp("host@example.com", "the-real-password"); err != nil {
		t.Fatal(err)
	}
	tok, err := a.SignIn("host@example.com", "the-real-password")
	if err != nil {
		t.Fatal(err)
	}
	a.SignOut(tok)
	if a.IsSignedIn(tok) {
		t.Fatal("a signed-out token should no longer be signed in")
	}
	// Signing out something that was never valid, or already signed out,
	// shouldn't panic or error.
	a.SignOut(tok)
	a.SignOut("")
}

func TestPasswordsAreHashedAtRestNotStoredInTheClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	a, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	const password = "super-secret-password"
	if err := a.SignUp("host@example.com", password); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading accounts file: %v", err)
	}
	if strings.Contains(string(raw), password) {
		t.Fatal("the plaintext password should never be written to disk")
	}
	if !strings.Contains(string(raw), "$2a$") && !strings.Contains(string(raw), "$2b$") {
		t.Fatalf("expected a bcrypt hash in the accounts file, got: %s", raw)
	}
}

func TestAccountsSurviveAReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")

	a, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SignUp("host@example.com", "the-real-password"); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if reloaded.Empty() {
		t.Fatal("the account should still be there after a reload")
	}
	if _, err := reloaded.SignIn("host@example.com", "the-real-password"); err != nil {
		t.Fatalf("signing in after a reload: %v", err)
	}
}
