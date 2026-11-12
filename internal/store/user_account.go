package store

import (
	"crypto/subtle"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrUsernameTaken = errors.New("该用户名已被使用")

func ValidateUsername(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 32 {
		return "", errors.New("用户名须为 2–32 个字符")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("用户名不能包含控制字符")
		}
	}
	return name, nil
}

func UserAvatarURL(path string) string {
	if strings.HasPrefix(path, "media/user-avatars/") && !strings.Contains(path, "..") {
		return "/media/" + path
	}
	return ""
}

// UpdateOwnAccount updates only mutable profile fields, never identity or privileges.
func (s *Store) UpdateOwnAccount(id int64, username, avatarPath *string) (*User, error) {
	var name, avatar any
	if username != nil {
		v, err := ValidateUsername(*username)
		if err != nil {
			return nil, err
		}
		name = v
	}
	if avatarPath != nil {
		avatar = *avatarPath
	}
	u, err := scanUser(s.DB.QueryRow(`UPDATE users SET username=COALESCE(?,username),avatar_path=COALESCE(?,avatar_path)
	 WHERE id=? AND enabled=1 RETURNING `+userCols, name, avatar, id))
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: users.username") {
		return nil, ErrUsernameTaken
	}
	return u, err
}

func ValidateNewPassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 8 || utf8.RuneCountInString(password) > 128 {
		return errors.New("新密码须为 8–128 个字符")
	}
	return nil
}

// ChangeOwnPassword uses a compare-and-swap so an intervening admin reset cannot be overwritten.
// The password and session invalidation commit together, including the current session.
func (s *Store) ChangeOwnPassword(id int64, currentPassword, newPassword string) error {
	if err := ValidateNewPassword(newPassword); err != nil {
		return err
	}
	u, err := s.GetUser(id)
	if err != nil || !u.Enabled {
		return ErrAuth
	}
	want := HashPassword(currentPassword, u.Salt)
	if subtle.ConstantTimeCompare([]byte(want), []byte(u.PassHash)) != 1 {
		return errors.New("当前密码不正确")
	}
	if currentPassword == newPassword {
		return errors.New("新密码不能与当前密码相同")
	}
	salt := newSalt()
	hash := HashPassword(newPassword, salt)
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE users SET pass_hash=?,salt=? WHERE id=? AND pass_hash=? AND enabled=1`, hash, salt, id, u.PassHash)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("账号状态已变化，请重新登录后再试")
	}
	if _, err = tx.Exec(`DELETE FROM sessions WHERE user_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
