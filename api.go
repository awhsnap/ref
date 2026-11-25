package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

type App struct {
	db      *sql.DB
	baseURL string
	google  *oauth2.Config // nil when Google sign-in isn't configured
	demo    bool           // allow the sample accounts without signing in
	secure  bool           // send cookies over HTTPS only
}

type User struct {
	ID             int64          `json:"id"`
	Name           string         `json:"name"`
	Role           string         `json:"role"`
	DealershipID   sql.NullInt64  `json:"-"`
	DealershipName sql.NullString `json:"-"`
	FacebookPage   sql.NullString `json:"-"`
	Phone          string         `json:"phone"`
	Email          string         `json:"email"`
	SendMode       string         `json:"send_mode"`
	GoogleSub      string         `json:"-"`
	Picture        string         `json:"picture"`
	Onboarded      bool           `json:"onboarded"`
	InviteCode     sql.NullString `json:"-"`
}

func (u User) MarshalJSON() ([]byte, error) {
	type alias User
	return json.Marshal(struct {
		alias
		Dealership   string `json:"dealership"`
		FacebookPage string `json:"facebook_page"`
		Demo         bool   `json:"demo"`
		InviteCode   string `json:"invite_code,omitempty"`
	}{alias(u), u.DealershipName.String, u.FacebookPage.String, u.GoogleSub == "", inviteFor(u)})
}

func inviteFor(u User) string {
	if u.Role == "manager" {
		return u.InviteCode.String
	}
	return ""
}

type Customer struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Phone        string `json:"phone"`
	Email        string `json:"email"`
	Vehicle      string `json:"vehicle"`
	DeliveryDate string `json:"delivery_date"`
	Birthday     string `json:"birthday"`
	SMSOptIn     bool   `json:"sms_opt_in"`
	EmailOptIn   bool   `json:"email_opt_in"`
	PhotoOptIn   bool   `json:"photo_opt_in"`
	ConsentAt    string `json:"consent_at"`
	RefCode      string `json:"ref_code"`
	Notes        string `json:"notes"`
	Salesperson  string `json:"salesperson"`
}

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

const userCols = `u.id, u.name, u.role, u.dealership_id, COALESCE(d.name, NULLIF(u.workplace, '')), d.facebook_page, u.phone, u.email, u.send_mode,
	u.google_sub, u.picture, u.onboarded, d.invite_code`

func scanUser(sc interface{ Scan(...any) error }) (User, error) {
	var u User
	err := sc.Scan(&u.ID, &u.Name, &u.Role, &u.DealershipID, &u.DealershipName, &u.FacebookPage, &u.Phone, &u.Email, &u.SendMode,
		&u.GoogleSub, &u.Picture, &u.Onboarded, &u.InviteCode)
	return u, err
}

func loadUser(q querier, id int64) (User, error) {
	return scanUser(q.QueryRow(`SELECT `+userCols+` FROM users u LEFT JOIN dealerships d ON d.id = u.dealership_id WHERE u.id = ?`, id))
}

func (a *App) routes(mux *http.ServeMux) {
	a.authRoutes(mux)
	mux.HandleFunc("POST /api/onboarding", a.withUser(a.onboard))
	mux.HandleFunc("GET /api/users", a.listUsers)
	mux.HandleFunc("GET /api/me", a.withUser(a.getMe))
	mux.HandleFunc("PATCH /api/me", a.withUser(a.patchMe))
	mux.HandleFunc("GET /api/team", a.withUser(a.getTeam))
	mux.HandleFunc("GET /api/calendar", a.withUser(a.getCalendar))
	mux.HandleFunc("GET /api/stats", a.withUser(a.getStats))
	mux.HandleFunc("GET /api/customers", a.withUser(a.listCustomers))
	mux.HandleFunc("POST /api/customers", a.withUser(a.createCustomer))
	mux.HandleFunc("GET /api/customers/{id}", a.withUser(a.getCustomer))
	mux.HandleFunc("PATCH /api/customers/{id}", a.withUser(a.patchCustomer))
	mux.HandleFunc("PATCH /api/messages/{id}", a.withUser(a.patchMessage))
	mux.HandleFunc("POST /api/messages/{id}/reply", a.withUser(a.replyMessage))
	mux.HandleFunc("GET /api/templates", a.withUser(a.listTemplates))
	mux.HandleFunc("PATCH /api/templates/{id}", a.withUser(a.patchTemplate))
	mux.HandleFunc("GET /api/referrals", a.withUser(a.listReferrals))
	mux.HandleFunc("PATCH /api/referrals/{id}", a.withUser(a.patchReferral))
	mux.HandleFunc("GET /r/{code}", a.referralPage)
	mux.HandleFunc("POST /r/{code}", a.referralSubmit)
}

// ---- helpers ----

type httpError struct {
	code int
	msg  string
}

func (e httpError) Error() string { return e.msg }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode: %v", err)
	}
}

func writeErr(w http.ResponseWriter, err error) {
	var he httpError
	code := http.StatusInternalServerError
	msg := "internal error"
	switch {
	case errors.As(err, &he):
		code, msg = he.code, he.msg
	case errors.Is(err, sql.ErrNoRows):
		code, msg = http.StatusNotFound, "not found"
	default:
		log.Printf("error: %v", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v); err != nil {
		return httpError{http.StatusBadRequest, "invalid JSON body"}
	}
	return nil
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, httpError{http.StatusBadRequest, "bad id"}
	}
	return id, nil
}

// withUser resolves the signed-in user from the session cookie. In demo mode,
// a request with no session may pick one of the sample accounts with the
// X-User header; real (Google) accounts can never be reached that way.
func (a *App) withUser(h func(http.ResponseWriter, *http.Request, User) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var u User
		var err error
		if id := a.sessionUserID(r); id != 0 {
			u, err = loadUser(a.db, id)
		} else if demoID, _ := strconv.ParseInt(r.Header.Get("X-User"), 10, 64); a.demo && demoID != 0 {
			u, err = loadUser(a.db, demoID)
			if err == nil && u.GoogleSub != "" {
				err = sql.ErrNoRows
			}
		} else {
			err = sql.ErrNoRows
		}
		if err != nil {
			writeErr(w, httpError{http.StatusUnauthorized, "please sign in"})
			return
		}
		if !u.Onboarded && r.URL.Path != "/api/me" && r.URL.Path != "/api/onboarding" {
			writeErr(w, httpError{http.StatusForbidden, "finish setting up your account first"})
			return
		}
		if err := h(w, r, u); err != nil {
			writeErr(w, err)
		}
	}
}

// visibleUsers returns the salesperson ids whose data u may see. Managers see
// their whole dealership, optionally narrowed with ?salesperson=.
func (a *App) visibleUsers(r *http.Request, u User) ([]int64, error) {
	if u.Role != "manager" {
		return []int64{u.ID}, nil
	}
	rows, err := a.db.Query(`SELECT id FROM users WHERE dealership_id = ?`, u.DealershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	filter, _ := strconv.ParseInt(r.URL.Query().Get("salesperson"), 10, 64)
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if filter == 0 || filter == id {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

func inClause(ids []int64) (string, []any) {
	if len(ids) == 0 {
		return "(NULL)", nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return "(" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")", args
}

func (a *App) canSee(r *http.Request, u User, ownerID int64) error {
	ids, err := a.visibleUsers(r, u)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if id == ownerID {
			return nil
		}
	}
	return httpError{http.StatusNotFound, "not found"}
}

func today() string { return time.Now().Format("2006-01-02") }

// ---- users ----

// listUsers lists the sample accounts for the demo switcher.
func (a *App) listUsers(w http.ResponseWriter, r *http.Request) {
	users := []User{}
	if !a.demo {
		writeJSON(w, users)
		return
	}
	rows, err := a.db.Query(`SELECT ` + userCols + ` FROM users u LEFT JOIN dealerships d ON d.id = u.dealership_id WHERE u.google_sub = '' ORDER BY u.id`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			writeErr(w, err)
			return
		}
		users = append(users, u)
	}
	writeJSON(w, users)
}

func (a *App) getMe(w http.ResponseWriter, r *http.Request, u User) error {
	writeJSON(w, u)
	return nil
}

func (a *App) patchMe(w http.ResponseWriter, r *http.Request, u User) error {
	var in struct {
		SendMode string `json:"send_mode"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.SendMode != "tap" && in.SendMode != "auto" {
		return httpError{http.StatusBadRequest, "send_mode must be tap or auto"}
	}
	if _, err := a.db.Exec(`UPDATE users SET send_mode = ? WHERE id = ?`, in.SendMode, u.ID); err != nil {
		return err
	}
	u.SendMode = in.SendMode
	a.autoSend()
	writeJSON(w, u)
	return nil
}

func (a *App) getTeam(w http.ResponseWriter, r *http.Request, u User) error {
	if u.Role != "manager" {
		return httpError{http.StatusForbidden, "managers only"}
	}
	rows, err := a.db.Query(`
		SELECT u.id, u.name, u.role,
			(SELECT COUNT(*) FROM customers c WHERE c.user_id = u.id),
			(SELECT COUNT(*) FROM messages m WHERE m.user_id = u.id AND m.status = 'sent'),
			(SELECT COUNT(*) FROM messages m WHERE m.user_id = u.id AND m.reply_text <> ''),
			(SELECT COUNT(*) FROM messages m WHERE m.user_id = u.id AND m.status = 'scheduled' AND m.scheduled_date < ?),
			(SELECT COUNT(*) FROM referrals f WHERE f.user_id = u.id),
			(SELECT COUNT(*) FROM referrals f WHERE f.user_id = u.id AND f.status = 'sold')
		FROM users u WHERE u.dealership_id = ?
		ORDER BY 8 DESC, 7 DESC`, today(), u.DealershipID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Role      string `json:"role"`
		Customers int    `json:"customers"`
		Sent      int    `json:"sent"`
		Replies   int    `json:"replies"`
		Overdue   int    `json:"overdue"`
		Referrals int    `json:"referrals"`
		Sold      int    `json:"referral_sales"`
	}
	out := []row{}
	for rows.Next() {
		var t row
		if err := rows.Scan(&t.ID, &t.Name, &t.Role, &t.Customers, &t.Sent, &t.Replies, &t.Overdue, &t.Referrals, &t.Sold); err != nil {
			return err
		}
		out = append(out, t)
	}
	writeJSON(w, out)
	return rows.Err()
}

// ---- calendar & stats ----

type Event struct {
	ID           int64  `json:"id"`
	Type         string `json:"type"` // text, email, post, birthday, anniversary, referral
	Date         string `json:"date"`
	Title        string `json:"title"`
	Body         string `json:"body,omitempty"`
	Status       string `json:"status,omitempty"`
	SkipReason   string `json:"skip_reason,omitempty"`
	Reply        string `json:"reply,omitempty"`
	CustomerID   int64  `json:"customer_id"`
	CustomerName string `json:"customer_name"`
	Phone        string `json:"phone,omitempty"`
	Email        string `json:"email,omitempty"`
	Salesperson  string `json:"salesperson"`
	SalesID      int64  `json:"salesperson_id"`
}

func (a *App) getCalendar(w http.ResponseWriter, r *http.Request, u User) error {
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	f, err1 := time.Parse("2006-01-02", from)
	t, err2 := time.Parse("2006-01-02", to)
	if err1 != nil || err2 != nil || t.Before(f) || t.Sub(f) > 62*24*time.Hour {
		return httpError{http.StatusBadRequest, "from/to must be dates no more than 62 days apart"}
	}
	ids, err := a.visibleUsers(r, u)
	if err != nil {
		return err
	}
	in, args := inClause(ids)
	events := []Event{}

	rows, err := a.db.Query(`
		SELECT m.id, m.channel, m.scheduled_date, m.title, m.body, m.status, m.skip_reason, m.reply_text,
			c.id, c.first_name || ' ' || c.last_name, c.phone, c.email, u.name, u.id
		FROM messages m JOIN customers c ON c.id = m.customer_id JOIN users u ON u.id = m.user_id
		WHERE m.user_id IN `+in+` AND m.scheduled_date BETWEEN ? AND ?
		ORDER BY m.scheduled_date, m.status DESC, m.id`, append(args, from, to)...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Type, &e.Date, &e.Title, &e.Body, &e.Status, &e.SkipReason, &e.Reply,
			&e.CustomerID, &e.CustomerName, &e.Phone, &e.Email, &e.Salesperson, &e.SalesID); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	rows.Close()

	// Birthdays (month-day falls in range).
	rows, err = a.db.Query(`SELECT c.id, c.first_name || ' ' || c.last_name, c.birthday, c.phone, u.name, u.id
		FROM customers c JOIN users u ON u.id = c.user_id WHERE c.user_id IN `+in+` AND c.birthday <> ''`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var e Event
		var bday string
		if err := rows.Scan(&e.CustomerID, &e.CustomerName, &bday, &e.Phone, &e.Salesperson, &e.SalesID); err != nil {
			rows.Close()
			return err
		}
		if len(bday) == 10 {
			for d := f; !d.After(t); d = d.AddDate(0, 0, 1) {
				if d.Format("01-02") == bday[5:] {
					e.Type, e.Date, e.Title = "birthday", d.Format("2006-01-02"), "Birthday"
					events = append(events, e)
				}
			}
		}
	}
	rows.Close()

	// Referrals that came in.
	rows, err = a.db.Query(`SELECT f.id, f.created_date, f.name, f.status, c.id, c.first_name || ' ' || c.last_name, u.name, u.id
		FROM referrals f JOIN customers c ON c.id = f.customer_id JOIN users u ON u.id = f.user_id
		WHERE f.user_id IN `+in+` AND f.created_date BETWEEN ? AND ?`, append(args, from, to)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var e Event
		var name string
		if err := rows.Scan(&e.ID, &e.Date, &name, &e.Status, &e.CustomerID, &e.CustomerName, &e.Salesperson, &e.SalesID); err != nil {
			return err
		}
		e.Type, e.Title = "referral", "Referral: "+name
		events = append(events, e)
	}
	writeJSON(w, events)
	return rows.Err()
}

func (a *App) getStats(w http.ResponseWriter, r *http.Request, u User) error {
	month := r.URL.Query().Get("month")
	if _, err := time.Parse("2006-01", month); err != nil {
		month = time.Now().Format("2006-01")
	}
	ids, err := a.visibleUsers(r, u)
	if err != nil {
		return err
	}
	in, args := inClause(ids)
	var s struct {
		Month         string `json:"month"`
		Sent          int    `json:"sent"`
		Scheduled     int    `json:"scheduled"`
		Replies       int    `json:"replies"`
		Overdue       int    `json:"overdue"`
		Referrals     int    `json:"referrals"`
		ReferralSales int    `json:"referral_sales"`
		Customers     int    `json:"customers"`
	}
	s.Month = month
	like := month + "-%"
	q := func(dst *int, query string, extra ...any) error {
		return a.db.QueryRow(query, append(append([]any{}, args...), extra...)...).Scan(dst)
	}
	if err := q(&s.Sent, `SELECT COUNT(*) FROM messages WHERE user_id IN `+in+` AND status='sent' AND scheduled_date LIKE ?`, like); err != nil {
		return err
	}
	if err := q(&s.Scheduled, `SELECT COUNT(*) FROM messages WHERE user_id IN `+in+` AND status='scheduled' AND scheduled_date LIKE ?`, like); err != nil {
		return err
	}
	if err := q(&s.Replies, `SELECT COUNT(*) FROM messages WHERE user_id IN `+in+` AND reply_text<>'' AND scheduled_date LIKE ?`, like); err != nil {
		return err
	}
	if err := q(&s.Overdue, `SELECT COUNT(*) FROM messages WHERE user_id IN `+in+` AND status='scheduled' AND scheduled_date < ?`, today()); err != nil {
		return err
	}
	if err := q(&s.Referrals, `SELECT COUNT(*) FROM referrals WHERE user_id IN `+in+` AND created_date LIKE ?`, like); err != nil {
		return err
	}
	if err := q(&s.ReferralSales, `SELECT COUNT(*) FROM referrals WHERE user_id IN `+in+` AND status='sold' AND created_date LIKE ?`, like); err != nil {
		return err
	}
	if err := q(&s.Customers, `SELECT COUNT(*) FROM customers WHERE user_id IN `+in); err != nil {
		return err
	}
	writeJSON(w, s)
	return nil
}

// ---- customers ----

const customerCols = `c.id, c.user_id, c.first_name, c.last_name, c.phone, c.email, c.vehicle, c.delivery_date, c.birthday,
	c.sms_opt_in, c.email_opt_in, c.photo_opt_in, c.consent_at, c.ref_code, c.notes, u.name`

func scanCustomer(sc interface{ Scan(...any) error }) (Customer, error) {
	var c Customer
	err := sc.Scan(&c.ID, &c.UserID, &c.FirstName, &c.LastName, &c.Phone, &c.Email, &c.Vehicle, &c.DeliveryDate, &c.Birthday,
		&c.SMSOptIn, &c.EmailOptIn, &c.PhotoOptIn, &c.ConsentAt, &c.RefCode, &c.Notes, &c.Salesperson)
	return c, err
}

func (a *App) listCustomers(w http.ResponseWriter, r *http.Request, u User) error {
	ids, err := a.visibleUsers(r, u)
	if err != nil {
		return err
	}
	in, args := inClause(ids)
	q := "%" + strings.ToLower(r.URL.Query().Get("q")) + "%"
	rows, err := a.db.Query(`SELECT `+customerCols+` FROM customers c JOIN users u ON u.id = c.user_id
		WHERE c.user_id IN `+in+` AND lower(c.first_name || ' ' || c.last_name || ' ' || c.vehicle) LIKE ?
		ORDER BY c.delivery_date DESC`, append(args, q)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []Customer{}
	for rows.Next() {
		c, err := scanCustomer(rows)
		if err != nil {
			return err
		}
		out = append(out, c)
	}
	writeJSON(w, out)
	return rows.Err()
}

func (a *App) createCustomer(w http.ResponseWriter, r *http.Request, u User) error {
	var c Customer
	if err := decode(r, &c); err != nil {
		return err
	}
	c.FirstName, c.LastName = strings.TrimSpace(c.FirstName), strings.TrimSpace(c.LastName)
	if c.FirstName == "" {
		return httpError{http.StatusBadRequest, "first name is required"}
	}
	if c.Phone == "" && c.Email == "" {
		return httpError{http.StatusBadRequest, "add a phone number or email"}
	}
	if c.DeliveryDate == "" {
		c.DeliveryDate = today()
	}
	if _, err := time.Parse("2006-01-02", c.DeliveryDate); err != nil {
		return httpError{http.StatusBadRequest, "delivery date must be YYYY-MM-DD"}
	}
	if c.Birthday != "" {
		if _, err := time.Parse("2006-01-02", c.Birthday); err != nil {
			return httpError{http.StatusBadRequest, "birthday must be YYYY-MM-DD"}
		}
	}
	c.UserID = u.ID
	c.RefCode = randomCode(rand.New(rand.NewSource(time.Now().UnixNano())))
	if c.SMSOptIn || c.EmailOptIn || c.PhotoOptIn {
		c.ConsentAt = time.Now().Format("2006-01-02 15:04") + " via app checkbox (recorded by " + u.Name + ")"
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO customers(user_id, first_name, last_name, phone, email, vehicle, delivery_date, birthday, sms_opt_in, email_opt_in, photo_opt_in, consent_at, ref_code, notes)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.UserID, c.FirstName, c.LastName, c.Phone, c.Email, c.Vehicle, c.DeliveryDate, c.Birthday,
		c.SMSOptIn, c.EmailOptIn, c.PhotoOptIn, c.ConsentAt, c.RefCode, c.Notes)
	if err != nil {
		return err
	}
	c.ID, _ = res.LastInsertId()
	if err := scheduleCadence(tx, u, c, a.baseURL); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.autoSend()
	c.Salesperson = u.Name
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, c)
	return nil
}

func (a *App) loadCustomer(r *http.Request, u User, id int64) (Customer, error) {
	c, err := scanCustomer(a.db.QueryRow(`SELECT `+customerCols+` FROM customers c JOIN users u ON u.id = c.user_id WHERE c.id = ?`, id))
	if err != nil {
		return c, err
	}
	return c, a.canSee(r, u, c.UserID)
}

func (a *App) getCustomer(w http.ResponseWriter, r *http.Request, u User) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	c, err := a.loadCustomer(r, u, id)
	if err != nil {
		return err
	}
	rows, err := a.db.Query(`SELECT id, channel, scheduled_date, title, body, status, skip_reason, reply_text FROM messages WHERE customer_id = ? ORDER BY scheduled_date, id`, id)
	if err != nil {
		return err
	}
	msgs := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Type, &e.Date, &e.Title, &e.Body, &e.Status, &e.SkipReason, &e.Reply); err != nil {
			rows.Close()
			return err
		}
		msgs = append(msgs, e)
	}
	rows.Close()
	refs, err := a.queryReferrals(`f.customer_id = ?`, id)
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{
		"customer":      c,
		"messages":      msgs,
		"referrals":     refs,
		"referral_link": strings.TrimRight(a.baseURL, "/") + "/r/" + c.RefCode,
	})
	return nil
}

func (a *App) patchCustomer(w http.ResponseWriter, r *http.Request, u User) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if _, err := a.loadCustomer(r, u, id); err != nil {
		return err
	}
	var in struct {
		SMSOptIn   *bool   `json:"sms_opt_in"`
		EmailOptIn *bool   `json:"email_opt_in"`
		PhotoOptIn *bool   `json:"photo_opt_in"`
		Notes      *string `json:"notes"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	set := func(col, channel, reason string, v *bool) error {
		if v == nil {
			return nil
		}
		if _, err := tx.Exec(`UPDATE customers SET `+col+` = ? WHERE id = ?`, *v, id); err != nil {
			return err
		}
		if *v {
			_, err = tx.Exec(`UPDATE messages SET status='scheduled', skip_reason='' WHERE customer_id=? AND channel=? AND status='skipped' AND skip_reason=? AND scheduled_date >= ?`, id, channel, reason, today())
		} else {
			_, err = tx.Exec(`UPDATE messages SET status='skipped', skip_reason=? WHERE customer_id=? AND channel=? AND status='scheduled'`, reason, id, channel)
		}
		return err
	}
	if err := set("sms_opt_in", "text", "No text consent", in.SMSOptIn); err != nil {
		return err
	}
	if err := set("email_opt_in", "email", "No email consent", in.EmailOptIn); err != nil {
		return err
	}
	if err := set("photo_opt_in", "post", "No photo consent", in.PhotoOptIn); err != nil {
		return err
	}
	if in.Notes != nil {
		if _, err := tx.Exec(`UPDATE customers SET notes=? WHERE id=?`, *in.Notes, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return a.getCustomer(w, r, u)
}

// ---- messages ----

func (a *App) messageOwner(id int64) (int64, int64, error) {
	var owner, cust int64
	err := a.db.QueryRow(`SELECT user_id, customer_id FROM messages WHERE id = ?`, id).Scan(&owner, &cust)
	return owner, cust, err
}

func (a *App) patchMessage(w http.ResponseWriter, r *http.Request, u User) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	owner, _, err := a.messageOwner(id)
	if err != nil {
		return err
	}
	if err := a.canSee(r, u, owner); err != nil {
		return err
	}
	var in struct {
		Status        *string `json:"status"`
		Body          *string `json:"body"`
		ScheduledDate *string `json:"scheduled_date"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.Status != nil {
		switch *in.Status {
		case "sent":
			_, err = a.db.Exec(`UPDATE messages SET status='sent', sent_at=? WHERE id=?`, time.Now().Format("2006-01-02 15:04"), id)
		case "skipped":
			_, err = a.db.Exec(`UPDATE messages SET status='skipped', skip_reason='Skipped by salesperson' WHERE id=?`, id)
		case "scheduled":
			_, err = a.db.Exec(`UPDATE messages SET status='scheduled', skip_reason='', sent_at='' WHERE id=?`, id)
		default:
			return httpError{http.StatusBadRequest, "bad status"}
		}
		if err != nil {
			return err
		}
	}
	if in.Body != nil {
		if _, err := a.db.Exec(`UPDATE messages SET body=? WHERE id=?`, *in.Body, id); err != nil {
			return err
		}
	}
	if in.ScheduledDate != nil {
		if _, err := time.Parse("2006-01-02", *in.ScheduledDate); err != nil {
			return httpError{http.StatusBadRequest, "date must be YYYY-MM-DD"}
		}
		if _, err := a.db.Exec(`UPDATE messages SET scheduled_date=? WHERE id=?`, *in.ScheduledDate, id); err != nil {
			return err
		}
	}
	writeJSON(w, map[string]bool{"ok": true})
	return nil
}

// replyMessage simulates an incoming customer reply. "STOP" and similar
// keywords opt the customer out of all texts, as carriers require.
func (a *App) replyMessage(w http.ResponseWriter, r *http.Request, u User) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	owner, cust, err := a.messageOwner(id)
	if err != nil {
		return err
	}
	if err := a.canSee(r, u, owner); err != nil {
		return err
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if _, err := a.db.Exec(`UPDATE messages SET reply_text=? WHERE id=?`, in.Text, id); err != nil {
		return err
	}
	optedOut := false
	switch strings.ToUpper(strings.TrimSpace(in.Text)) {
	case "STOP", "STOPALL", "UNSUBSCRIBE", "CANCEL", "END", "QUIT":
		optedOut = true
		if _, err := a.db.Exec(`UPDATE customers SET sms_opt_in=0 WHERE id=?`, cust); err != nil {
			return err
		}
		if _, err := a.db.Exec(`UPDATE messages SET status='skipped', skip_reason='Customer replied STOP' WHERE customer_id=? AND channel='text' AND status='scheduled'`, cust); err != nil {
			return err
		}
	}
	writeJSON(w, map[string]bool{"ok": true, "opted_out": optedOut})
	return nil
}

// autoSend stands in for the real texting/email provider: for salespeople on
// "auto" mode, anything due today or earlier is marked sent. Dealership "Sold"
// posts go to the dealership's Facebook Page automatically.
func (a *App) autoSend() {
	now := time.Now().Format("2006-01-02 15:04")
	_, err := a.db.Exec(`UPDATE messages SET status='sent', sent_at=?
		WHERE status='scheduled' AND scheduled_date <= ? AND (
			(channel IN ('text','email') AND user_id IN (SELECT id FROM users WHERE send_mode='auto'))
			OR (channel='post' AND user_id IN (SELECT id FROM users WHERE dealership_id IS NOT NULL))
		)`, now, today())
	if err != nil {
		log.Printf("autosend: %v", err)
	}
}

func (a *App) autoSendLoop() {
	for {
		a.autoSend()
		a.cleanSessions()
		time.Sleep(time.Minute)
	}
}

// ---- templates ----

type Template struct {
	ID      int64  `json:"id"`
	Offset  int    `json:"offset_days"`
	Channel string `json:"channel"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Active  bool   `json:"active"`
}

func (a *App) listTemplates(w http.ResponseWriter, r *http.Request, u User) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ensureTemplates(tx, accountKey(u)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	rows, err := a.db.Query(`SELECT id, offset_days, channel, title, body, active FROM templates WHERE account = ? ORDER BY offset_days, id`, accountKey(u))
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []Template{}
	for rows.Next() {
		var t Template
		if err := rows.Scan(&t.ID, &t.Offset, &t.Channel, &t.Title, &t.Body, &t.Active); err != nil {
			return err
		}
		out = append(out, t)
	}
	writeJSON(w, map[string]any{"templates": out, "can_edit": u.Role != "sales"})
	return rows.Err()
}

func (a *App) patchTemplate(w http.ResponseWriter, r *http.Request, u User) error {
	if u.Role == "sales" {
		return httpError{http.StatusForbidden, "only the dealership manager can edit shared templates"}
	}
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var in struct {
		Title  *string `json:"title"`
		Body   *string `json:"body"`
		Offset *int    `json:"offset_days"`
		Active *bool   `json:"active"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.Offset != nil && (*in.Offset < 0 || *in.Offset > 3650) {
		return httpError{http.StatusBadRequest, "days must be between 0 and 3650"}
	}
	res, err := a.db.Exec(`UPDATE templates SET
		title = COALESCE(?, title), body = COALESCE(?, body), offset_days = COALESCE(?, offset_days), active = COALESCE(?, active)
		WHERE id = ? AND account = ?`, in.Title, in.Body, in.Offset, in.Active, id, accountKey(u))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	writeJSON(w, map[string]bool{"ok": true})
	return nil
}

// ---- referrals ----

type Referral struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Phone        string `json:"phone"`
	Email        string `json:"email"`
	Interest     string `json:"interest"`
	Status       string `json:"status"`
	Date         string `json:"date"`
	CustomerID   int64  `json:"customer_id"`
	CustomerName string `json:"referred_by"`
	Salesperson  string `json:"salesperson"`
}

func (a *App) queryReferrals(where string, args ...any) ([]Referral, error) {
	rows, err := a.db.Query(`SELECT f.id, f.name, f.phone, f.email, f.interest, f.status, f.created_date, c.id, c.first_name || ' ' || c.last_name, u.name
		FROM referrals f JOIN customers c ON c.id = f.customer_id JOIN users u ON u.id = f.user_id
		WHERE `+where+` ORDER BY f.created_date DESC, f.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Referral{}
	for rows.Next() {
		var f Referral
		if err := rows.Scan(&f.ID, &f.Name, &f.Phone, &f.Email, &f.Interest, &f.Status, &f.Date, &f.CustomerID, &f.CustomerName, &f.Salesperson); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (a *App) listReferrals(w http.ResponseWriter, r *http.Request, u User) error {
	ids, err := a.visibleUsers(r, u)
	if err != nil {
		return err
	}
	in, args := inClause(ids)
	refs, err := a.queryReferrals(`f.user_id IN `+in, args...)
	if err != nil {
		return err
	}
	writeJSON(w, refs)
	return nil
}

func (a *App) patchReferral(w http.ResponseWriter, r *http.Request, u User) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var owner int64
	if err := a.db.QueryRow(`SELECT user_id FROM referrals WHERE id=?`, id).Scan(&owner); err != nil {
		return err
	}
	if err := a.canSee(r, u, owner); err != nil {
		return err
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.Status != "new" && in.Status != "contacted" && in.Status != "sold" {
		return httpError{http.StatusBadRequest, "bad status"}
	}
	if _, err := a.db.Exec(`UPDATE referrals SET status=? WHERE id=?`, in.Status, id); err != nil {
		return err
	}
	writeJSON(w, map[string]bool{"ok": true})
	return nil
}

// ---- public referral landing page ----

var refTmpl = template.Must(template.New("ref").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Sales}} at {{.Dealer}}</title><link rel="stylesheet" href="/style.css"></head>
<body class="landing"><main class="landing-card">
{{if .Done}}
  <h1>Thanks, you're all set!</h1>
  <p>{{.Sales}} will reach out shortly. {{.Customer}} is lucky to have friends like you.</p>
{{else}}
  <p class="eyebrow">Referred by {{.Customer}}</p>
  <h1>Meet {{.Sales}}</h1>
  <p>{{.Customer}} bought their car from {{.Sales}} at {{.Dealer}} and thought you'd like the same easy experience.</p>
  <form method="post">
    <label>Your name<input name="name" required maxlength="80"></label>
    <label>Phone<input name="phone" type="tel" maxlength="30"></label>
    <label>Email<input name="email" type="email" maxlength="120"></label>
    <label>What are you looking for?<input name="interest" placeholder="e.g. a family SUV" maxlength="120"></label>
    <label class="check"><input type="checkbox" name="consent" value="1" required> It's OK for {{.Sales}} to contact me by text or email about my car search.</label>
    <button class="btn primary" type="submit">Have {{.Sales}} reach out</button>
  </form>
{{end}}
</main></body></html>`))

func (a *App) referralLookup(code string) (cust Customer, sales, dealer string, err error) {
	var d sql.NullString
	err = a.db.QueryRow(`SELECT c.id, c.user_id, c.first_name, u.name, COALESCE(d.name, NULLIF(u.workplace, '')) FROM customers c JOIN users u ON u.id = c.user_id
		LEFT JOIN dealerships d ON d.id = u.dealership_id WHERE c.ref_code = ?`, code).
		Scan(&cust.ID, &cust.UserID, &cust.FirstName, &sales, &d)
	dealer = d.String
	if dealer == "" {
		dealer = "the dealership"
	}
	return
}

func (a *App) referralPage(w http.ResponseWriter, r *http.Request) {
	c, sales, dealer, err := a.referralLookup(r.PathValue("code"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	refTmpl.Execute(w, map[string]any{"Customer": c.FirstName, "Sales": strings.Fields(sales)[0], "Dealer": dealer, "Done": false})
}

func (a *App) referralSubmit(w http.ResponseWriter, r *http.Request) {
	c, sales, dealer, err := a.referralLookup(r.PathValue("code"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || r.FormValue("consent") != "1" {
		http.Error(w, "name and consent are required", http.StatusBadRequest)
		return
	}
	trim := func(s string, n int) string {
		s = strings.TrimSpace(s)
		if len(s) > n {
			s = s[:n]
		}
		return s
	}
	if _, err := a.db.Exec(`INSERT INTO referrals(customer_id, user_id, name, phone, email, interest, created_date) VALUES (?,?,?,?,?,?,?)`,
		c.ID, c.UserID, trim(name, 80), trim(r.FormValue("phone"), 30), trim(r.FormValue("email"), 120), trim(r.FormValue("interest"), 120), today()); err != nil {
		writeErr(w, fmt.Errorf("insert referral: %w", err))
		return
	}
	refTmpl.Execute(w, map[string]any{"Customer": c.FirstName, "Sales": strings.Fields(sales)[0], "Dealer": dealer, "Done": true})
}
