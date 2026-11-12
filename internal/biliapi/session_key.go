package biliapi

import (
	"crypto/sha256"
	"encoding/hex"
)

// SessionKey binds durable cursors to the login session without storing cookies.
// Switching accounts or refreshing SESSDATA invalidates an unfinished scan.
func (c *Client) SessionKey() string {
	sum := sha256.Sum256([]byte(c.cookieValue("SESSDATA") + "\x00" + c.cookieValue("DedeUserID")))
	return hex.EncodeToString(sum[:])
}
