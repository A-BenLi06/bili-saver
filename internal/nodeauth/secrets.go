// Package nodeauth manages private installation credentials, outside content catalogs.
package nodeauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

type Credentials struct{ User, Password, Secret string }

func Token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func (c Credentials) SigningKey() string {
	m := hmac.New(sha256.New, []byte(c.Secret))
	m.Write([]byte("playback-v1\x00" + c.User + "\x00" + c.Password))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

type Private struct {
	Version          int    `json:"version"`
	PlaybackOverride bool   `json:"playback_override,omitempty"`
	ConsoleUser      string `json:"console_user,omitempty"`
	ConsolePassword  string `json:"console_bootstrap_password,omitempty"`
	PlaybackUser     string `json:"playback_user"`
	PlaybackPassword string `json:"playback_password"`
	JWTSecret        string `json:"jwt_secret"`
}
type Vault struct {
	mu    sync.Mutex
	path  string
	value Private
}

func Open(root string) (*Vault, error) {
	v := &Vault{path: filepath.Join(root, "credentials.json"), value: Private{Version: 1}}
	body, err := os.ReadFile(v.path)
	if err == nil {
		if err = json.Unmarshal(body, &v.value); err != nil {
			return nil, fmt.Errorf("invalid private credential file: %w", err)
		}
		if v.value.Version != 1 {
			return nil, fmt.Errorf("unsupported private credential version")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return v, nil
}
func (v *Vault) save(value Private) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(v.path), ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(body); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), v.path); err != nil {
		return err
	}
	v.value = value
	return nil
}

// Bootstrap rotates only the recognized legacy seed, preserving custom accounts.
// The private recovery value is synced before the password transaction.
func (v *Vault) Bootstrap(st *store.Store, cfg *config.Config) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	p := v.value
	u, err := st.GetUserByUsername("admin")
	if err != nil && err != store.ErrNotFound {
		return err
	}
	rotate := err == nil && u.Role == "admin" && u.PassHash == store.HashPassword("bili-saver", u.Salt)
	if rotate && p.ConsolePassword == "" {
		p.ConsoleUser = u.Username
		p.ConsolePassword = Token()
	}
	if p.JWTSecret == "" {
		p.JWTSecret = Token()
	}
	if p.PlaybackPassword == "" {
		p.PlaybackPassword = Token()
		p.PlaybackUser = cfg.View().Teriteri.AdminUser
	}
	if err = v.save(p); err != nil {
		return err
	}
	if rotate {
		if err = st.UpdateUser(u.ID, &p.ConsolePassword, nil, nil); err != nil {
			return err
		}
	}
	// Explicit custom/environment credentials take precedence; known insecure values do not.
	return cfg.Update(func(next *config.Config) error {
		if next.Teriteri.AdminPass == "" || next.Teriteri.AdminPass == "bili-saver" || (p.PlaybackOverride && !config.EnvControlled("teriteri.admin_pass")) {
			next.Teriteri.AdminPass = p.PlaybackPassword
		}
		if next.Teriteri.JWTSecret == "" || next.Teriteri.JWTSecret == "bili-saver-default-jwt-secret" {
			next.Teriteri.JWTSecret = p.JWTSecret
		}
		return nil
	})
}

func (v *Vault) SetPlayback(user, password string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	p := v.value
	p.PlaybackUser = user
	p.PlaybackPassword = password
	p.PlaybackOverride = true
	return v.save(p)
}
