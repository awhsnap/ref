// Command ref is a demo of a referral follow-up app for car salespeople.
// It serves a small JSON API backed by SQLite and a plain HTML/CSS/JS front end.
package main

import (
	"database/sql"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed static
var staticFiles embed.FS

func main() {
	dbPath := env("DB_PATH", "ref.db")
	addr := ":" + env("PORT", "8080")
	baseURL := env("BASE_URL", "http://localhost:8080")

	db, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	demo := env("DEMO_MODE", "1") == "1"
	if demo {
		if err := seed(db, baseURL); err != nil {
			log.Fatalf("seed: %v", err)
		}
	}

	app := &App{
		db:      db,
		baseURL: baseURL,
		google:  newGoogleConfig(baseURL),
		demo:    demo,
		secure:  strings.HasPrefix(baseURL, "https://"),
	}
	if app.google == nil {
		log.Printf("Google sign-in is off: set GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET to turn it on")
	}
	go app.autoSendLoop()

	static, _ := fs.Sub(staticFiles, "static")
	mux := http.NewServeMux()
	app.routes(mux)
	mux.Handle("GET /", http.FileServer(http.FS(static)))

	log.Printf("listening on %s (db %s)", addr, dbPath)
	srv := &http.Server{Addr: addr, Handler: logRequests(mux), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
