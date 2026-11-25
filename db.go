package main

import (
	"database/sql"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

const schema = `
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS dealerships (
	id   INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	facebook_page TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY,
	name          TEXT NOT NULL,
	role          TEXT NOT NULL CHECK (role IN ('solo','manager','sales')),
	dealership_id INTEGER REFERENCES dealerships(id),
	phone         TEXT NOT NULL DEFAULT '',
	email         TEXT NOT NULL DEFAULT '',
	send_mode     TEXT NOT NULL DEFAULT 'tap' CHECK (send_mode IN ('tap','auto')),
	workplace     TEXT NOT NULL DEFAULT '' -- dealership name for solo accounts
);

CREATE TABLE IF NOT EXISTS customers (
	id            INTEGER PRIMARY KEY,
	user_id       INTEGER NOT NULL REFERENCES users(id),
	first_name    TEXT NOT NULL,
	last_name     TEXT NOT NULL DEFAULT '',
	phone         TEXT NOT NULL DEFAULT '',
	email         TEXT NOT NULL DEFAULT '',
	vehicle       TEXT NOT NULL DEFAULT '',
	delivery_date TEXT NOT NULL,
	birthday      TEXT NOT NULL DEFAULT '',
	sms_opt_in    INTEGER NOT NULL DEFAULT 0,
	email_opt_in  INTEGER NOT NULL DEFAULT 0,
	photo_opt_in  INTEGER NOT NULL DEFAULT 0,
	consent_at    TEXT NOT NULL DEFAULT '',
	ref_code      TEXT NOT NULL UNIQUE,
	notes         TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Follow-up cadence steps. account is 'd<dealership id>' or 'u<user id>'.
CREATE TABLE IF NOT EXISTS templates (
	id          INTEGER PRIMARY KEY,
	account     TEXT NOT NULL,
	offset_days INTEGER NOT NULL,
	channel     TEXT NOT NULL CHECK (channel IN ('text','email','post')),
	title       TEXT NOT NULL,
	body        TEXT NOT NULL,
	active      INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS messages (
	id             INTEGER PRIMARY KEY,
	customer_id    INTEGER NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	user_id        INTEGER NOT NULL REFERENCES users(id),
	channel        TEXT NOT NULL,
	title          TEXT NOT NULL,
	body           TEXT NOT NULL,
	scheduled_date TEXT NOT NULL,
	status         TEXT NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled','sent','skipped')),
	skip_reason    TEXT NOT NULL DEFAULT '',
	sent_at        TEXT NOT NULL DEFAULT '',
	reply_text     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS messages_date ON messages(scheduled_date);

CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS referrals (
	id          INTEGER PRIMARY KEY,
	customer_id INTEGER NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	user_id     INTEGER NOT NULL REFERENCES users(id),
	name        TEXT NOT NULL,
	phone       TEXT NOT NULL DEFAULT '',
	email       TEXT NOT NULL DEFAULT '',
	interest    TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'new' CHECK (status IN ('new','contacted','sold')),
	created_date TEXT NOT NULL
);
`

// Default cadence used for every new account.
var defaultTemplates = []struct {
	Offset  int
	Channel string
	Title   string
	Body    string
}{
	{0, "text", "Delivery day thank-you", "Hi {first_name}, it's {salesperson} from {dealership}. Congrats again on the {vehicle}! Save my number, I'm here for anything you need. Reply STOP to opt out."},
	{0, "post", "Sold! post", "Congrats to {first_name} on the new {vehicle}! Thank you for trusting me and {dealership}. 🎉🚗"},
	{3, "text", "Day 3 check-in", "Hi {first_name}, how's the {vehicle} treating you? Any questions on features or pairing your phone, just text me."},
	{14, "email", "Two-week check-in + review", "Hi {first_name},\n\nIt's been two weeks with your {vehicle}. I hope you're loving it! If you have a minute, a quick review would mean a lot to me.\n\nThanks,\n{salesperson}\n{dealership}"},
	{30, "text", "Referral ask", "Hi {first_name}, hope you're loving the {vehicle}! If anyone you know is shopping, send them my way and I'll take great care of them: {referral_link}"},
	{90, "email", "First service reminder", "Hi {first_name},\n\nYour {vehicle} is coming up on its first service. Reply here and I'll help you book it.\n\n{salesperson}\n{dealership}"},
	{180, "text", "6-month touch", "Hi {first_name}, {salesperson} here. Six months with the {vehicle} already! Anything I can help with? And thanks for any friends you've sent my way: {referral_link}"},
	{365, "text", "Car anniversary", "Happy one-year car anniversary, {first_name}! 🎉 Thanks for being a great customer. I'm always here if you or someone you know needs anything: {referral_link}"},
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	// Columns added after the first release. SQLite can't add a column
	// twice, so check first.
	added := []struct{ table, col, def string }{
		{"users", "google_sub", "TEXT NOT NULL DEFAULT ''"},
		{"users", "picture", "TEXT NOT NULL DEFAULT ''"},
		{"users", "onboarded", "INTEGER NOT NULL DEFAULT 1"},
		{"dealerships", "invite_code", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, c := range added {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.col).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.col + ` ` + c.def); err != nil {
				return err
			}
		}
	}
	_, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS users_google_sub ON users(google_sub) WHERE google_sub <> '';
		CREATE INDEX IF NOT EXISTS customers_user ON customers(user_id);
		CREATE INDEX IF NOT EXISTS messages_user ON messages(user_id, scheduled_date)`)
	return err
}

func accountKey(u User) string {
	if u.DealershipID.Valid {
		return fmt.Sprintf("d%d", u.DealershipID.Int64)
	}
	return fmt.Sprintf("u%d", u.ID)
}

func ensureTemplates(tx *sql.Tx, account string) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM templates WHERE account = ?`, account).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	for _, t := range defaultTemplates {
		if _, err := tx.Exec(`INSERT INTO templates(account, offset_days, channel, title, body) VALUES (?,?,?,?,?)`,
			account, t.Offset, t.Channel, t.Title, t.Body); err != nil {
			return err
		}
	}
	return nil
}

func fill(body string, vars map[string]string) string {
	for k, v := range vars {
		body = strings.ReplaceAll(body, "{"+k+"}", v)
	}
	return body
}

// scheduleCadence creates the follow-up messages for a newly sold customer.
func scheduleCadence(tx *sql.Tx, u User, c Customer, baseURL string) error {
	if err := ensureTemplates(tx, accountKey(u)); err != nil {
		return err
	}
	dealer := "my dealership"
	if u.DealershipName.Valid {
		dealer = u.DealershipName.String
	}
	vars := map[string]string{
		"first_name":    c.FirstName,
		"vehicle":       c.Vehicle,
		"salesperson":   strings.Fields(u.Name)[0],
		"dealership":    dealer,
		"referral_link": strings.TrimRight(baseURL, "/") + "/r/" + c.RefCode,
	}
	delivery, err := time.Parse("2006-01-02", c.DeliveryDate)
	if err != nil {
		return fmt.Errorf("bad delivery date: %w", err)
	}
	rows, err := tx.Query(`SELECT offset_days, channel, title, body FROM templates WHERE account = ? AND active = 1 ORDER BY offset_days, id`, accountKey(u))
	if err != nil {
		return err
	}
	type step struct {
		off             int
		ch, title, body string
	}
	var steps []step
	for rows.Next() {
		var s step
		if err := rows.Scan(&s.off, &s.ch, &s.title, &s.body); err != nil {
			rows.Close()
			return err
		}
		steps = append(steps, s)
	}
	rows.Close()
	for _, s := range steps {
		status, reason := "scheduled", ""
		switch {
		case s.ch == "text" && !c.SMSOptIn:
			status, reason = "skipped", "No text consent"
		case s.ch == "email" && !c.EmailOptIn:
			status, reason = "skipped", "No email consent"
		case s.ch == "post" && !c.PhotoOptIn:
			status, reason = "skipped", "No photo consent"
		}
		date := delivery.AddDate(0, 0, s.off).Format("2006-01-02")
		if _, err := tx.Exec(`INSERT INTO messages(customer_id, user_id, channel, title, body, scheduled_date, status, skip_reason) VALUES (?,?,?,?,?,?,?,?)`,
			c.ID, u.ID, s.ch, s.title, fill(s.body, vars), date, status, reason); err != nil {
			return err
		}
	}
	return nil
}

func randomCode(r *rand.Rand) string {
	const letters = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 7)
	for i := range b {
		b[i] = letters[r.Intn(len(letters))]
	}
	return string(b)
}

// seed fills an empty database with demo data dated around today.
func seed(db *sql.DB, baseURL string) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE google_sub = ''`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	mustExec := func(q string, args ...any) int64 {
		if err != nil {
			return 0
		}
		var res sql.Result
		res, err = tx.Exec(q, args...)
		if err != nil {
			return 0
		}
		id, _ := res.LastInsertId()
		return id
	}

	d := mustExec(`INSERT INTO dealerships(name, facebook_page) VALUES ('Sunset Motors', 'facebook.com/sunsetmotors-demo')`)
	mustExec(`INSERT INTO users(name, role, phone, email, send_mode, workplace) VALUES ('Alex Rivera','solo','+15555550101','alex@example.com','tap','Bayside Auto')`)
	mustExec(`INSERT INTO users(name, role, dealership_id, phone, email, send_mode) VALUES ('Jordan Lee','manager',?,'+15555550102','jordan@example.com','auto')`, d)
	mustExec(`INSERT INTO users(name, role, dealership_id, phone, email, send_mode) VALUES ('Sam Patel','sales',?,'+15555550103','sam@example.com','tap')`, d)
	mustExec(`INSERT INTO users(name, role, dealership_id, phone, email, send_mode) VALUES ('Maria Gomez','sales',?,'+15555550104','maria@example.com','tap')`, d)
	if err != nil {
		return err
	}

	r := rand.New(rand.NewSource(42))
	firsts := []string{"Chris", "Taylor", "Morgan", "Jamie", "Riley", "Casey", "Avery", "Drew", "Quinn", "Jesse", "Robin", "Skyler", "Dana", "Kendall", "Reese", "Parker", "Cameron", "Logan"}
	lasts := []string{"Nguyen", "Smith", "Johnson", "Brown", "Garcia", "Martinez", "Davis", "Lopez", "Wilson", "Clark", "Young", "Hall", "Allen", "King"}
	vehicles := []string{"2026 Toyota Tacoma", "2026 Honda CR-V", "2025 Ford F-150", "2026 Chevy Equinox", "2026 Toyota RAV4", "2025 Jeep Wrangler", "2026 Hyundai Tucson", "2026 Kia Telluride", "2026 Subaru Outback", "2025 Tesla Model Y"}
	replies := []string{"Loving it, thank you!", "All good so far 👍", "Quick question about Apple CarPlay", "Thanks! Will do.", "My brother is looking actually, I'll send him your way"}
	today := time.Now()

	var ids []int64
	rows, err := tx.Query(`SELECT id FROM users WHERE google_sub = '' ORDER BY id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, uid := range ids {
		u, err := loadUser(tx, uid)
		if err != nil {
			return err
		}
		count := 9
		if u.Role == "manager" {
			count = 3
		}
		for i := 0; i < count; i++ {
			c := Customer{
				UserID:       uid,
				FirstName:    firsts[r.Intn(len(firsts))],
				LastName:     lasts[r.Intn(len(lasts))],
				Vehicle:      vehicles[r.Intn(len(vehicles))],
				DeliveryDate: today.AddDate(0, 0, -r.Intn(200)).Format("2006-01-02"),
				SMSOptIn:     r.Intn(10) > 0,
				EmailOptIn:   r.Intn(10) > 1,
				PhotoOptIn:   r.Intn(10) > 3,
				RefCode:      randomCode(r),
			}
			if i == 0 {
				c.DeliveryDate = today.Format("2006-01-02")
				c.SMSOptIn, c.EmailOptIn, c.PhotoOptIn = true, true, true
			}
			c.Phone = fmt.Sprintf("+1555555%04d", 1000+r.Intn(8999))
			c.Email = strings.ToLower(c.FirstName+"."+c.LastName) + "@example.com"
			if r.Intn(3) > 0 {
				c.Birthday = fmt.Sprintf("%d-%s", 1960+r.Intn(40), today.AddDate(0, 0, r.Intn(60)-20).Format("01-02"))
			}
			res, err := tx.Exec(`INSERT INTO customers(user_id, first_name, last_name, phone, email, vehicle, delivery_date, birthday, sms_opt_in, email_opt_in, photo_opt_in, consent_at, ref_code)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				c.UserID, c.FirstName, c.LastName, c.Phone, c.Email, c.Vehicle, c.DeliveryDate, c.Birthday,
				c.SMSOptIn, c.EmailOptIn, c.PhotoOptIn, c.DeliveryDate+" at delivery (signed form)", c.RefCode)
			if err != nil {
				return err
			}
			c.ID, _ = res.LastInsertId()
			if err := scheduleCadence(tx, u, c, baseURL); err != nil {
				return err
			}
			// Past messages are already sent; some got replies.
			// Leave the last two days unsent so the demo shows overdue items.
			todayStr := today.AddDate(0, 0, -2).Format("2006-01-02")
			if _, err := tx.Exec(`UPDATE messages SET status='sent', sent_at=scheduled_date||' 10:00' WHERE customer_id=? AND status='scheduled' AND scheduled_date < ?`, c.ID, todayStr); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE messages SET reply_text=? WHERE id IN (SELECT id FROM messages WHERE customer_id=? AND status='sent' AND channel='text' ORDER BY random() LIMIT 1) AND abs(random()) % 2 = 0`,
				replies[r.Intn(len(replies))], c.ID); err != nil {
				return err
			}
			// Customers past day 30 sometimes sent a referral.
			deliv, _ := time.Parse("2006-01-02", c.DeliveryDate)
			if today.Sub(deliv) > 32*24*time.Hour && r.Intn(3) == 0 {
				status := []string{"new", "contacted", "sold"}[r.Intn(3)]
				refDate := deliv.AddDate(0, 0, 31+r.Intn(int(today.Sub(deliv).Hours()/24)-30))
				if _, err := tx.Exec(`INSERT INTO referrals(customer_id, user_id, name, phone, interest, status, created_date) VALUES (?,?,?,?,?,?,?)`,
					c.ID, uid, firsts[r.Intn(len(firsts))]+" "+lasts[r.Intn(len(lasts))],
					fmt.Sprintf("+1555555%04d", 1000+r.Intn(8999)), vehicles[r.Intn(len(vehicles))], status, refDate.Format("2006-01-02")); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}
