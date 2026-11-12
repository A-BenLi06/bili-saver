package server

import (
	"net/http/httptest"
	"testing"
)

func TestPluginCapabilityCannotAdminister(t *testing.T) {
	s := linkFixture(t)
	s.Deps.PluginToken = "scoped-test-capability"
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/api/v1/videos", 200}, {"GET", "/api/v1/config", 403}, {"POST", "/api/v1/add", 403}, {"GET", "/api/v1/archive/catalog", 403}} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", "Bearer scoped-test-capability")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
}
