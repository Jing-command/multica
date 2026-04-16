package handler

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type UserResponse struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Email     string  `json:"email"`
	AvatarURL *string `json:"avatar_url"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

func userToResponse(u db.User) UserResponse {
	return UserResponse{
		ID:        uuidToString(u.ID),
		Name:      u.Name,
		Email:     u.Email,
		AvatarURL: textToPtr(u.AvatarUrl),
		CreatedAt: timestampToString(u.CreatedAt),
		UpdatedAt: timestampToString(u.UpdatedAt),
	}
}

type LoginResponse struct {
	Token string       `json:"token"`
	User  UserResponse `json:"user"`
}

type SendCodeRequest struct {
	Email string `json:"email"`
}

type VerifyCodeRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

func defaultWorkspaceName(user db.User) string {
	name := strings.TrimSpace(user.Name)
	if name == "" {
		email := strings.TrimSpace(user.Email)
		if at := strings.Index(email, "@"); at > 0 {
			name = email[:at]
		}
	}
	if name == "" {
		name = "Personal"
	}
	return name + "'s Workspace"
}

func slugifyWorkspacePart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastWasDash := false

	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastWasDash = false
		case b.Len() > 0 && !lastWasDash:
			b.WriteByte('-')
			lastWasDash = true
		}
	}

	return strings.Trim(b.String(), "-")
}

func defaultWorkspaceSlug(user db.User) string {
	candidates := []string{
		slugifyWorkspacePart(user.Name),
		slugifyWorkspacePart(strings.Split(strings.TrimSpace(user.Email), "@")[0]),
		"workspace",
	}

	base := "workspace"
	for _, candidate := range candidates {
		if candidate != "" {
			base = candidate
			break
		}
	}

	userID := uuidToString(user.ID)
	if len(userID) >= 8 {
		return base + "-" + userID[:8]
	}
	return base
}

func (h *Handler) ensureUserWorkspace(ctx context.Context, user db.User) error {
	workspaces, err := h.Queries.ListWorkspaces(ctx, user.ID)
	if err != nil {
		return err
	}
	if len(workspaces) > 0 {
		return nil
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	qtx := h.Queries.WithTx(tx)
	workspaces, err = qtx.ListWorkspaces(ctx, user.ID)
	if err != nil {
		return err
	}
	if len(workspaces) > 0 {
		return nil
	}

	wsName := defaultWorkspaceName(user)
	workspace, err := qtx.CreateWorkspace(ctx, db.CreateWorkspaceParams{
		Name:        wsName,
		Slug:        defaultWorkspaceSlug(user),
		Description: pgtype.Text{},
		IssuePrefix: generateIssuePrefix(wsName),
	})
	if err != nil {
		if isUniqueViolation(err) {
			workspaces, lookupErr := h.Queries.ListWorkspaces(ctx, user.ID)
			if lookupErr == nil && len(workspaces) > 0 {
				return nil
			}
		}
		return err
	}

	if _, err := qtx.CreateMember(ctx, db.CreateMemberParams{
		WorkspaceID: workspace.ID,
		UserID:      user.ID,
		Role:        "owner",
	}); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func generateCode() (string, error) {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint32(buf[:]) % 1000000
	return fmt.Sprintf("%06d", n), nil
}

func (h *Handler) issueJWT(user db.User) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   uuidToString(user.ID),
		"email": user.Email,
		"name":  user.Name,
		"exp":   time.Now().Add(30 * 24 * time.Hour).Unix(),
		"iat":   time.Now().Unix(),
	})
	return token.SignedString(auth.JWTSecret())
}

const (
	authAbuseEventSendCodeRequested = "send_code_requested"
	authAbuseEventSendCodeBlocked   = "send_code_blocked"
	authAbuseEventVerifyCodeFailed  = "verify_code_failed"
	authAbuseEventVerifyCodeBlocked = "verify_code_blocked"

	sendCodeCooldown               = 60 * time.Second
	sendCodeIdentifierWindow       = 15 * time.Minute
	sendCodeIdentifierLimit        = 5
	sendCodeIdentifierDailyWindow  = 24 * time.Hour
	sendCodeIdentifierDailyLimit   = 20
	sendCodeIPWindow               = 15 * time.Minute
	sendCodeIPLimit                = 20
	sendCodeIPDailyWindow          = 24 * time.Hour
	sendCodeIPDailyLimit           = 100
	sendCodeIPIdentifierWindow     = 15 * time.Minute
	sendCodeIPIdentifierLimit      = 3
	verifyCodeIdentifierFailWindow = 15 * time.Minute
	verifyCodeIdentifierFailLimit  = 10
	verifyCodeIPFailWindow         = 15 * time.Minute
	verifyCodeIPFailLimit          = 25
)

type authGuardDecision struct {
	Allow  bool
	Reason string
}

func normalizeEmailIdentifier(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func clientIP(r *http.Request) string {
	host := strings.TrimSpace(r.RemoteAddr)
	if host == "" {
		return "unknown"
	}
	if strings.Contains(host, ":") {
		if parsedHost, _, err := net.SplitHostPort(host); err == nil {
			return parsedHost
		}
	}
	return host
}

func (h *Handler) recordAuthAbuseEvent(ctx context.Context, eventType, identifier, ip string) {
	_, err := h.Queries.CreateAuthAbuseEvent(ctx, db.CreateAuthAbuseEventParams{
		EventType:  eventType,
		Identifier: identifier,
		Ip:         ip,
	})
	if err != nil {
		slog.Warn("record auth abuse event failed", "event_type", eventType, "identifier", identifier, "ip", ip, "error", err)
	}
}

func (h *Handler) countAuthEventsByIdentifier(ctx context.Context, eventType, identifier string, since time.Time) (int32, error) {
	count, err := h.Queries.CountAuthAbuseEventsByIdentifierSince(ctx, db.CountAuthAbuseEventsByIdentifierSinceParams{
		EventType:  eventType,
		Identifier: identifier,
		CreatedAt:  pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		slog.Warn("count auth abuse event by identifier failed", "event_type", eventType, "identifier", identifier, "error", err)
		return 0, err
	}
	return count, nil
}

func (h *Handler) countAuthEventsByIP(ctx context.Context, eventType, ip string, since time.Time) (int32, error) {
	count, err := h.Queries.CountAuthAbuseEventsByIPSince(ctx, db.CountAuthAbuseEventsByIPSinceParams{
		EventType: eventType,
		Ip:        ip,
		CreatedAt: pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		slog.Warn("count auth abuse event by ip failed", "event_type", eventType, "ip", ip, "error", err)
		return 0, err
	}
	return count, nil
}

func (h *Handler) countAuthEventsByIPAndIdentifier(ctx context.Context, eventType, ip, identifier string, since time.Time) (int32, error) {
	count, err := h.Queries.CountAuthAbuseEventsByIPAndIdentifierSince(ctx, db.CountAuthAbuseEventsByIPAndIdentifierSinceParams{
		EventType:  eventType,
		Ip:         ip,
		Identifier: identifier,
		CreatedAt:  pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		slog.Warn("count auth abuse event by ip and identifier failed", "event_type", eventType, "ip", ip, "identifier", identifier, "error", err)
		return 0, err
	}
	return count, nil
}

func (h *Handler) evaluateSendCodeGuard(ctx context.Context, identifier, ip string, now time.Time) authGuardDecision {
	cooldownCount, err := h.countAuthEventsByIdentifier(ctx, authAbuseEventSendCodeRequested, identifier, now.Add(-sendCodeCooldown))
	if err != nil {
		return authGuardDecision{Allow: false, Reason: "cooldown_lookup_error"}
	}
	if cooldownCount > 0 {
		return authGuardDecision{Allow: false, Reason: "cooldown"}
	}

	identifierRequestedCount, err := h.countAuthEventsByIdentifier(ctx, authAbuseEventSendCodeRequested, identifier, now.Add(-sendCodeIdentifierWindow))
	if err != nil {
		return authGuardDecision{Allow: false, Reason: "identifier_window_lookup_error"}
	}
	identifierBlockedCount, err := h.countAuthEventsByIdentifier(ctx, authAbuseEventSendCodeBlocked, identifier, now.Add(-sendCodeIdentifierWindow))
	if err != nil {
		return authGuardDecision{Allow: false, Reason: "identifier_window_lookup_error"}
	}
	if identifierRequestedCount+identifierBlockedCount >= sendCodeIdentifierLimit {
		return authGuardDecision{Allow: false, Reason: "identifier_window"}
	}

	identifierDailyRequestedCount, err := h.countAuthEventsByIdentifier(ctx, authAbuseEventSendCodeRequested, identifier, now.Add(-sendCodeIdentifierDailyWindow))
	if err != nil {
		return authGuardDecision{Allow: false, Reason: "identifier_daily_window_lookup_error"}
	}
	identifierDailyBlockedCount, err := h.countAuthEventsByIdentifier(ctx, authAbuseEventSendCodeBlocked, identifier, now.Add(-sendCodeIdentifierDailyWindow))
	if err != nil {
		return authGuardDecision{Allow: false, Reason: "identifier_daily_window_lookup_error"}
	}
	if identifierDailyRequestedCount+identifierDailyBlockedCount >= sendCodeIdentifierDailyLimit {
		return authGuardDecision{Allow: false, Reason: "identifier_daily_window"}
	}

	ipRequestedCount, err := h.countAuthEventsByIP(ctx, authAbuseEventSendCodeRequested, ip, now.Add(-sendCodeIPWindow))
	if err != nil {
		return authGuardDecision{Allow: false, Reason: "ip_window_lookup_error"}
	}
	ipBlockedCount, err := h.countAuthEventsByIP(ctx, authAbuseEventSendCodeBlocked, ip, now.Add(-sendCodeIPWindow))
	if err != nil {
		return authGuardDecision{Allow: false, Reason: "ip_window_lookup_error"}
	}
	if ipRequestedCount+ipBlockedCount >= sendCodeIPLimit {
		return authGuardDecision{Allow: false, Reason: "ip_window"}
	}

	ipDailyRequestedCount, err := h.countAuthEventsByIP(ctx, authAbuseEventSendCodeRequested, ip, now.Add(-sendCodeIPDailyWindow))
	if err != nil {
		return authGuardDecision{Allow: false, Reason: "ip_daily_window_lookup_error"}
	}
	ipDailyBlockedCount, err := h.countAuthEventsByIP(ctx, authAbuseEventSendCodeBlocked, ip, now.Add(-sendCodeIPDailyWindow))
	if err != nil {
		return authGuardDecision{Allow: false, Reason: "ip_daily_window_lookup_error"}
	}
	if ipDailyRequestedCount+ipDailyBlockedCount >= sendCodeIPDailyLimit {
		return authGuardDecision{Allow: false, Reason: "ip_daily_window"}
	}

	ipIdentifierRequestedCount, err := h.countAuthEventsByIPAndIdentifier(ctx, authAbuseEventSendCodeRequested, ip, identifier, now.Add(-sendCodeIPIdentifierWindow))
	if err != nil {
		return authGuardDecision{Allow: false, Reason: "ip_identifier_window_lookup_error"}
	}
	ipIdentifierBlockedCount, err := h.countAuthEventsByIPAndIdentifier(ctx, authAbuseEventSendCodeBlocked, ip, identifier, now.Add(-sendCodeIPIdentifierWindow))
	if err != nil {
		return authGuardDecision{Allow: false, Reason: "ip_identifier_window_lookup_error"}
	}
	if ipIdentifierRequestedCount+ipIdentifierBlockedCount >= sendCodeIPIdentifierLimit {
		return authGuardDecision{Allow: false, Reason: "ip_identifier_window"}
	}

	return authGuardDecision{Allow: true}
}

func (h *Handler) evaluateVerifyCodeGuard(ctx context.Context, identifier, ip string, now time.Time) authGuardDecision {
	identifierFailedCount, err := h.countAuthEventsByIdentifier(ctx, authAbuseEventVerifyCodeFailed, identifier, now.Add(-verifyCodeIdentifierFailWindow))
	if err != nil {
		return authGuardDecision{Allow: false, Reason: "identifier_failed_window_lookup_error"}
	}
	identifierBlockedCount, err := h.countAuthEventsByIdentifier(ctx, authAbuseEventVerifyCodeBlocked, identifier, now.Add(-verifyCodeIdentifierFailWindow))
	if err != nil {
		return authGuardDecision{Allow: false, Reason: "identifier_failed_window_lookup_error"}
	}
	if identifierFailedCount+identifierBlockedCount >= verifyCodeIdentifierFailLimit {
		return authGuardDecision{Allow: false, Reason: "identifier_failed_window"}
	}

	ipFailedCount, err := h.countAuthEventsByIP(ctx, authAbuseEventVerifyCodeFailed, ip, now.Add(-verifyCodeIPFailWindow))
	if err != nil {
		return authGuardDecision{Allow: false, Reason: "ip_failed_window_lookup_error"}
	}
	ipBlockedCount, err := h.countAuthEventsByIP(ctx, authAbuseEventVerifyCodeBlocked, ip, now.Add(-verifyCodeIPFailWindow))
	if err != nil {
		return authGuardDecision{Allow: false, Reason: "ip_failed_window_lookup_error"}
	}
	if ipFailedCount+ipBlockedCount >= verifyCodeIPFailLimit {
		return authGuardDecision{Allow: false, Reason: "ip_failed_window"}
	}
	return authGuardDecision{Allow: true}
}

func (h *Handler) findOrCreateUser(ctx context.Context, email string) (db.User, error) {
	user, err := h.Queries.GetUserByEmail(ctx, email)
	if err != nil {
		if !isNotFound(err) {
			return db.User{}, err
		}
		name := email
		if at := strings.Index(email, "@"); at > 0 {
			name = email[:at]
		}
		user, err = h.Queries.CreateUser(ctx, db.CreateUserParams{
			Name:  name,
			Email: email,
		})
		if err != nil {
			return db.User{}, err
		}
	}
	return user, nil
}

func (h *Handler) SendCode(w http.ResponseWriter, r *http.Request) {
	var req SendCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	email := normalizeEmailIdentifier(req.Email)
	if email == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}

	now := time.Now()
	ip := clientIP(r)
	decision := h.evaluateSendCodeGuard(r.Context(), email, ip, now)
	if !decision.Allow {
		slog.Warn("send code blocked", append(logger.RequestAttrs(r), "reason", decision.Reason, "identifier", email, "ip", ip)...)
		h.recordAuthAbuseEvent(r.Context(), authAbuseEventSendCodeBlocked, email, ip)
		_ = h.Queries.DeleteOldAuthAbuseEvents(r.Context())
		_ = h.Queries.DeleteExpiredVerificationCodes(r.Context())
		writeJSON(w, http.StatusOK, map[string]string{"message": "Verification code sent"})
		return
	}

	code, err := generateCode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate code")
		return
	}

	verificationCode, err := h.Queries.CreateVerificationCode(r.Context(), db.CreateVerificationCodeParams{
		Email:     email,
		Code:      code,
		ExpiresAt: pgtype.Timestamptz{Time: now.Add(10 * time.Minute), Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store verification code")
		return
	}

	if err := h.EmailService.SendVerificationCode(email, code); err != nil {
		_ = h.Queries.DeleteVerificationCodeByID(r.Context(), verificationCode.ID)
		writeError(w, http.StatusInternalServerError, "failed to send verification code")
		return
	}
	h.recordAuthAbuseEvent(r.Context(), authAbuseEventSendCodeRequested, email, ip)

	_ = h.Queries.DeleteOldAuthAbuseEvents(r.Context())
	_ = h.Queries.DeleteExpiredVerificationCodes(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{"message": "Verification code sent"})
}

func (h *Handler) VerifyCode(w http.ResponseWriter, r *http.Request) {
	var req VerifyCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	email := normalizeEmailIdentifier(req.Email)
	code := strings.TrimSpace(req.Code)
	if email == "" || code == "" {
		writeError(w, http.StatusBadRequest, "email and code are required")
		return
	}

	now := time.Now()
	ip := clientIP(r)
	decision := h.evaluateVerifyCodeGuard(r.Context(), email, ip, now)
	if !decision.Allow {
		slog.Warn("verify code blocked", append(logger.RequestAttrs(r), "reason", decision.Reason, "identifier", email, "ip", ip)...)
		h.recordAuthAbuseEvent(r.Context(), authAbuseEventVerifyCodeBlocked, email, ip)
		writeError(w, http.StatusBadRequest, "invalid or expired code")
		return
	}

	dbCode, err := h.Queries.GetLatestVerificationCode(r.Context(), email)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusBadRequest, "invalid or expired code")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to verify code")
		return
	}

	isMagicCodeEnabled := strings.EqualFold(strings.TrimSpace(os.Getenv("AUTH_ENABLE_MAGIC_CODE")), "true")
	isMasterCode := code == "888888" && isMagicCodeEnabled
	if !isMasterCode && subtle.ConstantTimeCompare([]byte(code), []byte(dbCode.Code)) != 1 {
		_ = h.Queries.IncrementVerificationCodeAttempts(r.Context(), dbCode.ID)
		h.recordAuthAbuseEvent(r.Context(), authAbuseEventVerifyCodeFailed, email, ip)
		writeError(w, http.StatusBadRequest, "invalid or expired code")
		return
	}

	if err := h.Queries.MarkVerificationCodeUsed(r.Context(), dbCode.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify code")
		return
	}

	user, err := h.findOrCreateUser(r.Context(), email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	if err := h.ensureUserWorkspace(r.Context(), user); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to provision workspace")
		return
	}

	tokenString, err := h.issueJWT(user)
	if err != nil {
		slog.Warn("login failed", append(logger.RequestAttrs(r), "error", err, "email", req.Email)...)
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	expiresAt := time.Now().Add(30 * 24 * time.Hour)
	middleware.SetAuthCookie(w, tokenString, expiresAt)
	if h.CFSigner != nil {
		for _, cookie := range h.CFSigner.SignedCookies(expiresAt) {
			http.SetCookie(w, cookie)
		}
	}

	slog.Info("user logged in", append(logger.RequestAttrs(r), "user_id", uuidToString(user.ID), "email", user.Email)...)
	writeJSON(w, http.StatusOK, LoginResponse{Token: tokenString, User: userToResponse(user)})
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}

	middleware.ClearAuthCookie(w)
	if h.CFSigner != nil {
		for _, cookie := range h.CFSigner.ClearCookies() {
			http.SetCookie(w, cookie)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	user, err := h.Queries.GetUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	writeJSON(w, http.StatusOK, userToResponse(user))
}

func (h *Handler) GetSessionToken(w http.ResponseWriter, r *http.Request) {
	token := middleware.JWTFromRequest(r)
	if strings.TrimSpace(token) == "" {
		writeError(w, http.StatusUnauthorized, "missing authorization header")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

type UpdateMeRequest struct {
	Name      *string `json:"name"`
	AvatarURL *string `json:"avatar_url"`
}

type GoogleLoginRequest struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}

type googleTokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
}

type googleUserInfo struct {
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

func (h *Handler) GoogleLogin(w http.ResponseWriter, r *http.Request) {
	var req GoogleLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Code == "" {
		writeError(w, http.StatusBadRequest, "code is required")
		return
	}

	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "Google login is not configured")
		return
	}

	redirectURI := req.RedirectURI
	if redirectURI == "" {
		redirectURI = os.Getenv("GOOGLE_REDIRECT_URI")
	}

	// Exchange authorization code for tokens.
	tokenResp, err := http.PostForm("https://oauth2.googleapis.com/token", url.Values{
		"code":          {req.Code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	})
	if err != nil {
		slog.Error("google oauth token exchange failed", "error", err)
		writeError(w, http.StatusBadGateway, "failed to exchange code with Google")
		return
	}
	defer tokenResp.Body.Close()

	tokenBody, err := io.ReadAll(tokenResp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to read Google token response")
		return
	}

	if tokenResp.StatusCode != http.StatusOK {
		slog.Error("google oauth token exchange returned error", "status", tokenResp.StatusCode, "body", string(tokenBody))
		writeError(w, http.StatusBadRequest, "failed to exchange code with Google")
		return
	}

	var gToken googleTokenResponse
	if err := json.Unmarshal(tokenBody, &gToken); err != nil {
		writeError(w, http.StatusBadGateway, "failed to parse Google token response")
		return
	}

	// Fetch user info from Google.
	userInfoReq, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	userInfoReq.Header.Set("Authorization", "Bearer "+gToken.AccessToken)

	userInfoResp, err := http.DefaultClient.Do(userInfoReq)
	if err != nil {
		slog.Error("google userinfo fetch failed", "error", err)
		writeError(w, http.StatusBadGateway, "failed to fetch user info from Google")
		return
	}
	defer userInfoResp.Body.Close()

	var gUser googleUserInfo
	if err := json.NewDecoder(userInfoResp.Body).Decode(&gUser); err != nil {
		writeError(w, http.StatusBadGateway, "failed to parse Google user info")
		return
	}

	if gUser.Email == "" {
		writeError(w, http.StatusBadRequest, "Google account has no email")
		return
	}

	email := strings.ToLower(strings.TrimSpace(gUser.Email))

	user, err := h.findOrCreateUser(r.Context(), email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	// Update name and avatar from Google profile if the user was just created
	// (default name is email prefix) or has no avatar yet.
	needsUpdate := false
	newName := user.Name
	newAvatar := user.AvatarUrl

	if gUser.Name != "" && user.Name == strings.Split(email, "@")[0] {
		newName = gUser.Name
		needsUpdate = true
	}
	if gUser.Picture != "" && !user.AvatarUrl.Valid {
		newAvatar = pgtype.Text{String: gUser.Picture, Valid: true}
		needsUpdate = true
	}

	if needsUpdate {
		updated, err := h.Queries.UpdateUser(r.Context(), db.UpdateUserParams{
			ID:        user.ID,
			Name:      newName,
			AvatarUrl: newAvatar,
		})
		if err == nil {
			user = updated
		}
	}

	if err := h.ensureUserWorkspace(r.Context(), user); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to provision workspace")
		return
	}

	tokenString, err := h.issueJWT(user)
	if err != nil {
		slog.Warn("google login failed", append(logger.RequestAttrs(r), "error", err, "email", email)...)
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	expiresAt := time.Now().Add(30 * 24 * time.Hour)
	middleware.SetAuthCookie(w, tokenString, expiresAt)
	if h.CFSigner != nil {
		for _, cookie := range h.CFSigner.SignedCookies(expiresAt) {
			http.SetCookie(w, cookie)
		}
	}

	slog.Info("user logged in via google", append(logger.RequestAttrs(r), "user_id", uuidToString(user.ID), "email", user.Email)...)
	writeJSON(w, http.StatusOK, LoginResponse{
		Token: tokenString,
		User:  userToResponse(user),
	})
}

func (h *Handler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req UpdateMeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	currentUser, err := h.Queries.GetUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	name := currentUser.Name
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
	}

	params := db.UpdateUserParams{
		ID:   currentUser.ID,
		Name: name,
	}
	if req.AvatarURL != nil {
		params.AvatarUrl = pgtype.Text{String: strings.TrimSpace(*req.AvatarURL), Valid: true}
	}

	updatedUser, err := h.Queries.UpdateUser(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update user")
		return
	}

	writeJSON(w, http.StatusOK, userToResponse(updatedUser))
}
