package server

import (
	"net/http"
)

func (s *Server) handleGuide(w http.ResponseWriter, r *http.Request) {
	path := s.Deps.ConfigPath
	if path == "" {
		path = "config.yaml"
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"data_dir": s.Deps.Cfg.View().DataDir, "config_file": path})
}
