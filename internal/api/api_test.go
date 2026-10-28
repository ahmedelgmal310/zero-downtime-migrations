package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/api"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/names"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/pgtest"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/store"
)

type fakeStore struct {
	users  map[int64]store.User
	next   int64
	err    error
	search bool
}

func (f *fakeStore) Create(_ context.Context, name string) (store.User, error) {
	if f.err != nil {
		return store.User{}, f.err
	}
	f.next++
	first, last := names.Split(name)
	u := store.User{ID: f.next, Name: name, FirstName: &first, LastName: &last}
	f.users[u.ID] = u
	return u, nil
}

func (f *fakeStore) Get(_ context.Context, id int64) (store.User, error) {
	if f.err != nil {
		return store.User{}, f.err
	}
	u, ok := f.users[id]
	if !ok {
		return u, store.ErrNotFound
	}
	return u, nil
}

func (f *fakeStore) SearchByLastName(_ context.Context, last string, limit int) ([]store.User, error) {
	if !f.search {
		return nil, store.ErrUnsupported
	}
	if f.err != nil {
		return nil, f.err
	}
	var out []store.User
	for _, u := range f.users {
		if *u.LastName == last && len(out) < limit {
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *fakeStore) RequiredColumns() []string { return []string{"id", "name"} }

func newServer(f *fakeStore) (*api.Server, http.Handler) {
	s := &api.Server{Version: "v2", Store: f, Log: pgtest.Discard()}
	return s, s.Handler()
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-App-Version") != "v2" {
		t.Errorf("%s %s: missing X-App-Version", method, path)
	}
	return rec
}

func TestCRUD(t *testing.T) {
	f := &fakeStore{users: map[int64]store.User{}, search: true}
	_, h := newServer(f)

	rec := do(t, h, http.MethodPost, "/users", `{"name":"Ada Lovelace"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var u store.User
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil || u.ID != 1 || u.Name != "Ada Lovelace" || *u.LastName != "Lovelace" {
		t.Fatalf("created %+v %v", u, err)
	}

	rec = do(t, h, http.MethodGet, "/users/1", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"Ada Lovelace"`) {
		t.Errorf("get = %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet, "/users?last_name=Lovelace&limit=5", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"users":[{`) {
		t.Errorf("search = %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet, "/users?last_name=Nobody", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"users":[]`) {
		t.Errorf("empty search = %d %s", rec.Code, rec.Body)
	}
}

func TestErrors(t *testing.T) {
	f := &fakeStore{users: map[int64]store.User{}}
	_, h := newServer(f)
	cases := []struct {
		method, path, body string
		code               int
	}{
		{http.MethodPost, "/users", `not json`, http.StatusBadRequest},
		{http.MethodPost, "/users", `{"name":"x","extra":1}`, http.StatusBadRequest},
		{http.MethodPost, "/users", `{"name":" padded"}`, http.StatusBadRequest},
		{http.MethodPost, "/users", `{"name":""}`, http.StatusBadRequest},
		{http.MethodGet, "/users/abc", "", http.StatusBadRequest},
		{http.MethodGet, "/users/0", "", http.StatusBadRequest},
		{http.MethodGet, "/users/42", "", http.StatusNotFound},
		{http.MethodGet, "/users", "", http.StatusBadRequest},
		{http.MethodGet, "/users?last_name=x&limit=0", "", http.StatusBadRequest},
		{http.MethodGet, "/users?last_name=x&limit=abc", "", http.StatusBadRequest},
		{http.MethodGet, "/users?last_name=x", "", http.StatusNotImplemented},
		{http.MethodDelete, "/users/1", "", http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		if rec := do(t, h, c.method, c.path, c.body); rec.Code != c.code {
			t.Errorf("%s %s %q = %d, want %d (%s)", c.method, c.path, c.body, rec.Code, c.code, rec.Body)
		}
	}

	f.err, f.search = errors.New("db down"), true
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/users", `{"name":"Ada"}`},
		{http.MethodGet, "/users/1", ""},
		{http.MethodGet, "/users?last_name=x", ""},
	} {
		if rec := do(t, h, c.method, c.path, c.body); rec.Code != http.StatusInternalServerError ||
			strings.Contains(rec.Body.String(), "db down") {
			t.Errorf("%s %s with store error = %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
}

func TestProbes(t *testing.T) {
	s, h := newServer(&fakeStore{users: map[int64]store.User{}})
	if rec := do(t, h, http.MethodGet, "/healthz", ""); rec.Code != http.StatusOK {
		t.Errorf("healthz = %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/readyz", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz before schema check = %d", rec.Code)
	}
	s.SetReady(true)
	if rec := do(t, h, http.MethodGet, "/readyz", ""); rec.Code != http.StatusOK {
		t.Errorf("readyz when ready = %d", rec.Code)
	}
	s.Drain()
	if rec := do(t, h, http.MethodGet, "/readyz", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz while draining = %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/version", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"version":"v2"`) {
		t.Errorf("version = %d %s", rec.Code, rec.Body)
	}
}
