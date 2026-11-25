package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	sessionCookie = "ref_session"
	stateCookie   = "ref_oauth"
	sessionTTL    = 30 * 24 * time.Hour
)

// userInfoURL is Google's OpenID Connect profile endpoint (a var for tests).
var userInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"

// newGoogleConfig returns nil when Google sign-in isn't configured.
func newGoogleConfig(baseURL string) *oauth2.Config {
	id, secret := env("GOOGLE_CLIENT_ID", ""), env("GOOGLE_CLIENT_SECRET", "")
	if id == "" || secret == "" {
		return nil
	}
	return &oauth2.Config{
		ClientID:     id,
		ClientSecret: secret,
		RedirectURL:  strings.TrimRight(baseURL, "/") + "/auth/google/callback",
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     google.Endpoint,
	}
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (a *App) authRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/config", a.authConfig)
	mux.HandleFunc("GET /auth/google/login", a.googleLogin)
	mux.HandleFunc("GET /auth/google/callback", a.googleCallback)
	mux.HandleFunc("POST /auth/logout", a.logout)
}

func (a *App) authConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"google": a.google != nil, "demo": a.demo})
}

func (a *App) setCookie(w http.ResponseWriter, name, value, path string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: path, MaxAge: maxAge,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) googleLogin(w http.ResponseWriter, r *http.Request) {
	if a.google == nil {
		http.Error(w, "Google sign-in is not configured on this server.", http.StatusServiceUnavailable)
		return
	}
	state, verifier := randomToken(24), oauth2.GenerateVerifier()
	a.setCookie(w, stateCookie, state+"."+verifier, "/auth/google", 600)
	url := a.google.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("prompt", "select_account"))
	http.Redirect(w, r, url, http.StatusFound)
}

type googleUser struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

func (a *App) googleCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string, err error) {
		if err != nil {
			log.Printf("google callback: %s: %v", msg, err)
		}
		http.Redirect(w, r, "/?auth_error="+msg, http.StatusFound)
	}
	if a.google == nil {
		fail("not_configured", nil)
		return
	}
	c, err := r.Cookie(stateCookie)
	a.setCookie(w, stateCookie, "", "/auth/google", -1)
	if err != nil {
		fail("expired", err)
		return
	}
	state, verifier, ok := strings.Cut(c.Value, ".")
	if !ok || state == "" || r.URL.Query().Get("state") != state {
		fail("state", nil)
		return
	}
	if r.URL.Query().Get("error") != "" {
		fail("cancelled", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tok, err := a.google.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		fail("exchange", err)
		return
	}
	gu, err := fetchGoogleUser(ctx, a.google.Client(ctx, tok))
	if err != nil {
		fail("profile", err)
		return
	}
	if gu.Sub == "" || !gu.EmailVerified {
		fail("unverified", nil)
		return
	}
	uid, err := a.upsertGoogleUser(gu)
	if err != nil {
		fail("account", err)
		return
	}
	if err := a.startSession(w, uid); err != nil {
		fail("session", err)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func fetchGoogleUser(ctx context.Context, client *http.Client) (googleUser, error) {
	var gu googleUser
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, nil)
	res, err := client.Do(req)
	if err != nil {
		return gu, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return gu, fmt.Errorf("userinfo status %d", res.StatusCode)
	}
	err = json.NewDecoder(res.Body).Decode(&gu)
	return gu, err
}

// upsertGoogleUser finds the account for a Google identity, creating it on
// first sign-in. New accounts start un-onboarded so they pick a plan.
func (a *App) upsertGoogleUser(gu googleUser) (int64, error) {
	name := strings.TrimSpace(gu.Name)
	if name == "" {
		name, _, _ = strings.Cut(gu.Email, "@")
	}
	var id int64
	err := a.db.QueryRow(`SELECT id FROM users WHERE google_sub = ?`, gu.Sub).Scan(&id)
	switch {
	case err == nil:
		_, err = a.db.Exec(`UPDATE users SET email = ?, picture = ? WHERE id = ?`, gu.Email, gu.Picture, id)
		return id, err
	case errors.Is(err, sql.ErrNoRows):
		res, err := a.db.Exec(`INSERT INTO users(name, role, email, google_sub, picture, onboarded) VALUES (?, 'solo', ?, ?, ?, 0)`,
			name, gu.Email, gu.Sub, gu.Picture)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	default:
		return 0, err
	}
}

func (a *App) startSession(w http.ResponseWriter, uid int64) error {
	tok := randomToken(32)
	exp := time.Now().Add(sessionTTL)
	if _, err := a.db.Exec(`INSERT INTO sessions(token_hash, user_id, expires_at) VALUES (?,?,?)`,
		hashToken(tok), uid, exp.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	a.setCookie(w, sessionCookie, tok, "/", int(sessionTTL.Seconds()))
	return nil
}

// sessionUserID returns the signed-in user's id, or 0.
func (a *App) sessionUserID(r *http.Request) int64 {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return 0
	}
	var id int64
	err = a.db.QueryRow(`SELECT user_id FROM sessions WHERE token_hash = ? AND expires_at > ?`,
		hashToken(c.Value), time.Now().UTC().Format(time.RFC3339)).Scan(&id)
	if err != nil {
		return 0
	}
	return id
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(c.Value))
	}
	a.setCookie(w, sessionCookie, "", "/", -1)
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) cleanSessions() {
	a.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC().Format(time.RFC3339))
}

// ---- onboarding ----

func (a *App) onboard(w http.ResponseWriter, r *http.Request, u User) error {
	if u.Onboarded {
		return httpError{http.StatusConflict, "account is already set up"}
	}
	var in struct {
		Plan           string `json:"plan"` // solo, dealership, join
		Name           string `json:"name"`
		Phone          string `json:"phone"`
		Workplace      string `json:"workplace"`
		DealershipName string `json:"dealership_name"`
		InviteCode     string `json:"invite_code"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	clip := func(s string, n int) string {
		s = strings.TrimSpace(s)
		if len(s) > n {
			s = s[:n]
		}
		return s
	}
	name := clip(in.Name, 80)
	if name == "" {
		name = u.Name
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	switch in.Plan {
	case "solo":
		_, err = tx.Exec(`UPDATE users SET name=?, phone=?, role='solo', workplace=?, dealership_id=NULL, onboarded=1 WHERE id=?`,
			name, clip(in.Phone, 30), clip(in.Workplace, 80), u.ID)
	case "dealership":
		dn := clip(in.DealershipName, 80)
		if dn == "" {
			return httpError{http.StatusBadRequest, "enter the dealership's name"}
		}
		var res sql.Result
		res, err = tx.Exec(`INSERT INTO dealerships(name, invite_code) VALUES (?,?)`, dn, inviteCode())
		if err != nil {
			return err
		}
		did, _ := res.LastInsertId()
		_, err = tx.Exec(`UPDATE users SET name=?, phone=?, role='manager', dealership_id=?, onboarded=1 WHERE id=?`, name, clip(in.Phone, 30), did, u.ID)
	case "join":
		code := strings.ToUpper(strings.TrimSpace(in.InviteCode))
		var did int64
		if code == "" || tx.QueryRow(`SELECT id FROM dealerships WHERE invite_code = ? AND invite_code <> ''`, code).Scan(&did) != nil {
			return httpError{http.StatusBadRequest, "that invite code didn't match a dealership"}
		}
		_, err = tx.Exec(`UPDATE users SET name=?, phone=?, role='sales', dealership_id=?, onboarded=1 WHERE id=?`, name, clip(in.Phone, 30), did, u.ID)
	default:
		return httpError{http.StatusBadRequest, "pick a plan"}
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	nu, err := loadUser(a.db, u.ID)
	if err != nil {
		return err
	}
	writeJSON(w, nu)
	return nil
}

func inviteCode() string {
	const letters = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}
