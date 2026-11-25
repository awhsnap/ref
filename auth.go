package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "ref_session"
	sessionTTL    = 30 * 24 * time.Hour
	bcryptCost    = 12
	minPassword   = 8
	maxPassword   = 72 // bcrypt only uses the first 72 bytes
)

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
	mux.HandleFunc("POST /auth/register", a.register)
	mux.HandleFunc("POST /auth/login", a.login)
	mux.HandleFunc("POST /auth/logout", a.logout)
	mux.HandleFunc("POST /api/password", a.withUser(a.changePassword))
	mux.HandleFunc("POST /api/team/{id}/reset-password", a.withUser(a.resetTeamPassword))
}

func (a *App) authConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"demo": a.demo})
}

func (a *App) setCookie(w http.ResponseWriter, name, value, path string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: path, MaxAge: maxAge,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

// ---- request protection ----

// protect rejects cross-site writes (CSRF): every POST/PATCH/PUT/DELETE to
// /api/ or /auth/ must come from this site and carry a JSON body type, which
// a plain HTML form on another site can't send.
func protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unsafe := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		guarded := strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/")
		if unsafe && guarded {
			if origin := r.Header.Get("Origin"); origin != "" {
				if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
					writeErr(w, httpError{http.StatusForbidden, "cross-site request blocked"})
					return
				}
			} else if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				writeErr(w, httpError{http.StatusForbidden, "cross-site request blocked"})
				return
			}
			if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				writeErr(w, httpError{http.StatusUnsupportedMediaType, "send JSON"})
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// limiter counts failed attempts per key in a sliding window.
type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLimiter() *limiter { return &limiter{hits: map[string][]time.Time{}} }

func (l *limiter) recent(key string, window time.Duration) []time.Time {
	cut := time.Now().Add(-window)
	h := l.hits[key]
	i := 0
	for i < len(h) && h[i].Before(cut) {
		i++
	}
	h = h[i:]
	if len(h) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = h
	}
	return h
}

// blocked reports whether key has max or more hits in window, and how long
// until the oldest one expires.
func (l *limiter) blocked(key string, max int, window time.Duration) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	h := l.recent(key, window)
	if len(h) < max {
		return false, 0
	}
	return true, time.Until(h[0].Add(window))
}

func (l *limiter) hit(key string) {
	l.mu.Lock()
	l.hits[key] = append(l.hits[key], time.Now())
	l.mu.Unlock()
}

func (l *limiter) clear(key string) {
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}

const (
	loginWindow     = 15 * time.Minute
	maxFailsPerUser = 8
	maxFailsPerIP   = 30
	maxSignupsPerIP = 10 // per hour
)

// clientIP prefers Fly's proxy header, which fly.io sets itself.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("Fly-Client-IP"); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func tooMany(wait time.Duration) error {
	mins := int(wait.Minutes()) + 1
	return httpError{http.StatusTooManyRequests, fmt.Sprintf("Too many attempts. Try again in %d minute%s.", mins, map[bool]string{true: "", false: "s"}[mins == 1])}
}

// ---- passwords ----

func checkPasswordRules(pw string) error {
	if len(pw) < minPassword {
		return httpError{http.StatusBadRequest, fmt.Sprintf("Use at least %d characters for your password.", minPassword)}
	}
	if len(pw) > maxPassword {
		return httpError{http.StatusBadRequest, fmt.Sprintf("Use at most %d characters for your password.", maxPassword)}
	}
	return nil
}

func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	return string(b), err
}

// dummyHash is compared against when an email isn't found, so a wrong email
// takes as long as a wrong password.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), bcryptCost)

func normalizeEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s || len(s) > 254 {
		return "", httpError{http.StatusBadRequest, "Enter a valid email address."}
	}
	return s, nil
}

// tempPassword makes an easy-to-read one-time password.
func tempPassword() string {
	const letters = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b[:4]) + "-" + string(b[4:8]) + "-" + string(b[8:])
}

// ---- register, login ----

func (a *App) register(w http.ResponseWriter, r *http.Request) {
	if err := a.doRegister(w, r); err != nil {
		writeErr(w, err)
	}
}

func (a *App) doRegister(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	ipKey := "signup:" + clientIP(r)
	if no, wait := a.limits.blocked(ipKey, maxSignupsPerIP, time.Hour); no {
		return tooMany(wait)
	}
	email, err := normalizeEmail(in.Email)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 80 {
		return httpError{http.StatusBadRequest, "Enter your name."}
	}
	if err := checkPasswordRules(in.Password); err != nil {
		return err
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		return err
	}
	res, err := a.db.Exec(`INSERT INTO users(name, role, email, password_hash, onboarded) VALUES (?, 'solo', ?, ?, 0)`, name, email, hash)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return httpError{http.StatusConflict, "An account with this email already exists. Try logging in."}
		}
		return err
	}
	a.limits.hit(ipKey)
	uid, _ := res.LastInsertId()
	if err := a.startSession(w, uid); err != nil {
		return err
	}
	u, err := loadUser(a.db, uid)
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, u)
	return nil
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if err := a.doLogin(w, r); err != nil {
		writeErr(w, err)
	}
}

func (a *App) doLogin(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	ipKey, userKey := "login-ip:"+clientIP(r), "login-user:"+email
	if no, wait := a.limits.blocked(ipKey, maxFailsPerIP, loginWindow); no {
		return tooMany(wait)
	}
	if no, wait := a.limits.blocked(userKey, maxFailsPerUser, loginWindow); no {
		return tooMany(wait)
	}
	var id int64
	var hash string
	err := a.db.QueryRow(`SELECT id, password_hash FROM users WHERE lower(email) = ? AND is_demo = 0 AND password_hash <> ''`, email).Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		hash = string(dummyHash)
	} else if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil || id == 0 {
		a.limits.hit(ipKey)
		a.limits.hit(userKey)
		return httpError{http.StatusUnauthorized, "Email or password is incorrect."}
	}
	a.limits.clear(userKey)
	if err := a.startSession(w, id); err != nil {
		return err
	}
	u, err := loadUser(a.db, id)
	if err != nil {
		return err
	}
	writeJSON(w, u)
	return nil
}

// ---- password change and reset ----

// changePassword sets a new password. The current one is needed unless the
// account is on a temporary password.
func (a *App) changePassword(w http.ResponseWriter, r *http.Request, u User) error {
	if u.IsDemo {
		return httpError{http.StatusForbidden, "Sample accounts don't have passwords."}
	}
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if !u.MustChangePW {
		key := "pwchange:" + strconv.FormatInt(u.ID, 10)
		if no, wait := a.limits.blocked(key, maxFailsPerUser, loginWindow); no {
			return tooMany(wait)
		}
		var hash string
		if err := a.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, u.ID).Scan(&hash); err != nil {
			return err
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Current)) != nil {
			a.limits.hit(key)
			return httpError{http.StatusBadRequest, "Your current password is incorrect."}
		}
	}
	if err := checkPasswordRules(in.New); err != nil {
		return err
	}
	hash, err := hashPassword(in.New)
	if err != nil {
		return err
	}
	if _, err := a.db.Exec(`UPDATE users SET password_hash = ?, must_change_password = 0 WHERE id = ?`, hash, u.ID); err != nil {
		return err
	}
	// Sign out everywhere else.
	c, _ := r.Cookie(sessionCookie)
	keep := ""
	if c != nil {
		keep = hashToken(c.Value)
	}
	if _, err := a.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, u.ID, keep); err != nil {
		return err
	}
	nu, err := loadUser(a.db, u.ID)
	if err != nil {
		return err
	}
	writeJSON(w, nu)
	return nil
}

// setTempPassword gives an account a one-time password that must be changed
// at next sign-in, and signs it out everywhere.
func setTempPassword(db *sql.DB, uid int64) (string, error) {
	pw := tempPassword()
	hash, err := hashPassword(pw)
	if err != nil {
		return "", err
	}
	if _, err := db.Exec(`UPDATE users SET password_hash = ?, must_change_password = 1 WHERE id = ?`, hash, uid); err != nil {
		return "", err
	}
	if _, err := db.Exec(`DELETE FROM sessions WHERE user_id = ?`, uid); err != nil {
		return "", err
	}
	return pw, nil
}

// resetTeamPassword lets a dealership manager reset a salesperson's password.
func (a *App) resetTeamPassword(w http.ResponseWriter, r *http.Request, u User) error {
	if u.Role != "manager" || u.IsDemo {
		return httpError{http.StatusForbidden, "Only a dealership manager can reset passwords."}
	}
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if id == u.ID {
		return httpError{http.StatusBadRequest, "Change your own password in Settings."}
	}
	var dealer sql.NullInt64
	var demo bool
	if err := a.db.QueryRow(`SELECT dealership_id, is_demo FROM users WHERE id = ?`, id).Scan(&dealer, &demo); err != nil {
		return err
	}
	if demo || !dealer.Valid || dealer != u.DealershipID {
		return httpError{http.StatusNotFound, "not found"}
	}
	pw, err := setTempPassword(a.db, id)
	if err != nil {
		return err
	}
	writeJSON(w, map[string]string{"temp_password": pw})
	return nil
}

// resetPasswordCLI is the admin fallback for accounts with no manager:
//
//	fly ssh console -C "ref reset-password someone@example.com"
func resetPasswordCLI(db *sql.DB, email string) (string, error) {
	var id int64
	err := db.QueryRow(`SELECT id FROM users WHERE lower(email) = lower(?) AND is_demo = 0`, strings.TrimSpace(email)).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("no account with email %q", email)
	}
	return setTempPassword(db, id)
}

// ---- sessions ----

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
