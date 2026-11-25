# Referral Desk (demo)

A clickable demo of a follow-up and referral app for car salespeople. You log every sold customer, and the app plans friendly follow-ups by text, email and social post. Each customer gets a personal referral link, so referrals come in without an awkward ask.

The calendar is the main screen. It shows every planned text, email and post, plus birthdays and new referrals, with the month's results across the top.

| Desktop (dealership manager) | Phone (Today view) |
|---|---|
| ![Month calendar](docs/calendar-month.png) | ![Today on phone](docs/today-mobile.png) |

## What's in the demo

- **Calendar:** Today, Week and Month views. Tap anything to edit the message, move the date, skip it or send it.
- **Tap to send:** opens your phone's Messages app (iMessage on iPhone) or email app with the customer and the message filled in.
- **Fully automatic mode:** simulates sending from a registered business number (Twilio in the real app).
- **Add sold customer:** schedules the whole follow-up plan, and records the customer's consent for texts, emails and a photo post.
- **Opt-outs:** unticking consent, or a simulated "STOP" reply, cancels all future messages on that channel.
- **Referral links:** each customer gets a public page at `/r/<code>`. A friend fills it out, and the lead lands under Referrals, credited to that customer.
- **Sold! posts:** solo salespeople share to their personal Facebook from their phone. Dealership posts are marked as going to the dealership's Facebook Page.
- **Follow-up plan:** edit the message templates and the days they go out. On the dealership plan, the manager controls the team's shared plan.
- **Team:** the dealership manager sees a leaderboard and can filter the calendar by salesperson.

## Accounts and sign-in

Sign-up and login are built into the app, with an email and a password, and no outside sign-in service. The first time someone logs in, they pick a plan:

- **Just me:** a solo salesperson account.
- **My dealership:** creates a dealership with the person as its manager. Settings shows an 8-letter **invite code** for the team.
- **Join my team:** a salesperson enters the manager's invite code and joins that dealership.

How accounts are protected:

- **Passwords:** stored only as bcrypt hashes (cost 12). They must be 8 to 72 characters. Emails are matched without regard to letter case.
- **Sessions:** a random 256-bit token sits in an `HttpOnly`, `SameSite=Lax` cookie, which is `Secure` when `BASE_URL` is https. Only its SHA-256 hash is stored in SQLite. Sessions last 30 days, and logging out deletes them.
- **Cross-site requests (CSRF):** every change request must come from the app's own site, checked with the `Origin` and `Sec-Fetch-Site` headers, and must be JSON, which a form on another website can't send.
- **Login rate limits:** 8 failed tries per email and 30 per IP address in 15 minutes, and 10 sign-ups per IP address per hour. Unknown emails take as long to reject as wrong passwords. The counters are kept in memory, so they reset when the app restarts.
- **Password changes:** changing your password in Settings signs you out on your other devices.

**Forgot password (for now):** there's no reset-by-email yet, because that needs an email provider. Until then:

- A **dealership manager** can reset a salesperson's password from the **Team** page. The salesperson gets a one-time password and must choose a new one when they log in.
- For anyone else (solo users, or managers), an admin runs: `fly ssh console -C "ref reset-password someone@example.com"`. It prints a one-time password that works the same way.

**Demo mode** (`DEMO_MODE=1`, the default) adds a **Try the demo with sample data** button on the login page. The sample accounts have no passwords and can never see or reach real accounts. Set `DEMO_MODE=0` to turn them off.

## Run it locally

Requires Go 1.26 or newer.

```sh
go run .
# open http://localhost:8080
```

A `ref.db` SQLite file is created and filled with sample data on first run. Delete it to start fresh.

| Setting | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | Port to listen on |
| `DB_PATH` | `ref.db` | SQLite file location |
| `BASE_URL` | `http://localhost:8080` | Public address, used in referral links. Cookies are HTTPS-only when it starts with `https://` |
| `DEMO_MODE` | `1` | `1` shows the sample-data demo, `0` hides it |

## Deploy to fly.io

```sh
fly launch --no-deploy --copy-config   # pick your own app name, then update app + BASE_URL in fly.toml
fly volumes create ref_data --size 1   # same region as primary_region
fly deploy
```

The database lives on the `ref_data` volume at `/data/ref.db`, so it survives deploys.

### Automatic deploys

`.github/workflows/fly-deploy.yml` runs the tests on every pull request, and deploys `main` to fly.io after every merge. On its first run it also creates the app and the `ref_data` volume. It needs a repository secret named `FLY_API_TOKEN`. A deploy token from `fly tokens create deploy -a <app>` is enough once the app exists. If your fly.io organization isn't `personal`, set a repository variable `FLY_ORG`. You can also start a deploy by hand from the repo's **Actions** tab ("Run workflow").

## How it's built

- `main.go`: starts the server and serves the `static/` front end (embedded in the binary)
- `api.go`: JSON API, the referral landing page, and the simulated auto-sender
- `auth.go`: sign-up, login, sessions, password reset, CSRF and rate limiting, and first-time account setup (tested in `auth_test.go`)
- `db.go`: SQLite schema, the default follow-up plan, and the demo data
- `static/`: plain HTML, CSS and JavaScript, with no build step

SQLite comes from `modernc.org/sqlite` (pure Go), so the binary builds with `CGO_ENABLED=0`.

## Not in the demo yet

- Stripe subscriptions
- Real texting (Twilio with 10DLC registration) and email (SendGrid or Postmark)
- Meta app review for automatic Facebook Page and Instagram posting
- Spreadsheet import from dealership CRMs
