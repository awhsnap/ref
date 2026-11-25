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

Sign-in and sign-up go through [Auth0](https://auth0.com/)'s hosted login page, with email and password, plus Google or other social logins if you turn them on in Auth0. The sign-in page has **Log in** and **Create an account** buttons. The first time someone signs in, they pick a plan:

- **Just me:** a solo salesperson account.
- **My dealership:** creates a dealership with the person as its manager. Settings shows an 8-letter **invite code** for the team.
- **Join my team:** a salesperson enters the manager's invite code and joins that dealership.

The app uses OpenID Connect (authorization code flow with PKCE and a state check). Accounts are matched on Auth0's user id. App sessions last 30 days in an HttpOnly cookie. Signing out ends both the app session and the Auth0 session.

**Demo mode** (`DEMO_MODE=1`, the default) adds a **Try the demo with sample data** button on the sign-in page. It opens the sample accounts with a switcher for a solo salesperson, a dealership manager and two salespeople. Sample accounts can never see or reach real accounts. Set `DEMO_MODE=0` to remove the button and turn off the sample accounts. Nothing in the demo is actually sent to anyone.

### Setting up Auth0

1. Sign up at [auth0.com](https://auth0.com/). The free plan covers this. A tenant is created for you, with a domain like `your-tenant.us.auth0.com`.
2. Go to **Applications → Applications → Create Application**, choose **Regular Web Applications**, and open its **Settings** tab.
3. Fill in these fields and save:
   - **Allowed Callback URLs:** `https://<your-app>.fly.dev/auth/callback, http://localhost:8080/auth/callback`
   - **Allowed Logout URLs:** `https://<your-app>.fly.dev/, http://localhost:8080/`
   - **Allowed Web Origins:** `https://<your-app>.fly.dev`
4. Under the application's **Connections** tab, turn on **Username-Password-Authentication** for email sign-up, and **google-oauth2** if you want "Continue with Google". For Google in production, add your own Google keys under **Authentication → Social**, because Auth0's built-in dev keys are for testing only.
5. Optionally, under **Branding → Universal Login**, add your logo and colors.
6. Copy the **Domain**, **Client ID** and **Client Secret** from the application's Settings and give them to the server:

```sh
fly secrets set AUTH0_DOMAIN=your-tenant.us.auth0.com AUTH0_CLIENT_ID=... AUTH0_CLIENT_SECRET=...
```

Locally: `AUTH0_DOMAIN=... AUTH0_CLIENT_ID=... AUTH0_CLIENT_SECRET=... go run .`

Without these settings, the server still runs, but the sign-in buttons are replaced with a note and only the demo works.

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
| `BASE_URL` | `http://localhost:8080` | Public address, used in referral links and the Auth0 callback and logout URLs. Cookies are HTTPS-only when it starts with `https://` |
| `AUTH0_DOMAIN` | (none) | Your Auth0 domain, e.g. `your-tenant.us.auth0.com` |
| `AUTH0_CLIENT_ID` | (none) | Auth0 application client ID |
| `AUTH0_CLIENT_SECRET` | (none) | Auth0 application client secret (keep it in `fly secrets`, never in `fly.toml`) |
| `DEMO_MODE` | `1` | `1` shows the sample-data demo, `0` hides it |

## Deploy to fly.io

```sh
fly launch --no-deploy --copy-config   # pick your own app name, then update app + BASE_URL in fly.toml
fly volumes create ref_data --size 1   # same region as primary_region
fly secrets set AUTH0_DOMAIN=... AUTH0_CLIENT_ID=... AUTH0_CLIENT_SECRET=...
fly deploy
```

The database lives on the `ref_data` volume at `/data/ref.db`, so it survives deploys.

### Automatic deploys

`.github/workflows/fly-deploy.yml` runs the tests on every pull request, and deploys `main` to fly.io after every merge. On its first run it also creates the app and the `ref_data` volume. It needs a repository secret named `FLY_API_TOKEN`. A deploy token from `fly tokens create deploy -a <app>` is enough once the app exists. If your fly.io organization isn't `personal`, set a repository variable `FLY_ORG`. You can also start a deploy by hand from the repo's **Actions** tab ("Run workflow").

## How it's built

- `main.go`: starts the server and serves the `static/` front end (embedded in the binary)
- `api.go`: JSON API, the referral landing page, and the simulated auto-sender
- `auth.go`: Auth0 sign-in, sessions, and first-time account setup (`auth_test.go` runs the whole flow against a fake Auth0)
- `db.go`: SQLite schema, the default follow-up plan, and the demo data
- `static/`: plain HTML, CSS and JavaScript, with no build step

SQLite comes from `modernc.org/sqlite` (pure Go), so the binary builds with `CGO_ENABLED=0`.

## Not in the demo yet

- Stripe subscriptions
- Real texting (Twilio with 10DLC registration) and email (SendGrid or Postmark)
- Meta app review for automatic Facebook Page and Instagram posting
- Spreadsheet import from dealership CRMs
