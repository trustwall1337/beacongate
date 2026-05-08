package admin

import "net/http"

type StatusReport struct {
	ServerID     string `json:"server_id,omitempty"`
	SessionCount int    `json:"session_count"`
}

func (a *API) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		a.writeError(w, &httpError{status: http.StatusMethodNotAllowed, msg: "method not allowed"})
		return
	}
	report := StatusReport{}
	if a.server != nil {
		report.SessionCount = a.server.SessionCount()
	}
	a.writeJSON(w, http.StatusOK, report)
}
