package store

// 多用户（v11）：users / sessions / user_tiers 三表 + 源归属（sources.user_id）。
// 密码 = 盐 + 迭代 sha256（stdlib 实现，无新依赖；面向局域网自部署）。

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

var ErrAuth = errors.New("认证失败")

const pwIterations = 20000

func HashPassword(password, salt string) string {
	b := []byte(salt + password)
	for i := 0; i < pwIterations; i++ {
		h := sha256.Sum256(b)
		b = h[:]
	}
	return hex.EncodeToString(b)
}

func newSalt() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	PassHash  string `json:"-"`
	Salt      string `json:"-"`
	Role      string `json:"role"` // admin | user
	TierID    int64  `json:"tier_id"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
	// 仅查询时填充（不出现在表里）
	TierName  string `json:"tier_name,omitempty"`
	TodayAdds int    `json:"today_adds,omitempty"`
}

type Session struct {
	Token     string
	UserID    int64
	ExpiresAt int64
}

type UserTier struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	MaxDailyAdds   int    `json:"max_daily_adds"`       // 每日可添加补档数
	MinIntervalMin int    `json:"min_interval_minutes"` // 检查频率下限（分钟），用户设更小则钳到该值
}

const userCols = `id, username, pass_hash, salt, role, tier_id, enabled, created_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var enabled int
	err := row.Scan(&u.ID, &u.Username, &u.PassHash, &u.Salt, &u.Role, &u.TierID, &enabled, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.Enabled = enabled == 1
	return &u, nil
}

// GetUserByUsername 登录用。
func (s *Store) GetUserByUsername(username string) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE username=?`, username))
}

func (s *Store) GetUser(id int64) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id))
}

// ListUsers 全部用户（附等级名与当日添加数）。
func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.DB.Query(`SELECT u.`+userColsWithPrefix("u")+`, COALESCE(t.name,''), 
		(SELECT COUNT(*) FROM sources s2 WHERE s2.user_id=u.id AND s2.created_at>=? AND s2.created_at<?)
		FROM users u LEFT JOIN user_tiers t ON t.id=u.tier_id ORDER BY u.id`,
		todayStart(), todayStart()+86400)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		var u User
		var enabled int
		var tierName string
		if err := rows.Scan(&u.ID, &u.Username, &u.PassHash, &u.Salt, &u.Role, &u.TierID, &enabled, &u.CreatedAt, &tierName, &u.TodayAdds); err != nil {
			return nil, err
		}
		u.Enabled = enabled == 1
		u.TierName = tierName
		out = append(out, &u)
	}
	return out, rows.Err()
}

func userColsWithPrefix(prefix string) string {
	return prefix + ".id, " + prefix + ".username, " + prefix + ".pass_hash, " + prefix + ".salt, " +
		prefix + ".role, " + prefix + ".tier_id, " + prefix + ".enabled, " + prefix + ".created_at"
}

// CreateUser 建用户（唯一用户名）。
func (s *Store) CreateUser(username, password, role string, tierID int64) (*User, error) {
	salt := newSalt()
	now := time.Now().Unix()
	res, err := s.DB.Exec(`INSERT INTO users(username, pass_hash, salt, role, tier_id, enabled, created_at)
		VALUES(?,?,?,?,?,?,?)`,
		username, HashPassword(password, salt), salt, role, tierID, 1, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetUser(id)
}

// UpdateUserAuth 改密码/等级/启用态（指针 nil=不改）；admin 角色不可经此降级为 user。
func (s *Store) UpdateUser(id int64, password *string, tierID *int64, enabled *bool) error {
	u, err := s.GetUser(id)
	if err != nil {
		return err
	}
	if u.Role == "admin" && enabled != nil && !*enabled {
		return errors.New("不能停用管理员账号")
	}
	if password != nil && *password != "" {
		u.Salt = newSalt()
		u.PassHash = HashPassword(*password, u.Salt)
	}
	if tierID != nil {
		u.TierID = *tierID
	}
	if enabled != nil {
		if *enabled {
			u.Enabled = true
		} else {
			u.Enabled = false
		}
	}
	_, err = s.DB.Exec(`UPDATE users SET pass_hash=?, salt=?, tier_id=?, enabled=? WHERE id=?`,
		u.PassHash, u.Salt, u.TierID, boolInt(u.Enabled), id)
	return err
}

// DeleteUser 删除用户；其名下补档归管理员（user_id=0），会话一并清除。
func (s *Store) DeleteUser(id int64) error {
	u, err := s.GetUser(id)
	if err != nil {
		return err
	}
	if u.Role == "admin" {
		if n := s.countAdmins(); n <= 1 {
			return errors.New("至少保留一个管理员账号")
		}
	}
	if _, err := s.DB.Exec(`UPDATE sources SET user_id=0 WHERE user_id=?`, id); err != nil {
		return err
	}
	if _, err := s.DB.Exec(`DELETE FROM sessions WHERE user_id=?`, id); err != nil {
		return err
	}
	_, err = s.DB.Exec(`DELETE FROM users WHERE id=?`, id)
	return err
}

func (s *Store) countAdmins() int {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin'`).Scan(&n)
	return n
}

// ---- 会话 ----

// CreateSession 生成随机 token 会话（7 天）。
func (s *Store) CreateSession(userID int64) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	exp := time.Now().Add(7 * 24 * time.Hour).Unix()
	// 顺手清理过期会话
	_, _ = s.DB.Exec(`DELETE FROM sessions WHERE expires_at<?`, time.Now().Unix())
	_, err := s.DB.Exec(`INSERT INTO sessions(token, user_id, expires_at) VALUES(?,?,?)`, token, userID, exp)
	if err != nil {
		return "", err
	}
	return token, nil
}

// UserBySession 校验会话并返回用户（过期/禁用一律视为未登录）。
func (s *Store) UserBySession(token string) (*User, error) {
	if token == "" {
		return nil, ErrAuth
	}
	var userID int64
	var exp int64
	err := s.DB.QueryRow(`SELECT user_id, expires_at FROM sessions WHERE token=?`, token).Scan(&userID, &exp)
	if err != nil || exp < time.Now().Unix() {
		return nil, ErrAuth
	}
	u, err := s.GetUser(userID)
	if err != nil || !u.Enabled {
		return nil, ErrAuth
	}
	return u, nil
}

func (s *Store) DeleteSession(token string) {
	_, _ = s.DB.Exec(`DELETE FROM sessions WHERE token=?`, token)
}

// ---- 等级 ----

const tierCols = `id, name, max_daily_adds, min_interval_minutes`

func scanTier(row interface{ Scan(...any) error }) (*UserTier, error) {
	var t UserTier
	err := row.Scan(&t.ID, &t.Name, &t.MaxDailyAdds, &t.MinIntervalMin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &t, err
}

func (s *Store) ListTiers() ([]*UserTier, error) {
	rows, err := s.DB.Query(`SELECT ` + tierCols + ` FROM user_tiers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*UserTier
	for rows.Next() {
		t, err := scanTier(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetTier(id int64) (*UserTier, error) {
	return scanTier(s.DB.QueryRow(`SELECT `+tierCols+` FROM user_tiers WHERE id=?`, id))
}

func (s *Store) UpsertTier(t *UserTier) (int64, error) {
	if t.ID > 0 {
		_, err := s.DB.Exec(`UPDATE user_tiers SET name=?, max_daily_adds=?, min_interval_minutes=? WHERE id=?`,
			t.Name, t.MaxDailyAdds, t.MinIntervalMin, t.ID)
		return t.ID, err
	}
	res, err := s.DB.Exec(`INSERT INTO user_tiers(name, max_daily_adds, min_interval_minutes) VALUES(?,?,?)`,
		t.Name, t.MaxDailyAdds, t.MinIntervalMin)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteTier 删除等级（有用户在用时拒绝）。
func (s *Store) DeleteTier(id int64) error {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE tier_id=?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return errors.New("该等级仍有用户使用，请先转移用户")
	}
	_, err := s.DB.Exec(`DELETE FROM user_tiers WHERE id=?`, id)
	return err
}

// CountUserAddsToday 用户当日已添加补档数（本地时区零点起）。
func (s *Store) CountUserAddsToday(userID int64) (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM sources WHERE user_id=? AND created_at>=? AND created_at<?`,
		userID, todayStart(), todayStart()+86400).Scan(&n)
	return n, err
}

func todayStart() int64 {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
}
