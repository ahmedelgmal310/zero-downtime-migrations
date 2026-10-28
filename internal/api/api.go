// Package api is the HTTP surface of the users service. The API contract is
// identical in every release (clients send and receive a full "name"); only
// the storage behind it changes, which is what lets old and new pods serve
// the same traffic during a rolling update.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/names"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/store"
)

// Server serves the API for one release.
type Server struct {
	Version string
	Store   store.Store
	Log     *slog.Logger

	ready    atomic.Bool
	draining atomic.Bool
}

// SetReady records whether the schema is compatible with this release.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

// Drain makes the readiness probe fail for good (shutdown in progress).
func (s *Server) Drain() { s.draining.Store(true) }

// Ready reports whether the pod should receive traffic.
func (s *Server) Ready() bool { return s.ready.Load() && !s.draining.Load() }

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /users", s.create)
	mux.HandleFunc("GET /users/{id}", s.get)
	mux.HandleFunc("GET /users", s.search)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.Ready() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"version": s.Version, "columns": s.Store.RequiredColumns()})
	})
	return s.versionHeader(mux)
}

// versionHeader tags every response with the release that served it, so the
// load test can show which versions handled traffic during each step.
func (s *Server) versionHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-App-Version", s.Version)
		next.ServeHTTP(w, r)
	})
}

type createRequest struct {
	Name string `json:"name"`
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := names.Validate(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.Store.Create(r.Context(), req.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return
	}
	u, err := s.Store.Get(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	last := r.URL.Query().Get("last_name")
	if last == "" {
		writeError(w, http.StatusBadRequest, "last_name is required")
		return
	}
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			writeError(w, http.StatusBadRequest, "limit must be 1-100")
			return
		}
		limit = n
	}
	users, err := s.Store.SearchByLastName(r.Context(), last, limit)
	if errors.Is(err, store.ErrUnsupported) {
		writeError(w, http.StatusNotImplemented, "search by last name needs release v3")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if users == nil {
		users = []store.User{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
