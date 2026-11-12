package store

import (
	"net/url"
	"path/filepath"
	"strings"
)

// DatabaseURL escapes filesystem names so '#', spaces and Unicode cannot become
// SQLite URI query/fragment syntax. The same encoding is used by maintenance.
func DatabaseURL(path string) (url.URL, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return url.URL{}, err
	}
	value := filepath.ToSlash(absolute)
	if len(value) > 1 && value[1] == ':' && !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return url.URL{Scheme: "file", Path: value}, nil
}
