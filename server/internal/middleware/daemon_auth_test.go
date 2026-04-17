package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func daemonTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Skipf("skip db-backed daemon auth test: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("skip db-backed daemon auth test: %v", err)
	}
	return pool
}

func TestDaemonAuth_RejectsJWT(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "test-user-id",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString(auth.JWTSecret())
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}

	handler := DaemonAuth(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/daemon/register", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if body := w.Body.String(); body != "{\"error\":\"invalid daemon token\"}" {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestDaemonAuth_RejectsPAT(t *testing.T) {
	pool := daemonTestPool(t)
	defer pool.Close()

	handler := DaemonAuth(db.New(pool))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/daemon/register", nil)
	req.Header.Set("Authorization", "Bearer mul_not_allowed_here")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if body := w.Body.String(); body != "{\"error\":\"invalid daemon token\"}" {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestDaemonAuth_ValidDaemonTokenInjectsScope(t *testing.T) {
	pool := daemonTestPool(t)
	defer pool.Close()
	queries := db.New(pool)
	ctx := context.Background()

	var workspaceID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('daemon auth ws', 'daemon-auth-scope', 'test', 'DAS')
		RETURNING id
	`).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	defer pool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, workspaceID)

	var userID string
	if _, err := pool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, "daemon-auth-user@multica.ai"); err != nil {
		t.Fatalf("cleanup user: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('daemon auth user', 'daemon-auth-user@multica.ai')
		RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	defer pool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, userID)

	rawToken, err := auth.GenerateDaemonToken()
	if err != nil {
		t.Fatalf("generate daemon token: %v", err)
	}

	if _, err := queries.CreateDaemonToken(ctx, db.CreateDaemonTokenParams{
		TokenHash:   auth.HashToken(rawToken),
		WorkspaceID: parseUUIDForDaemonTest(t, workspaceID),
		DaemonID:    "daemon-auth-scope",
		UserID:      parseUUIDForDaemonTest(t, userID),
		ExpiresAt: pgtype.Timestamptz{
			Time:  time.Now().Add(time.Hour),
			Valid: true,
		},
	}); err != nil {
		t.Fatalf("create daemon token: %v", err)
	}

	var gotWorkspaceID, gotDaemonID, gotUserID string
	handler := DaemonAuth(queries)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotWorkspaceID = DaemonWorkspaceIDFromContext(r.Context())
		gotDaemonID = DaemonIDFromContext(r.Context())
		gotUserID = DaemonUserIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/daemon/register", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if gotWorkspaceID != workspaceID {
		t.Fatalf("expected workspace scope %q, got %q", workspaceID, gotWorkspaceID)
	}
	if gotDaemonID != "daemon-auth-scope" {
		t.Fatalf("expected daemon scope %q, got %q", "daemon-auth-scope", gotDaemonID)
	}
	if gotUserID != userID {
		t.Fatalf("expected user scope %q, got %q", userID, gotUserID)
	}
}

func parseUUIDForDaemonTest(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatalf("scan uuid %q: %v", s, err)
	}
	return u
}
