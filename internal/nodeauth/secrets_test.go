package nodeauth

import (
	"bili-saver/internal/config"
	"bili-saver/internal/store"
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapRotatesOnlySeedAndPersists(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "bili-saver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	custom, err := st.CreateUser("friend", "custom-password", "user", 1)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.DataDir = root
	v, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = v.Bootstrap(st, cfg); err != nil {
		t.Fatal(err)
	}
	u, _ := st.GetUserByUsername("admin")
	if u.PassHash == store.HashPassword("bili-saver", u.Salt) {
		t.Fatal("legacy password retained")
	}
	if cfg.View().Teriteri.AdminPass == "" || cfg.View().Teriteri.JWTSecret == "" {
		t.Fatal("missing private playback secrets")
	}
	first := cfg.View().Teriteri.AdminPass
	if err = v.SetPlayback("admin", "changed-password"); err != nil {
		t.Fatal(err)
	}
	next := config.Defaults()
	next.DataDir = root
	reopened, _ := Open(root)
	if err = reopened.Bootstrap(st, next); err != nil {
		t.Fatal(err)
	}
	if next.View().Teriteri.AdminPass != "changed-password" || first == next.View().Teriteri.AdminPass {
		t.Fatal("password not persisted")
	}
	actual, _ := st.GetUser(custom.ID)
	if actual.PassHash != custom.PassHash {
		t.Fatal("custom user modified")
	}
	info, err := os.Stat(filepath.Join(root, "credentials.json"))
	if err != nil || info.Size() == 0 {
		t.Fatal("private recovery file missing")
	}
}
func TestSigningKeyChangesWithPassword(t *testing.T) {
	a := Credentials{User: "admin", Password: "before", Secret: Token()}
	b := a
	b.Password = "after"
	if a.SigningKey() == b.SigningKey() {
		t.Fatal("old tokens remain valid")
	}
}
