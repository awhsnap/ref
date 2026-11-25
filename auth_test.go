package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeAuth0 stands in for Auth0's authorize, token and userinfo endpoints.
func fakeAuth0(t *testing.T, sub, email string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge") == "" {
			t.Error("missing PKCE challenge")
		}
		if q.Get("screen_hint") != "signup" {
			t.Error("missing sign-up hint")
		}
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=good&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("code") != "good" || r.Form.Get("code_verifier") == "" {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"tok","token_type":"Bearer","expires_in":3600}`)
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"sub": sub, "email": email, "name": "Test Seller"})
	})
	return httptest.NewServer(mux)
}

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
	app := &App{db: db, demo: demo}
	mux := http.NewServeMux()
	app.routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	app.baseURL = srv.URL
	if demo {
		if err := seed(db, srv.URL); err != nil {
			t.Fatal(err)
		}
	}
	return srv, app
}

func signIn(t *testing.T, srv *httptest.Server, app *App, sub, email string) *http.Client {
	t.Helper()
	g := fakeAuth0(t, sub, email)
	t.Cleanup(g.Close)
	t.Setenv("AUTH0_DOMAIN", strings.TrimPrefix(g.URL, "http://"))
	t.Setenv("AUTH0_CLIENT_ID", "id")
	t.Setenv("AUTH0_CLIENT_SECRET", "secret")
	app.auth = newAuth0(srv.URL)
	// The fake server speaks plain HTTP.
	for _, u := range []*string{&app.auth.oauth.Endpoint.AuthURL, &app.auth.oauth.Endpoint.TokenURL, &app.auth.userInfoURL} {
		*u = strings.Replace(*u, "https://", "http://", 1)
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	res, err := c.Get(srv.URL + "/auth/login?signup=1")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if strings.Contains(res.Request.URL.RawQuery, "auth_error") {
		t.Fatalf("sign-in failed: %s", res.Request.URL)
	}
	return c
}

func call(t *testing.T, c *http.Client, method, url, body string, header ...string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
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

func TestSignInAndDealershipOnboarding(t *testing.T) {
	srv, app := newTestApp(t, true)

	// Manager signs up and creates a dealership.
	mgr := signIn(t, srv, app, "auth0|mgr", "mgr@example.com")
	code, me := call(t, mgr, "GET", srv.URL+"/api/me", "")
	if code != 200 || me["onboarded"] != false || me["demo"] != false {
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

	// Salesperson joins with the invite code and adds a customer.
	rep := signIn(t, srv, app, "google-oauth2|rep", "rep@example.com")
	if code, _ := call(t, rep, "POST", srv.URL+"/api/onboarding", `{"plan":"join","invite_code":"WRONGONE"}`); code != 400 {
		t.Fatalf("bad invite accepted: %d", code)
	}
	code, me = call(t, rep, "POST", srv.URL+"/api/onboarding", `{"plan":"join","invite_code":"`+strings.ToLower(invite)+`"}`)
	if code != 200 || me["role"] != "sales" || me["dealership"] != "Test Motors" || me["invite_code"] != nil {
		t.Fatalf("join: %d %v", code, me)
	}
	code, cust := call(t, rep, "POST", srv.URL+"/api/customers", `{"first_name":"Pat","phone":"+15550001111","sms_opt_in":true}`)
	if code != 201 {
		t.Fatalf("create customer: %d %v", code, cust)
	}

	// The manager sees the team; sample (demo) data stays separate.
	req, _ := http.NewRequest("GET", srv.URL+"/api/team", nil)
	res, _ := mgr.Do(req)
	var team []map[string]any
	json.NewDecoder(res.Body).Decode(&team)
	res.Body.Close()
	if len(team) != 2 {
		t.Fatalf("team = %v", team)
	}

	// Signing in again reuses the same account.
	again := signIn(t, srv, app, "auth0|mgr", "mgr@example.com")
	if _, me := call(t, again, "GET", srv.URL+"/api/me", ""); me["role"] != "manager" {
		t.Fatalf("second sign-in got %v", me)
	}

	// Sign out ends the session and hands back Auth0's logout URL.
	_, out := call(t, mgr, "POST", srv.URL+"/auth/logout", "")
	if lu, _ := out["logout_url"].(string); !strings.Contains(lu, "/v2/logout?") || !strings.Contains(lu, "returnTo=") {
		t.Fatalf("logout_url = %q", lu)
	}
	if code, _ := call(t, mgr, "GET", srv.URL+"/api/me", ""); code != 401 {
		t.Fatalf("after logout: %d", code)
	}
}

func TestDemoHeaderCannotReachRealAccounts(t *testing.T) {
	srv, app := newTestApp(t, true)
	signIn(t, srv, app, "auth0|x", "x@example.com")
	var realID string
	app.db.QueryRow(`SELECT id FROM users WHERE auth_sub = 'auth0|x'`).Scan(&realID)

	anon := &http.Client{}
	if code, _ := call(t, anon, "GET", srv.URL+"/api/me", "", "X-User", realID); code != 401 {
		t.Fatalf("X-User reached a real account: %d", code)
	}
	if code, me := call(t, anon, "GET", srv.URL+"/api/me", "", "X-User", "1"); code != 200 || me["demo"] != true {
		t.Fatalf("demo account: %d %v", code, me)
	}
	if code, _ := call(t, anon, "GET", srv.URL+"/api/me", ""); code != 401 {
		t.Fatalf("no session, no header: %d", code)
	}
}

func TestDemoOffRequiresSignIn(t *testing.T) {
	srv, _ := newTestApp(t, false)
	if code, _ := call(t, &http.Client{}, "GET", srv.URL+"/api/me", "", "X-User", "1"); code != 401 {
		t.Fatalf("demo off but header accepted: %d", code)
	}
}

func TestBadStateIsRejected(t *testing.T) {
	srv, app := newTestApp(t, false)
	t.Setenv("AUTH0_DOMAIN", "example.auth0.com")
	t.Setenv("AUTH0_CLIENT_ID", "id")
	t.Setenv("AUTH0_CLIENT_SECRET", "s")
	app.auth = newAuth0(srv.URL)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Get(srv.URL + "/auth/callback?code=x&state=forged")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if loc := res.Header.Get("Location"); !strings.Contains(loc, "auth_error") {
		t.Fatalf("forged state not rejected, went to %q", loc)
	}
}
