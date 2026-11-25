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
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	sessionCookie = "ref_session"
	stateCookie   = "ref_oauth"
	sessionTTL    = 30 * 24 * time.Hour
)

// authProvider is an OpenID Connect login provider (Auth0).
type authProvider struct {
	oauth       *oauth2.Config
	userInfoURL string
	logoutURL   string // provider logout endpoint, without query
}

// newAuth0 returns nil when Auth0 isn't configured.
func newAuth0(baseURL string) *authProvider {
	domain := strings.TrimSuffix(strings.TrimPrefix(env("AUTH0_DOMAIN", ""), "https://"), "/")
	id, secret := env("AUTH0_CLIENT_ID", ""), env("AUTH0_CLIENT_SECRET", "")
	if domain == "" || id == "" || secret == "" {
		return nil
	}
	issuer := "https://" + domain
	return &authProvider{
		oauth: &oauth2.Config{
			ClientID:     id,
			ClientSecret: secret,
			RedirectURL:  strings.TrimRight(baseURL, "/") + "/auth/callback",
			Scopes:       []string{"openid", "email", "profile"},
			Endpoint: oauth2.Endpoint{
				AuthURL:   issuer + "/authorize",
				TokenURL:  issuer + "/oauth/token",
				AuthStyle: oauth2.AuthStyleInParams,
			},
		},
		userInfoURL: issuer + "/userinfo",
		logoutURL:   issuer + "/v2/logout",
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
	mux.HandleFunc("GET /auth/login", a.login)
	mux.HandleFunc("GET /auth/callback", a.callback)
	mux.HandleFunc("POST /auth/logout", a.logout)
}

func (a *App) authConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"auth": a.auth != nil, "demo": a.demo})
}

func (a *App) setCookie(w http.ResponseWriter, name, value, path string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: path, MaxAge: maxAge,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

// login sends the browser to Auth0's hosted login page. ?signup=1 opens it on
// the sign-up tab.
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil {
		http.Error(w, "Sign-in is not configured on this server.", http.StatusServiceUnavailable)
		return
	}
	state, verifier := randomToken(24), oauth2.GenerateVerifier()
	a.setCookie(w, stateCookie, state+"."+verifier, "/auth", 600)
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	if r.URL.Query().Get("signup") == "1" {
		opts = append(opts, oauth2.SetAuthURLParam("screen_hint", "signup"))
	}
	http.Redirect(w, r, a.auth.oauth.AuthCodeURL(state, opts...), http.StatusFound)
}

type profile struct {
	Sub     string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

func (a *App) callback(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string, err error) {
		if err != nil {
			log.Printf("auth callback: %s: %v", msg, err)
		}
		http.Redirect(w, r, "/?auth_error="+msg, http.StatusFound)
	}
	if a.auth == nil {
		fail("not_configured", nil)
		return
	}
	c, err := r.Cookie(stateCookie)
	a.setCookie(w, stateCookie, "", "/auth", -1)
	if err != nil {
		fail("expired", err)
		return
	}
	state, verifier, ok := strings.Cut(c.Value, ".")
	if !ok || state == "" || r.URL.Query().Get("state") != state {
		fail("state", nil)
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		// e.g. access_denied when the user cancels or a rule blocks them.
		fail("cancelled", errors.New(e+": "+r.URL.Query().Get("error_description")))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tok, err := a.auth.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		fail("exchange", err)
		return
	}
	p, err := fetchProfile(ctx, a.auth.oauth.Client(ctx, tok), a.auth.userInfoURL)
	if err != nil || p.Sub == "" {
		fail("profile", err)
		return
	}
	uid, err := a.upsertUser(p)
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

func fetchProfile(ctx context.Context, client *http.Client, u string) (profile, error) {
	var p profile
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	res, err := client.Do(req)
	if err != nil {
		return p, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return p, fmt.Errorf("userinfo status %d", res.StatusCode)
	}
	err = json.NewDecoder(res.Body).Decode(&p)
	return p, err
}

// upsertUser finds the account for an Auth0 user id ("sub"), creating it on
// first sign-in. New accounts start un-onboarded so they pick a plan.
func (a *App) upsertUser(p profile) (int64, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" || name == p.Email {
		name, _, _ = strings.Cut(p.Email, "@")
	}
	var id int64
	err := a.db.QueryRow(`SELECT id FROM users WHERE auth_sub = ?`, p.Sub).Scan(&id)
	switch {
	case err == nil:
		_, err = a.db.Exec(`UPDATE users SET email = ?, picture = ? WHERE id = ?`, p.Email, p.Picture, id)
		return id, err
	case errors.Is(err, sql.ErrNoRows):
		res, err := a.db.Exec(`INSERT INTO users(name, role, email, auth_sub, picture, onboarded) VALUES (?, 'solo', ?, ?, ?, 0)`,
			name, p.Email, p.Sub, p.Picture)
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
	// Also end the Auth0 session, so the next login asks again.
	out := map[string]string{"logout_url": "/"}
	if a.auth != nil {
		out["logout_url"] = a.auth.logoutURL + "?" + url.Values{
			"client_id": {a.auth.oauth.ClientID},
			"returnTo":  {strings.TrimRight(a.baseURL, "/") + "/"},
		}.Encode()
	}
	writeJSON(w, out)
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
