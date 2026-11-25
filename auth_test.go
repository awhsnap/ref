package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestApp(t *testing.T, demo bool) (*httptest.Server, *App) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	app := &App{db: db, demo: demo, limits: newLimiter()}
	mux := http.NewServeMux()
	app.routes(mux)
	srv := httptest.NewServer(protect(mux))
	t.Cleanup(srv.Close)
	app.baseURL = srv.URL
	if demo {
		if err := seed(db, srv.URL); err != nil {
			t.Fatal(err)
		}
	}
	return srv, app
}

func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

// call sends a same-site JSON request, like the app's own front end.
func call(t *testing.T, c *http.Client, method, url, body string, header ...string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(res.Body)
	json.Unmarshal(b, &out)
	return res.StatusCode, out
}

func register(t *testing.T, srv *httptest.Server, email, pw string) *http.Client {
	t.Helper()
	c := newClient()
	code, out := call(t, c, "POST", srv.URL+"/auth/register", `{"name":"Test Seller","email":"`+email+`","password":"`+pw+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("register %s: %d %v", email, code, out)
	}
	return c
}

func TestRegisterLoginAndDealershipOnboarding(t *testing.T) {
	srv, app := newTestApp(t, true)

	// Manager registers and creates a dealership.
	mgr := register(t, srv, "Mgr@Example.com", "correct horse")
	code, me := call(t, mgr, "GET", srv.URL+"/api/me", "")
	if code != 200 || me["onboarded"] != false || me["demo"] != false || me["email"] != "mgr@example.com" {
		t.Fatalf("new user: %d %v", code, me)
	}
	if code, _ := call(t, mgr, "GET", srv.URL+"/api/customers", ""); code != http.StatusForbidden {
		t.Fatalf("un-onboarded user reached customers: %d", code)
	}
	code, me = call(t, mgr, "POST", srv.URL+"/api/onboarding", `{"plan":"dealership","dealership_name":"Test Motors"}`)
	invite, _ := me["invite_code"].(string)
	if code != 200 || me["role"] != "manager" || len(invite) != 8 {
		t.Fatalf("onboard manager: %d %v", code, me)
	}

	// Same email can't register twice, in any letter case.
	if code, _ := call(t, newClient(), "POST", srv.URL+"/auth/register", `{"name":"X","email":"mgr@EXAMPLE.com","password":"another one"}`); code != http.StatusConflict {
		t.Fatalf("duplicate email: %d", code)
	}
	// A sample account's email doesn't block a real sign-up.
	register(t, srv, "alex@example.com", "password1")

	// Salesperson joins with the invite code and adds a customer.
	rep := register(t, srv, "rep@example.com", "rep password")
	if code, _ := call(t, rep, "POST", srv.URL+"/api/onboarding", `{"plan":"join","invite_code":"WRONGONE"}`); code != 400 {
		t.Fatalf("bad invite accepted: %d", code)
	}
	code, me = call(t, rep, "POST", srv.URL+"/api/onboarding", `{"plan":"join","invite_code":"`+strings.ToLower(invite)+`"}`)
	if code != 200 || me["role"] != "sales" || me["dealership"] != "Test Motors" || me["invite_code"] != nil {
		t.Fatalf("join: %d %v", code, me)
	}
	if code, out := call(t, rep, "POST", srv.URL+"/api/customers", `{"first_name":"Pat","phone":"+15550001111","sms_opt_in":true}`); code != 201 {
		t.Fatalf("create customer: %d %v", code, out)
	}

	// Log out, then back in.
	call(t, mgr, "POST", srv.URL+"/auth/logout", "")
	if code, _ := call(t, mgr, "GET", srv.URL+"/api/me", ""); code != 401 {
		t.Fatalf("after logout: %d", code)
	}
	if code, _ := call(t, mgr, "POST", srv.URL+"/auth/login", `{"email":"mgr@example.com","password":"wrong pass"}`); code != 401 {
		t.Fatalf("wrong password: %d", code)
	}
	if code, me := call(t, mgr, "POST", srv.URL+"/auth/login", `{"email":" MGR@example.com ","password":"correct horse"}`); code != 200 || me["role"] != "manager" {
		t.Fatalf("login: %d %v", code, me)
	}

	// Manager resets the salesperson's password; the old session ends and
	// the temporary password must be changed before anything else.
	var repID string
	app.db.QueryRow(`SELECT id FROM users WHERE email = 'rep@example.com'`).Scan(&repID)
	code, out := call(t, mgr, "POST", srv.URL+"/api/team/"+repID+"/reset-password", "")
	temp, _ := out["temp_password"].(string)
	if code != 200 || len(temp) != 14 {
		t.Fatalf("reset: %d %v", code, out)
	}
	if code, _ := call(t, rep, "GET", srv.URL+"/api/me", ""); code != 401 {
		t.Fatalf("old session survived reset: %d", code)
	}
	rep2 := newClient()
	if code, me := call(t, rep2, "POST", srv.URL+"/auth/login", `{"email":"rep@example.com","password":"`+temp+`"}`); code != 200 || me["must_change_password"] != true {
		t.Fatalf("login with temp: %d %v", code, me)
	}
	if code, _ := call(t, rep2, "GET", srv.URL+"/api/customers", ""); code != http.StatusForbidden {
		t.Fatalf("temp password reached customers: %d", code)
	}
	if code, me := call(t, rep2, "POST", srv.URL+"/api/password", `{"new":"brand new pw"}`); code != 200 || me["must_change_password"] != false {
		t.Fatalf("change after reset: %d %v", code, me)
	}
	if code, _ := call(t, rep2, "GET", srv.URL+"/api/customers", ""); code != 200 {
		t.Fatalf("after change: %d", code)
	}

	// Changing a password needs the current one.
	if code, _ := call(t, rep2, "POST", srv.URL+"/api/password", `{"current":"nope nope","new":"another pw1"}`); code != 400 {
		t.Fatalf("change without current: %d", code)
	}

	// A salesperson can't reset anyone; a manager can't reset outside their team.
	var mgrID, alexID string
	app.db.QueryRow(`SELECT id FROM users WHERE email = 'mgr@example.com'`).Scan(&mgrID)
	app.db.QueryRow(`SELECT id FROM users WHERE email = 'alex@example.com' AND is_demo = 0`).Scan(&alexID)
	if code, _ := call(t, rep2, "POST", srv.URL+"/api/team/"+mgrID+"/reset-password", ""); code != 403 {
		t.Fatalf("salesperson reset: %d", code)
	}
	if code, _ := call(t, mgr, "POST", srv.URL+"/api/team/"+alexID+"/reset-password", ""); code != 404 {
		t.Fatalf("reset outside team: %d", code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	srv, _ := newTestApp(t, false)
	register(t, srv, "a@example.com", "right password")
	c := newClient()
	for i := 0; i < maxFailsPerUser; i++ {
		if code, _ := call(t, c, "POST", srv.URL+"/auth/login", `{"email":"a@example.com","password":"wrong"}`); code != 401 {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	// Even the right password is refused while locked.
	if code, out := call(t, c, "POST", srv.URL+"/auth/login", `{"email":"a@example.com","password":"right password"}`); code != 429 {
		t.Fatalf("not rate limited: %d %v", code, out)
	}
	// Other accounts are unaffected.
	register(t, srv, "b@example.com", "b password")
	if code, _ := call(t, c, "POST", srv.URL+"/auth/login", `{"email":"b@example.com","password":"b password"}`); code != 200 {
		t.Fatalf("other account blocked: %d", code)
	}
}

func TestPasswordAndEmailRules(t *testing.T) {
	srv, _ := newTestApp(t, false)
	for _, body := range []string{
		`{"name":"X","email":"a@example.com","password":"short"}`,
		`{"name":"X","email":"not-an-email","password":"long enough"}`,
		`{"name":"","email":"a@example.com","password":"long enough"}`,
		`{"name":"X","email":"a@example.com","password":"` + strings.Repeat("x", 73) + `"}`,
	} {
		if code, _ := call(t, newClient(), "POST", srv.URL+"/auth/register", body); code != 400 {
			t.Errorf("%s accepted: %d", body, code)
		}
	}
}

func TestCrossSiteRequestsBlocked(t *testing.T) {
	srv, _ := newTestApp(t, false)
	c := register(t, srv, "a@example.com", "long enough")
	if code, _ := call(t, c, "POST", srv.URL+"/api/onboarding", `{"plan":"solo"}`, "Origin", "https://evil.example"); code != 403 {
		t.Fatalf("foreign Origin allowed: %d", code)
	}
	if code, _ := call(t, c, "POST", srv.URL+"/api/onboarding", `{"plan":"solo"}`, "Sec-Fetch-Site", "cross-site"); code != 403 {
		t.Fatalf("cross-site fetch allowed: %d", code)
	}
	// A plain HTML form post can't set a JSON content type.
	req, _ := http.NewRequest("POST", srv.URL+"/api/onboarding", strings.NewReader("plan=solo"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("form post allowed: %d", res.StatusCode)
	}
	if code, _ := call(t, c, "POST", srv.URL+"/api/onboarding", `{"plan":"solo"}`, "Origin", srv.URL); code != 200 {
		t.Fatalf("same-site request refused: %d", code)
	}
}

func TestSessionCookieFlags(t *testing.T) {
	srv, app := newTestApp(t, false)
	app.secure = true
	req, _ := http.NewRequest("POST", srv.URL+"/auth/register", strings.NewReader(`{"name":"X","email":"a@example.com","password":"long enough"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var token string
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			token = c.Value
			if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || len(c.Value) < 40 {
				t.Fatalf("weak cookie: %+v", c)
			}
		}
	}
	if token == "" {
		t.Fatal("no session cookie")
	}
	var n int
	app.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token_hash = ?`, token).Scan(&n)
	if n != 0 {
		t.Fatal("raw session token stored in the database")
	}
}

func TestDemoHeaderCannotReachRealAccounts(t *testing.T) {
	srv, app := newTestApp(t, true)
	register(t, srv, "x@example.com", "long enough")
	var realID string
	app.db.QueryRow(`SELECT id FROM users WHERE email = 'x@example.com'`).Scan(&realID)

	anon := &http.Client{}
	if code, _ := call(t, anon, "GET", srv.URL+"/api/me", "", "X-User", realID); code != 401 {
		t.Fatalf("X-User reached a real account: %d", code)
	}
	if code, me := call(t, anon, "GET", srv.URL+"/api/me", "", "X-User", "1"); code != 200 || me["demo"] != true {
		t.Fatalf("demo account: %d %v", code, me)
	}
	// Sample accounts have no password, so they can't be logged into.
	if code, _ := call(t, newClient(), "POST", srv.URL+"/auth/login", `{"email":"alex@example.com","password":""}`); code != 401 {
		t.Fatalf("logged into a sample account: %d", code)
	}
}

func TestDemoOffRequiresSignIn(t *testing.T) {
	srv, _ := newTestApp(t, false)
	if code, _ := call(t, &http.Client{}, "GET", srv.URL+"/api/me", "", "X-User", "1"); code != 401 {
		t.Fatalf("demo off but header accepted: %d", code)
	}
}

func TestResetPasswordCLI(t *testing.T) {
	srv, app := newTestApp(t, false)
	register(t, srv, "solo@example.com", "long enough")
	pw, err := resetPasswordCLI(app.db, "SOLO@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if code, me := call(t, newClient(), "POST", srv.URL+"/auth/login", `{"email":"solo@example.com","password":"`+pw+`"}`); code != 200 || me["must_change_password"] != true {
		t.Fatalf("login with CLI temp password: %d %v", code, me)
	}
	if _, err := resetPasswordCLI(app.db, "nobody@example.com"); err == nil {
		t.Fatal("reset of unknown email succeeded")
	}
}
