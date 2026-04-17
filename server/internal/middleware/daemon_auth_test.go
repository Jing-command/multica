package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func buildValidJWTForDaemonAuthTests(t *testing.T) string {
	t.Helper()

	claims := jwt.MapClaims{
		"sub":   "test-user-id",
		"email": "test@multica.ai",
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(auth.JWTSecret())
	if err != nil {
		t.Fatalf("failed to sign JWT: %v", err)
	}
	return signed
}

type fakeDBTX struct {
	queryRowFn func(ctx context.Context, sql string, args ...interface{}) pgx.Row
}

func (f fakeDBTX) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("not implemented")
}

func (f fakeDBTX) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, errors.New("not implemented")
}

func (f fakeDBTX) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	if f.queryRowFn == nil {
		return fakeRow{err: errors.New("queryRowFn not set")}
	}
	return f.queryRowFn(ctx, sql, args...)
}

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("scan destination/value length mismatch")
	}
	for i := range dest {
		switch d := dest[i].(type) {
		case *pgtype.UUID:
			v, ok := r.values[i].(pgtype.UUID)
			if !ok {
				return errors.New("value type mismatch for pgtype.UUID")
			}
			*d = v
		case *string:
			v, ok := r.values[i].(string)
			if !ok {
				return errors.New("value type mismatch for string")
			}
			*d = v
		case *pgtype.Timestamptz:
			v, ok := r.values[i].(pgtype.Timestamptz)
			if !ok {
				return errors.New("value type mismatch for pgtype.Timestamptz")
			}
			*d = v
		default:
			return errors.New("unsupported scan destination type")
		}
	}
	return nil
}

func mustParseUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatalf("failed to parse uuid %q: %v", s, err)
	}
	return u
}

func TestDaemonAuth_MissingHeader(t *testing.T) {
	handler := DaemonAuth(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/daemon/poll", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if body := w.Body.String(); body != `{"error":"missing authorization header"}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestDaemonAuth_InvalidAuthorizationFormat(t *testing.T) {
	handler := DaemonAuth(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/daemon/poll", nil)
	req.Header.Set("Authorization", "Token some-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if body := w.Body.String(); body != `{"error":"invalid authorization format"}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestDaemonAuth_ValidDaemonTokenPassesAndInjectsContext(t *testing.T) {
	tokenString := "mdt_test_daemon_token"
	expectedWorkspaceUUID := mustParseUUID(t, "11111111-1111-1111-1111-111111111111")
	expectedWorkspaceID := "11111111-1111-1111-1111-111111111111"
	expectedDaemonID := "daemon-123"

	queries := db.New(fakeDBTX{
		queryRowFn: func(ctx context.Context, sql string, args ...interface{}) pgx.Row {
			if len(args) != 1 {
				return fakeRow{err: errors.New("expected one query argument")}
			}
			hashArg, ok := args[0].(string)
			if !ok {
				return fakeRow{err: errors.New("expected hash argument as string")}
			}
			if hashArg != auth.HashToken(tokenString) {
				return fakeRow{err: errors.New("unexpected token hash")}
			}

			now := time.Now()
			return fakeRow{values: []any{
				mustParseUUID(t, "22222222-2222-2222-2222-222222222222"),
				hashArg,
				expectedWorkspaceUUID,
				expectedDaemonID,
				pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true},
				pgtype.Timestamptz{Time: now, Valid: true},
			}}
		},
	})

	nextCalled := false
	handler := DaemonAuth(queries)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		if got := DaemonWorkspaceIDFromContext(r.Context()); got != expectedWorkspaceID {
			t.Fatalf("expected workspace ID %q, got %q", expectedWorkspaceID, got)
		}
		if got := DaemonIDFromContext(r.Context()); got != expectedDaemonID {
			t.Fatalf("expected daemon ID %q, got %q", expectedDaemonID, got)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/daemon/poll", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if !nextCalled {
		t.Fatal("expected next handler to be called")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestDaemonAuth_RejectPATFallback(t *testing.T) {
	handler := DaemonAuth(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/daemon/poll", nil)
	req.Header.Set("Authorization", "Bearer mul_test_pat_token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if body := w.Body.String(); body != `{"error":"daemon token required"}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestDaemonAuth_RejectJWTFallback(t *testing.T) {
	handler := DaemonAuth(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	token := buildValidJWTForDaemonAuthTests(t)
	req := httptest.NewRequest("GET", "/daemon/poll", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if body := w.Body.String(); body != `{"error":"daemon token required"}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestDaemonAuth_RejectsUserJWTOnDaemonRoute(t *testing.T) {
	r := chi.NewRouter()
	r.Route("/api/daemon", func(r chi.Router) {
		r.Use(DaemonAuth(nil))
		r.Get("/tasks/pending", func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("daemon route handler should not be called")
		})
	})

	token := buildValidJWTForDaemonAuthTests(t)
	req := httptest.NewRequest("GET", "/api/daemon/tasks/pending", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if body := w.Body.String(); body != `{"error":"daemon token required"}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestDaemonAuth_RequireDaemonTokenPrefix(t *testing.T) {
	handler := DaemonAuth(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/daemon/poll", nil)
	req.Header.Set("Authorization", "Bearer not-a-daemon-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if body := w.Body.String(); body != `{"error":"daemon token required"}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestDaemonAuth_RejectDaemonTokenWhenQueriesNil(t *testing.T) {
	handler := DaemonAuth(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/daemon/poll", nil)
	req.Header.Set("Authorization", "Bearer mdt_test_daemon_token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if body := w.Body.String(); body != `{"error":"invalid daemon token"}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestDaemonWorkspaceIDFromContext_UnsetReturnsEmptyString(t *testing.T) {
	if got := DaemonWorkspaceIDFromContext(context.Background()); got != "" {
		t.Fatalf("expected empty workspace ID, got %q", got)
	}
}
