package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/trustwall1337/beacongate/server/policy"
)

func (a *API) handleRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.writeJSON(w, http.StatusOK, a.store.List())
	case http.MethodPost:
		var rule policy.Rule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			a.writeError(w, &httpError{status: http.StatusBadRequest, msg: err.Error()})
			return
		}
		if rule.UpdatedAt.IsZero() {
			rule.UpdatedAt = time.Now().UTC()
		}
		if err := a.store.Upsert(rule); err != nil {
			if errors.Is(err, policy.ErrInvalidRule) {
				a.writeError(w, &httpError{status: http.StatusBadRequest, msg: err.Error()})
				return
			}
			a.writeError(w, err)
			return
		}
		a.reload()
		a.writeJSON(w, http.StatusCreated, rule)
	default:
		a.writeError(w, &httpError{status: http.StatusMethodNotAllowed, msg: "method not allowed"})
	}
}

func (a *API) handleRule(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/policy/rules/")
	if id == "" || strings.Contains(id, "/") {
		a.writeError(w, &httpError{status: http.StatusBadRequest, msg: "rule id required"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		rule, ok := a.store.Get(id)
		if !ok {
			a.writeError(w, &httpError{status: http.StatusNotFound, msg: "no such rule"})
			return
		}
		a.writeJSON(w, http.StatusOK, rule)
	case http.MethodPut:
		var rule policy.Rule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			a.writeError(w, &httpError{status: http.StatusBadRequest, msg: err.Error()})
			return
		}
		rule.ID = id
		rule.UpdatedAt = time.Now().UTC()
		if err := a.store.Upsert(rule); err != nil {
			if errors.Is(err, policy.ErrInvalidRule) {
				a.writeError(w, &httpError{status: http.StatusBadRequest, msg: err.Error()})
				return
			}
			a.writeError(w, err)
			return
		}
		a.reload()
		a.writeJSON(w, http.StatusOK, rule)
	case http.MethodDelete:
		removed, err := a.store.Delete(id)
		if err != nil {
			a.writeError(w, err)
			return
		}
		if !removed {
			a.writeError(w, &httpError{status: http.StatusNotFound, msg: "no such rule"})
			return
		}
		a.reload()
		w.WriteHeader(http.StatusNoContent)
	default:
		a.writeError(w, &httpError{status: http.StatusMethodNotAllowed, msg: "method not allowed"})
	}
}
