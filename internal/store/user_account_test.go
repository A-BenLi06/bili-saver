package store

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOwnAccountRenameKeepsIdentityAndOwnership(t *testing.T) {
	s := openTest(t)
	u := personalUser(t, s, "account-alice", 3)
	other := personalUser(t, s, "account-bob", 3)
	r := personalRequest(t, s, u.ID, "BVAccount")
	id, err := s.UpsertSource(&Source{Type: "video", ExtID: "BVAccount", UserID: u.ID})
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.CreateSession(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	name := " 爱丽丝 "
	got, err := s.UpdateOwnAccount(u.ID, &name, nil)
	if err != nil || got.ID != u.ID || got.Username != "爱丽丝" || got.Role != u.Role || got.PassHash != u.PassHash {
		t.Fatal(got, err)
	}
	if fresh, err := s.UserBySession(session); err != nil || fresh.Username != "爱丽丝" {
		t.Fatal(fresh, err)
	}
	if src, err := s.GetSource(id); err != nil || src.UserID != u.ID {
		t.Fatal(src, err)
	}
	if req, err := s.UserDownload(u.ID, "BVAccount"); err != nil || req.ID != r.ID {
		t.Fatal(req, err)
	}
	name = other.Username
	if _, err = s.UpdateOwnAccount(u.ID, &name, nil); !errors.Is(err, ErrUsernameTaken) {
		t.Fatal(err)
	}
}

func TestOwnPasswordAndAdminResetRevokeAllSessions(t *testing.T) {
	s := openTest(t)
	u := personalUser(t, s, "password-user", 2)
	a, err := s.CreateSession(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateSession(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ChangeOwnPassword(u.ID, "incorrect", "replacement-password"); err == nil {
		t.Fatal("wrong old password accepted")
	}
	if _, err = s.UserBySession(a); err != nil {
		t.Fatal("failed change revoked session", err)
	}
	if err = s.ChangeOwnPassword(u.ID, "password", "replacement-password"); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{a, b} {
		if _, err = s.UserBySession(token); err == nil {
			t.Fatal("old session survived")
		}
	}
	got, err := s.GetUser(u.ID)
	if err != nil || got.PassHash != HashPassword("replacement-password", got.Salt) || got.ID != u.ID {
		t.Fatal(got, err)
	}
	c, err := s.CreateSession(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	reset := "administrator-reset"
	if err = s.UpdateUser(u.ID, &reset, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UserBySession(c); err == nil {
		t.Fatal("admin reset left old session active")
	}
}

func TestConcurrentProfileFieldsDoNotOverwriteEachOther(t *testing.T) {
	s := openTest(t)
	u := personalUser(t, s, "concurrent-profile", 1)
	name := "新用户名"
	avatar := "media/user-avatars/test.png"
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); _, err := s.UpdateOwnAccount(u.ID, &name, nil); errs <- err }()
	go func() { defer wg.Done(); _, err := s.UpdateOwnAccount(u.ID, nil, &avatar); errs <- err }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetUser(u.ID)
	if err != nil || got.Username != name || got.AvatarPath != avatar || got.AvatarURL != "/media/"+avatar {
		t.Fatal(got, err)
	}
}

func TestAccountMigrationFromOldUsersTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-account.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser("legacy-account", "password", "user", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`ALTER TABLE users DROP COLUMN avatar_path`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for i := 0; i < 2; i++ {
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.GetUser(u.ID)
		if err != nil || got.Username != u.Username || got.AvatarURL != "" {
			t.Fatal(got, err)
		}
		s.Close()
	}
}

func TestAccountValidation(t *testing.T) {
	for _, name := range []string{"", "a", strings.Repeat("名", 33), "a\nb"} {
		if _, err := ValidateUsername(name); err == nil {
			t.Fatal("accepted", name)
		}
	}
	for _, pw := range []string{"short", strings.Repeat("x", 129)} {
		if err := ValidateNewPassword(pw); err == nil {
			t.Fatal("accepted password length", len(pw))
		}
	}
}
