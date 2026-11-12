package server

import (
	"net/http"
)

func (s *Server) handleDeliveries(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Deps.St.DB.QueryContext(r.Context(), `SELECT d.id,d.event_id,d.target,d.state,d.attempts,d.error,d.next_at,d.updated_at,e.event FROM event_deliveries d JOIN archive_events e ON e.id=d.event_id ORDER BY d.id DESC LIMIT 200`)
	if err != nil {
		errJSON(w, 500, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, eventID, next, updated int64
		var target, state, message, event string
		var attempts int
		if err = rows.Scan(&id, &eventID, &target, &state, &attempts, &message, &next, &updated, &event); err != nil {
			errJSON(w, 500, err)
			return
		}
		out = append(out, map[string]any{"id": id, "event_id": eventID, "target": target, "event": event, "state": state, "attempts": attempts, "error": message, "next_attempt_at": next, "updated_at": updated})
	}
	if err = rows.Err(); err != nil {
		errJSON(w, 500, err)
		return
	}
	writeJSON(w, 200, out)
}
