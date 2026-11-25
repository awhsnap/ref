// Referral Desk demo front end. Plain JavaScript, no build step.
"use strict";

const $ = (sel, root = document) => root.querySelector(sel);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const pad = (n) => String(n).padStart(2, "0");
const ymd = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
const parseDate = (s) => { const [y, m, d] = s.split("-").map(Number); return new Date(y, m - 1, d); };
const addDays = (d, n) => { const x = new Date(d); x.setDate(x.getDate() + n); return x; };
const todayStr = () => ymd(new Date());
const fmtDate = (s, opts = { month: "short", day: "numeric" }) => parseDate(s).toLocaleDateString(undefined, opts);
const isIOS = /iPad|iPhone|iPod|Macintosh/.test(navigator.userAgent);

const ICONS = { text: "💬", email: "✉️", post: "📣", birthday: "🎂", referral: "🤝" };
const LABELS = { text: "Text", email: "Email", post: "Social post", birthday: "Birthday", referral: "Referral" };

const state = {
  user: null,
  users: [],
  view: "calendar",
  calMode: window.innerWidth < 700 ? "today" : "month",
  cursor: new Date(),
  salesperson: "",
};

// ---------- API ----------
async function api(path, opts = {}) {
  const headers = {};
  const demoUser = localStorage.getItem("demoUser");
  if (demoUser) headers["X-User"] = demoUser;
  if (opts.body && typeof opts.body !== "string") opts.body = JSON.stringify(opts.body);
  // The server only accepts JSON for changes (blocks cross-site form posts).
  if (opts.method && opts.method !== "GET") headers["Content-Type"] = "application/json";
  const res = await fetch(path, { ...opts, headers });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}
const spParam = () => (state.salesperson ? `&salesperson=${state.salesperson}` : "");

function toast(msg) {
  const t = document.createElement("div");
  t.className = "toast";
  t.textContent = msg;
  document.body.appendChild(t);
  setTimeout(() => t.remove(), 2600);
}

function modal(html, onMount) {
  closeModal();
  const bg = document.createElement("div");
  bg.className = "modal-bg";
  bg.innerHTML = `<div class="modal" role="dialog"><button class="x" aria-label="Close">×</button>${html}</div>`;
  bg.addEventListener("click", (e) => { if (e.target === bg) closeModal(); });
  $(".x", bg).onclick = closeModal;
  document.body.appendChild(bg);
  onMount?.($(".modal", bg));
}
function closeModal() { document.querySelector(".modal-bg")?.remove(); }

// ---------- boot ----------
async function boot() {
  state.cfg = await fetch("/api/auth/config").then((r) => r.json());
  document.querySelectorAll("#tabs button").forEach((b) => (b.onclick = () => go(b.dataset.view)));
  let me = null;
  try { me = await api("/api/me"); } catch { localStorage.removeItem("demoUser"); }
  if (!me) return renderSignIn();
  state.user = me;
  route();
}

// route picks the right screen for a signed-in user.
function route() {
  if (state.user.must_change_password) return renderForcePassword();
  if (!state.user.onboarded) return renderOnboarding();
  startApp();
}

function showChrome(on) {
  $("#tabs").hidden = !on;
}

async function startApp() {
  showChrome(true);
  const acct = $("#account");
  if (state.user.demo) {
    state.users = await api("/api/users");
    acct.innerHTML = `<span class="pill">Demo</span>
      <select id="user-switch" aria-label="Demo account">${state.users.map((u) =>
        `<option value="${u.id}">${esc(u.name)} · ${u.role === "solo" ? "Solo salesperson" : u.role === "manager" ? "Dealership manager" : "Dealership salesperson"}</option>`).join("")}</select>
      <button class="btn small" id="exit-demo">Exit demo</button>`;
    $("#user-switch").value = state.user.id;
    $("#user-switch").onchange = () => setUser(Number($("#user-switch").value));
    $("#exit-demo").onclick = () => { localStorage.removeItem("demoUser"); location.href = "/"; };
  } else {
    state.users = [state.user];
    if (state.user.role === "manager") {
      // The salesperson filter needs the team list.
      const team = await api("/api/team");
      state.users = team.map((t) => ({ id: t.id, name: t.name, dealership: state.user.dealership }));
    }
    acct.innerHTML = `${state.user.picture ? `<img class="avatar" src="${esc(state.user.picture)}" alt="" referrerpolicy="no-referrer">` : ""}
      <span class="small"><b>${esc(state.user.name)}</b></span>
      <button class="btn small" id="sign-out">Sign out</button>`;
    $("#sign-out").onclick = signOut;
  }
  afterUserChange();
}

async function signOut() {
  localStorage.removeItem("demoUser");
  await fetch("/auth/logout", { method: "POST", headers: { "Content-Type": "application/json" } }).catch(() => {});
  location.href = "/";
}

async function setUser(id) {
  closeModal();
  localStorage.setItem("demoUser", id);
  state.user = await api("/api/me");
  afterUserChange();
}

function afterUserChange() {
  state.salesperson = "";
  $("#tabs [data-view=team]").hidden = state.user.role !== "manager";
  if (state.view === "team" && state.user.role !== "manager") state.view = "calendar";
  render();
}

function renderSignIn(mode = "login") {
  showChrome(false);
  $("#account").innerHTML = "";
  const signup = mode === "signup";
  $("#view").innerHTML = `<div class="auth-card card">
    <h1>Never forget a sold customer again.</h1>
    <p class="muted">Referral Desk plans your follow-up texts, emails and Sold! posts, so referrals come in without the awkward ask.</p>
    <div class="seg auth-tabs">
      <button data-mode="login" class="${signup ? "" : "active"}">Log in</button>
      <button data-mode="signup" class="${signup ? "active" : ""}">Create an account</button>
    </div>
    <form id="auth-form" style="text-align:left">
      ${signup ? `<label>Your name<input name="name" autocomplete="name" required maxlength="80"></label>` : ""}
      <label>Email<input name="email" type="email" autocomplete="email" required></label>
      <label>Password<input name="password" type="password" required minlength="8" maxlength="72"
        autocomplete="${signup ? "new-password" : "current-password"}">${signup ? `<span class="muted small">At least 8 characters.</span>` : ""}</label>
      <p class="auth-error" id="auth-err" hidden></p>
      <button class="btn primary" type="submit" style="width:100%;justify-content:center">${signup ? "Create my account" : "Log in"}</button>
      ${signup ? "" : `<p class="muted small">Forgot your password? Ask your dealership manager to reset it from the Team page, or contact support.</p>`}
    </form>
    ${state.cfg.demo ? `<hr><button class="btn" id="try-demo">Try the demo with sample data</button>` : ""}
  </div>`;
  document.querySelectorAll(".auth-tabs [data-mode]").forEach((b) => (b.onclick = () => renderSignIn(b.dataset.mode)));
  $("#auth-form").onsubmit = async (ev) => {
    ev.preventDefault();
    const btn = ev.target.querySelector("button[type=submit]");
    btn.disabled = true;
    try {
      state.user = await api(signup ? "/auth/register" : "/auth/login", { method: "POST", body: Object.fromEntries(new FormData(ev.target)) });
      route();
    } catch (e) {
      $("#auth-err").textContent = e.message;
      $("#auth-err").hidden = false;
      btn.disabled = false;
    }
  };
  $("#try-demo") && ($("#try-demo").onclick = () => { localStorage.setItem("demoUser", "1"); location.reload(); });
}

function renderForcePassword() {
  showChrome(false);
  $("#account").innerHTML = `<button class="btn small" id="sign-out">Sign out</button>`;
  $("#sign-out").onclick = signOut;
  $("#view").innerHTML = `<div class="auth-card card" style="text-align:left">
    <h2>Choose a new password</h2>
    <p class="muted small">You signed in with a temporary password. Pick your own to continue.</p>
    <form id="pw-form">
      <label>New password<input name="new" type="password" autocomplete="new-password" required minlength="8" maxlength="72"></label>
      <p class="auth-error" id="pw-err" hidden></p>
      <button class="btn primary" type="submit">Save and continue</button>
    </form>
  </div>`;
  $("#pw-form").onsubmit = async (ev) => {
    ev.preventDefault();
    try {
      state.user = await api("/api/password", { method: "POST", body: { new: ev.target.new.value } });
      route();
    } catch (e) { $("#pw-err").textContent = e.message; $("#pw-err").hidden = false; }
  };
}

function renderOnboarding() {
  showChrome(false);
  $("#account").innerHTML = `<button class="btn small" id="sign-out">Sign out</button>`;
  $("#sign-out").onclick = signOut;
  const u = state.user;
  $("#view").innerHTML = `<div class="auth-card card" style="text-align:left">
    <h2>Welcome! Let's set up your account.</h2>
    <form id="ob">
      <div class="plans">
        <label class="plan"><input type="radio" name="plan" value="solo" checked><b>Just me</b><span>Solo salesperson · $29/month</span></label>
        <label class="plan"><input type="radio" name="plan" value="dealership"><b>My dealership</b><span>I'm a manager setting up the team · $35/seat</span></label>
        <label class="plan"><input type="radio" name="plan" value="join"><b>Join my team</b><span>My manager gave me an invite code</span></label>
      </div>
      <label>Your name<input name="name" value="${esc(u.name)}" required></label>
      <label>Mobile phone<input name="phone" type="tel" placeholder="+1 555 555 0123"></label>
      <label data-for="solo">Dealership you work at<input name="workplace" placeholder="e.g. Bayside Auto"></label>
      <label data-for="dealership" hidden>Dealership name<input name="dealership_name" placeholder="e.g. Sunset Motors"></label>
      <label data-for="join" hidden>Invite code<input name="invite_code" placeholder="8 letters, e.g. K7PQ2M9X" style="text-transform:uppercase"></label>
      <p class="auth-error" id="ob-err" hidden></p>
      <button class="btn primary" type="submit">Start using Referral Desk</button>
    </form>
  </div>`;
  const form = $("#ob");
  const sync = () => {
    const plan = form.plan.value;
    form.querySelectorAll("[data-for]").forEach((el) => (el.hidden = el.dataset.for !== plan));
  };
  form.querySelectorAll("[name=plan]").forEach((r) => (r.onchange = sync));
  form.onsubmit = async (ev) => {
    ev.preventDefault();
    const body = Object.fromEntries(new FormData(form));
    try {
      state.user = await api("/api/onboarding", { method: "POST", body });
      startApp();
      if (state.user.role === "manager") toast(`Team invite code: ${state.user.invite_code}`);
    } catch (e) {
      $("#ob-err").textContent = e.message;
      $("#ob-err").hidden = false;
    }
  };
}

function go(view) { state.view = view; render(); }

function render() {
  document.querySelectorAll("#tabs button").forEach((b) => b.classList.toggle("active", b.dataset.view === state.view));
  const views = { calendar: renderCalendar, customers: renderCustomers, referrals: renderReferrals, templates: renderTemplates, team: renderTeam, settings: renderSettings };
  views[state.view]().catch((e) => ($("#view").innerHTML = `<div class="card">Something went wrong: ${esc(e.message)}</div>`));
}

function banner() {
  if (!state.user.demo) return "";
  return `<div class="banner">Demo with sample data. Texts, emails and posts are simulated, nothing is actually sent to customers.</div>`;
}

async function salespersonFilter() {
  if (state.user.role !== "manager") return "";
  const team = state.users.filter((u) => u.dealership === state.user.dealership);
  return `<select id="sp-filter" style="width:auto">
    <option value="">Whole team</option>
    ${team.map((u) => `<option value="${u.id}" ${String(u.id) === String(state.salesperson) ? "selected" : ""}>${esc(u.name)}</option>`).join("")}
  </select>`;
}
function bindFilter() {
  const f = $("#sp-filter");
  if (f) f.onchange = () => { state.salesperson = f.value; render(); };
}

// ---------- calendar ----------
async function renderCalendar() {
  const c = state.cursor;
  let from, to, title;
  if (state.calMode === "today") {
    from = addDays(new Date(), -14); to = addDays(new Date(), 7);
    title = new Date().toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric" });
  } else if (state.calMode === "week") {
    from = addDays(c, -c.getDay()); to = addDays(from, 6);
    title = `${fmtDate(ymd(from))} – ${fmtDate(ymd(to))}`;
  } else {
    const first = new Date(c.getFullYear(), c.getMonth(), 1);
    from = addDays(first, -first.getDay()); to = addDays(from, 41);
    title = c.toLocaleDateString(undefined, { month: "long", year: "numeric" });
  }
  const month = state.calMode === "today" ? todayStr().slice(0, 7) : ymd(c).slice(0, 7);
  const [events, stats] = await Promise.all([
    api(`/api/calendar?from=${ymd(from)}&to=${ymd(to)}${spParam()}`),
    api(`/api/stats?month=${month}${spParam()}`),
  ]);
  state.events = events;

  const replyRate = stats.sent ? Math.round((stats.replies / stats.sent) * 100) : 0;
  const monthName = parseDate(month + "-01").toLocaleDateString(undefined, { month: "long" });
  let html = banner() + `
  <div class="stats">
    <div class="stat"><b>${stats.sent}</b><span>Sent in ${monthName}</span></div>
    <div class="stat"><b>${stats.scheduled}</b><span>Still scheduled</span></div>
    <div class="stat"><b>${replyRate}%</b><span>Reply rate (${stats.replies} replies)</span></div>
    <div class="stat"><b>${stats.referrals}</b><span>Referrals in</span></div>
    <div class="stat"><b>${stats.referral_sales}</b><span>Referral sales</span></div>
    <div class="stat ${stats.overdue ? "warn" : ""}"><b>${stats.overdue}</b><span>Overdue to send</span></div>
  </div>
  <div class="row cal-head">
    <div class="seg">
      ${["today", "week", "month"].map((m) => `<button data-mode="${m}" class="${state.calMode === m ? "active" : ""}">${m[0].toUpperCase() + m.slice(1)}</button>`).join("")}
    </div>
    ${state.calMode !== "today" ? `<button class="btn small" id="prev" aria-label="Previous">‹</button><div class="cal-title">${esc(title)}</div><button class="btn small" id="next" aria-label="Next">›</button><button class="btn small" id="now">Today</button>` : `<div class="cal-title" style="text-align:left">${esc(title)}</div>`}
    <div class="spacer"></div>
    ${await salespersonFilter()}
    <button class="btn primary" id="add-cust">+ Add sold customer</button>
  </div>
  <div class="legend" style="margin-bottom:10px">
    ${["text", "email", "post", "birthday", "referral"].map((t) => `<span class="t-${t}"><i></i>${LABELS[t]}</span>`).join("")}
  </div>`;

  if (state.calMode === "today") html += todayList(events);
  else if (state.calMode === "week") html += weekGrid(from, events);
  else html += monthGrid(from, c, events);

  $("#view").innerHTML = html;
  document.querySelectorAll("[data-mode]").forEach((b) => (b.onclick = () => { state.calMode = b.dataset.mode; state.cursor = new Date(); render(); }));
  const step = state.calMode === "week" ? 7 : 0;
  $("#prev") && ($("#prev").onclick = () => { state.cursor = step ? addDays(c, -7) : new Date(c.getFullYear(), c.getMonth() - 1, 1); render(); });
  $("#next") && ($("#next").onclick = () => { state.cursor = step ? addDays(c, 7) : new Date(c.getFullYear(), c.getMonth() + 1, 1); render(); });
  $("#now") && ($("#now").onclick = () => { state.cursor = new Date(); render(); });
  $("#add-cust").onclick = openAddCustomer;
  bindFilter();
  bindEventClicks($("#view"));
}

function bindEventClicks(root, list = state.events) {
  root.querySelectorAll("[data-ev]").forEach((el) => (el.onclick = (e) => {
    e.stopPropagation();
    openEvent(list[Number(el.dataset.ev)]);
  }));
  root.querySelectorAll("[data-day]").forEach((el) => (el.onclick = () => openDay(el.dataset.day)));
  root.querySelectorAll("[data-send]").forEach((el) => (el.onclick = (e) => sendAction(state.events[Number(el.dataset.send)], e)));
  root.querySelectorAll("[data-skip]").forEach((el) => (el.onclick = async () => {
    await api(`/api/messages/${state.events[Number(el.dataset.skip)].id}`, { method: "PATCH", body: { status: "skipped" } });
    toast("Skipped"); render();
  }));
}

const evIndex = (e) => state.events.indexOf(e);
const chip = (e) => `<div class="chip t-${e.type} ${e.status || ""}" data-ev="${evIndex(e)}" title="${esc(e.customer_name + " · " + e.title)}">${ICONS[e.type]} ${esc(e.customer_name.split(" ")[0])} · ${esc(e.title)}</div>`;

function byDate(events) {
  const m = {};
  for (const e of events) (m[e.date] ||= []).push(e);
  return m;
}

function monthGrid(from, cursor, events) {
  const days = byDate(events);
  let html = `<div class="month">${["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"].map((d) => `<div class="dow">${d}</div>`).join("")}`;
  for (let i = 0; i < 42; i++) {
    const d = addDays(from, i), key = ymd(d), list = days[key] || [];
    const shown = list.slice(0, 3);
    const sent = list.filter((e) => e.status === "sent").length;
    const replies = list.filter((e) => e.reply).length;
    const refs = list.filter((e) => e.type === "referral").length;
    html += `<div class="day ${d.getMonth() !== cursor.getMonth() ? "other" : ""} ${key === todayStr() ? "today" : ""}" data-day="${key}">
      <div class="dnum">${d.getDate()}</div>
      ${shown.map(chip).join("")}
      ${list.length > 3 ? `<div class="more">+${list.length - 3} more</div>` : ""}
      ${sent || replies || refs ? `<div class="dayres">${sent ? `✓${sent} sent` : ""}${replies ? ` · ${replies} 💬` : ""}${refs ? ` · ${refs} 🤝` : ""}</div>` : ""}
    </div>`;
  }
  return html + `</div>`;
}

function weekGrid(from, events) {
  const days = byDate(events);
  let html = `<div class="week">`;
  for (let i = 0; i < 7; i++) {
    const d = addDays(from, i), key = ymd(d), list = days[key] || [];
    html += `<div class="col ${key === todayStr() ? "today" : ""}">
      <h4>${d.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" })}</h4>
      ${list.map(chip).join("") || `<div class="muted small" style="text-align:center">Nothing planned</div>`}
    </div>`;
  }
  return html + `</div>`;
}

function taskCard(e) {
  const i = evIndex(e);
  const done = e.status === "sent" || e.status === "skipped";
  let actions = "";
  if (e.type === "birthday") actions = `<button class="btn small primary" data-send="${i}">🎂 Send birthday text</button>`;
  else if (e.type === "referral") actions = `<button class="btn small" data-ev="${i}">View referral</button>`;
  else if (!done) actions = `${sendButtonLabel(e, i)}<button class="btn small" data-ev="${i}">Edit</button><button class="btn small" data-skip="${i}">Skip</button>`;
  else actions = `<span class="pill ${e.status}">${e.status === "sent" ? "Sent" : "Skipped"}</span>${e.skip_reason ? `<span class="muted small">${esc(e.skip_reason)}</span>` : ""}`;
  return `<div class="task t-${e.type} ${done ? "done" : ""}">
    <div class="ico">${ICONS[e.type]}</div>
    <div class="main">
      <div class="row"><span class="who">${esc(e.customer_name)}</span><span class="muted small">${esc(e.title)}${state.user.role === "manager" ? " · " + esc(e.salesperson) : ""}</span></div>
      ${e.body ? `<div class="body">${esc(e.body)}</div>` : ""}
      ${e.reply ? `<div class="reply">↩︎ ${esc(e.reply)}</div>` : ""}
      <div class="row" style="margin-top:6px">${actions}</div>
    </div>
  </div>`;
}

function sendButtonLabel(e, i) {
  if (e.type === "text") return state.user.send_mode === "auto"
    ? `<button class="btn small primary" data-send="${i}">Send now</button>`
    : `<button class="btn small primary" data-send="${i}">📱 Tap to text</button>`;
  if (e.type === "email") return `<button class="btn small primary" data-send="${i}">✉️ Send email</button>`;
  if (e.type === "post") return `<button class="btn small primary" data-send="${i}">📣 Share post</button>`;
  return "";
}

function todayList(events) {
  const t = todayStr();
  const overdue = events.filter((e) => e.date < t && e.status === "scheduled");
  const today = events.filter((e) => e.date === t)
    .sort((a, b) => (a.status && a.status !== "scheduled") - (b.status && b.status !== "scheduled"));
  const soon = events.filter((e) => e.date > t && e.type !== "referral");
  let html = "";
  if (overdue.length) html += `<h3>⚠️ Overdue (${overdue.length})</h3>` + overdue.map(taskCard).join("");
  html += `<h3>Today (${today.length})</h3>` + (today.length ? today.map(taskCard).join("") : `<div class="card empty">Nothing due today. Nice work!</div>`);
  if (soon.length) html += `<h3>Coming up this week</h3><div class="card">` + soon.map((e) =>
    `<div class="row small" style="padding:4px 0;cursor:pointer" data-ev="${evIndex(e)}"><span class="pill">${fmtDate(e.date, { weekday: "short", month: "short", day: "numeric" })}</span>${ICONS[e.type]} <b>${esc(e.customer_name)}</b> <span class="muted">${esc(e.title)}</span></div>`).join("") + `</div>`;
  return html;
}

function openDay(date) {
  const list = state.events.filter((e) => e.date === date);
  modal(`<h2>${fmtDate(date, { weekday: "long", month: "long", day: "numeric" })}</h2>
    ${list.length ? list.map(taskCard).join("") : `<div class="empty">Nothing planned this day.</div>`}`,
    (m) => bindEventClicks(m));
}

// ---------- sending ----------
function smsLink(phone, body) { return `sms:${phone}${isIOS ? "&" : "?"}body=${encodeURIComponent(body)}`; }

async function markSent(id) { await api(`/api/messages/${id}`, { method: "PATCH", body: { status: "sent" } }); }

async function sendAction(e, evt) {
  evt?.stopPropagation();
  if (e.type === "birthday") {
    const body = `Happy birthday, ${e.customer_name.split(" ")[0]}! 🎂 Hope you have a great day. – ${state.user.name.split(" ")[0]}`;
    window.location.href = smsLink(e.phone, body);
    return;
  }
  if (e.type === "text") {
    if (state.user.send_mode === "tap") window.location.href = smsLink(e.phone, e.body);
    await markSent(e.id);
    toast(state.user.send_mode === "tap" ? "Opened Messages. Marked as sent." : "Sent from your business number (simulated)");
  } else if (e.type === "email") {
    window.location.href = `mailto:${e.email}?subject=${encodeURIComponent(e.title)}&body=${encodeURIComponent(e.body)}`;
    await markSent(e.id);
    toast("Opened your email app. Marked as sent.");
  } else if (e.type === "post") {
    return openPost(e);
  }
  closeModal();
  render();
}

function openPost(e) {
  const dealer = state.user.role !== "solo";
  modal(`<h2>📣 Sold! post</h2>
    <p class="muted small">${dealer
      ? `Dealership accounts post to <b>${esc(state.user.facebook_page || "the dealership Facebook Page")}</b> automatically once Meta approves the app. Until then, share it from your phone.`
      : "Solo accounts share to your personal Facebook or Instagram from your phone. Attach your delivery photo in the next step."}</p>
    <div class="post-preview"><div class="photo">🚗📸</div><p id="cap">${esc(e.body)}</p></div>
    <div class="row" style="margin-top:12px">
      <button class="btn primary" id="share">Share from my phone</button>
      <button class="btn" id="copy">Copy caption</button>
      <button class="btn" id="done">Mark as posted</button>
    </div>`, () => {
    $("#share").onclick = async () => {
      if (navigator.share) { try { await navigator.share({ text: e.body }); } catch { return; } }
      else { await navigator.clipboard?.writeText(e.body); toast("Caption copied. Paste it into Facebook."); }
      await markSent(e.id); closeModal(); render();
    };
    $("#copy").onclick = async () => { await navigator.clipboard?.writeText(e.body); toast("Caption copied"); };
    $("#done").onclick = async () => { await markSent(e.id); closeModal(); toast("Marked as posted"); render(); };
  });
}

function openEvent(e) {
  if (!e) return;
  if (e.type === "referral") { go("referrals"); return; }
  if (e.type === "birthday") {
    const i = evIndex(e);
    modal(`<h2>🎂 ${esc(e.customer_name)}'s birthday</h2><p>${fmtDate(e.date, { weekday: "long", month: "long", day: "numeric" })}</p>
      <button class="btn primary" data-send="${i}">Send birthday text</button>
      <button class="btn" data-cust="${e.customer_id}">Open customer</button>`, (m) => {
      bindEventClicks(m);
      $("[data-cust]", m).onclick = () => openCustomer(e.customer_id);
    });
    return;
  }
  const scheduled = e.status === "scheduled";
  const i = state.events.includes(e) ? evIndex(e) : -1;
  modal(`<h2>${ICONS[e.type]} ${esc(e.title)}</h2>
    <p class="row"><b>${esc(e.customer_name || "")}</b><span class="pill">${LABELS[e.type]}</span><span class="pill ${e.status}">${esc(e.status)}</span>${e.skip_reason ? `<span class="muted small">${esc(e.skip_reason)}</span>` : ""}</p>
    <label>Message<textarea id="m-body" ${scheduled ? "" : "readonly"}>${esc(e.body)}</textarea></label>
    <label>Date<input type="date" id="m-date" value="${e.date}" ${scheduled ? "" : "readonly"}></label>
    ${e.reply ? `<p><span class="reply">↩︎ Customer replied: ${esc(e.reply)}</span></p>` : ""}
    <div class="row">
      ${scheduled ? `<button class="btn primary" id="m-save">Save changes</button>${i >= 0 ? sendButtonLabel(e, i) : ""}<button class="btn" id="m-skip">Skip</button>` : `<button class="btn" id="m-restore">Put back on schedule</button>`}
      <div class="spacer"></div>
      ${e.status === "sent" && e.type === "text" ? `<button class="btn small" id="m-reply">Simulate customer reply</button>` : ""}
      ${e.customer_id ? `<button class="btn small" id="m-cust">Open customer</button>` : ""}
    </div>`, (m) => {
    bindEventClicks(m);
    const after = async (msg) => { toast(msg); closeModal(); render(); };
    $("#m-save") && ($("#m-save").onclick = async () => {
      await api(`/api/messages/${e.id}`, { method: "PATCH", body: { body: $("#m-body").value, scheduled_date: $("#m-date").value } });
      after("Saved");
    });
    $("#m-skip") && ($("#m-skip").onclick = async () => { await api(`/api/messages/${e.id}`, { method: "PATCH", body: { status: "skipped" } }); after("Skipped"); });
    $("#m-restore") && ($("#m-restore").onclick = async () => { await api(`/api/messages/${e.id}`, { method: "PATCH", body: { status: "scheduled" } }); after("Back on the schedule"); });
    $("#m-reply") && ($("#m-reply").onclick = async () => {
      const text = prompt("What did the customer reply? (Try STOP to see the opt-out)");
      if (!text) return;
      const r = await api(`/api/messages/${e.id}/reply`, { method: "POST", body: { text } });
      after(r.opted_out ? "Customer opted out. All future texts cancelled." : "Reply logged");
    });
    $("#m-cust") && ($("#m-cust").onclick = () => openCustomer(e.customer_id));
  });
}

// ---------- customers ----------
async function renderCustomers() {
  const q = state.custQuery || "";
  const list = await api(`/api/customers?q=${encodeURIComponent(q)}${spParam()}`);
  const mgr = state.user.role === "manager";
  $("#view").innerHTML = banner() + `
    <div class="row" style="margin-bottom:12px">
      <h2 style="margin:0">Sold customers</h2><div class="spacer"></div>
      ${await salespersonFilter()}
      <input id="cq" placeholder="Search name or vehicle" value="${esc(q)}" style="max-width:220px">
      <button class="btn primary" id="add-cust">+ Add sold customer</button>
    </div>
    <div class="card" style="padding:0;overflow-x:auto">
      <table>
        <thead><tr><th>Customer</th><th>Vehicle</th><th>Delivered</th>${mgr ? "<th>Salesperson</th>" : ""}<th>Consent</th></tr></thead>
        <tbody>${list.map((c) => `<tr data-id="${c.id}" style="cursor:pointer">
          <td><b>${esc(c.first_name)} ${esc(c.last_name)}</b><div class="muted small">${esc(c.phone)}</div></td>
          <td>${esc(c.vehicle)}</td><td>${fmtDate(c.delivery_date, { month: "short", day: "numeric", year: "numeric" })}</td>
          ${mgr ? `<td>${esc(c.salesperson)}</td>` : ""}
          <td class="small">${c.sms_opt_in ? "💬" : "<s>💬</s>"} ${c.email_opt_in ? "✉️" : "<s>✉️</s>"} ${c.photo_opt_in ? "📸" : "<s>📸</s>"}</td>
        </tr>`).join("") || `<tr><td colspan="5" class="empty">No customers yet</td></tr>`}</tbody>
      </table>
    </div>`;
  const cq = $("#cq");
  cq.oninput = () => { clearTimeout(state.qt); state.qt = setTimeout(() => { state.custQuery = cq.value; render(); setTimeout(() => { const n = $("#cq"); n.focus(); n.setSelectionRange(n.value.length, n.value.length); }); }, 300); };
  $("#add-cust").onclick = openAddCustomer;
  bindFilter();
  document.querySelectorAll("tr[data-id]").forEach((tr) => (tr.onclick = () => openCustomer(Number(tr.dataset.id))));
}

function openAddCustomer() {
  modal(`<h2>Add a sold customer</h2>
    <form id="cf">
      <div class="grid2">
        <label>First name<input name="first_name" required></label>
        <label>Last name<input name="last_name"></label>
        <label>Mobile phone<input name="phone" type="tel" placeholder="+1 555 555 0123"></label>
        <label>Email<input name="email" type="email"></label>
        <label>Vehicle<input name="vehicle" placeholder="2026 Toyota Tacoma"></label>
        <label>Delivery date<input name="delivery_date" type="date" value="${todayStr()}"></label>
        <label>Birthday (optional)<input name="birthday" type="date"></label>
      </div>
      <h3>Customer agreed to…</h3>
      <label class="check"><input type="checkbox" name="sms_opt_in" checked> Receive follow-up texts from me (required by law for automated texts)</label>
      <label class="check"><input type="checkbox" name="email_opt_in" checked> Receive follow-up emails</label>
      <label class="check"><input type="checkbox" name="photo_opt_in"> A "Sold!" photo post with their first name</label>
      <p class="muted small">We save when and how consent was given. Customers can reply STOP anytime.</p>
      <button class="btn primary" type="submit">Save and schedule follow-ups</button>
    </form>`, () => {
    $("#cf").onsubmit = async (ev) => {
      ev.preventDefault();
      const f = new FormData(ev.target);
      const body = Object.fromEntries(["first_name", "last_name", "phone", "email", "vehicle", "delivery_date", "birthday"].map((k) => [k, f.get(k).trim()]));
      for (const k of ["sms_opt_in", "email_opt_in", "photo_opt_in"]) body[k] = f.get(k) === "on";
      try {
        const c = await api("/api/customers", { method: "POST", body });
        toast(`${c.first_name} added. Follow-ups are on the calendar.`);
        openCustomer(c.id);
        render();
      } catch (err) { toast(err.message); }
    };
  });
}

async function openCustomer(id) {
  const d = await api(`/api/customers/${id}`);
  const c = d.customer;
  const consent = (k, label) => `<label class="check"><input type="checkbox" data-consent="${k}" ${c[k] ? "checked" : ""}> ${label}</label>`;
  const msgs = d.messages.map((m) => ({ ...m, customer_id: c.id, customer_name: `${c.first_name} ${c.last_name}`, phone: c.phone, email: c.email }));
  modal(`<h2>${esc(c.first_name)} ${esc(c.last_name)}</h2>
    <p class="muted">${esc(c.vehicle)} · delivered ${fmtDate(c.delivery_date, { month: "long", day: "numeric", year: "numeric" })}${state.user.role === "manager" ? " · " + esc(c.salesperson) : ""}<br>
    ${esc(c.phone)} ${c.email ? "· " + esc(c.email) : ""}</p>
    <h3>Referral link</h3>
    <div class="row"><input readonly value="${esc(d.referral_link)}" id="rl" style="flex:1"><button class="btn small" id="copy-rl">Copy</button><a class="btn small" href="${esc(d.referral_link)}" target="_blank">Open</a></div>
    ${d.referrals.length ? `<p class="small">🤝 ${d.referrals.length} referral${d.referrals.length > 1 ? "s" : ""}: ${d.referrals.map((r) => esc(r.name) + ` <span class="pill ${r.status}">${r.status}</span>`).join(", ")}</p>` : ""}
    <h3>Consent</h3>
    ${consent("sms_opt_in", "Texts")}${consent("email_opt_in", "Emails")}${consent("photo_opt_in", "Sold! photo post")}
    ${c.consent_at ? `<p class="muted small">Recorded ${esc(c.consent_at)}</p>` : ""}
    <h3>Follow-up plan</h3>
    <ul class="timeline">${msgs.map((m, i) => `<li data-ev="${i}" style="cursor:pointer">
      <div class="date">${fmtDate(m.date, { month: "short", day: "numeric", year: "2-digit" })}</div>
      <div style="flex:1">${ICONS[m.type]} ${esc(m.title)} <span class="pill ${m.status}">${m.status}</span>
        ${m.skip_reason ? `<span class="muted small">${esc(m.skip_reason)}</span>` : ""}
        ${m.reply ? `<div class="reply">↩︎ ${esc(m.reply)}</div>` : ""}</div></li>`).join("")}</ul>`, (m) => {
    bindEventClicks(m, msgs);
    $("#copy-rl").onclick = async () => { await navigator.clipboard?.writeText(d.referral_link); toast("Link copied"); };
    m.querySelectorAll("[data-consent]").forEach((cb) => (cb.onchange = async () => {
      await api(`/api/customers/${id}`, { method: "PATCH", body: { [cb.dataset.consent]: cb.checked } });
      toast(cb.checked ? "Consent saved. Upcoming messages are back on." : "Opted out. Upcoming messages cancelled.");
      openCustomer(id);
    }));
  });
}

// ---------- referrals ----------
async function renderReferrals() {
  const list = await api(`/api/referrals?x=1${spParam()}`);
  const mgr = state.user.role === "manager";
  $("#view").innerHTML = banner() + `
    <div class="row" style="margin-bottom:12px"><h2 style="margin:0">Referrals</h2><div class="spacer"></div>${await salespersonFilter()}</div>
    <p class="muted small">New leads land here when a friend fills out a customer's personal referral link.</p>
    <div class="card" style="padding:0;overflow-x:auto"><table>
      <thead><tr><th>Lead</th><th>Looking for</th><th>Referred by</th>${mgr ? "<th>Salesperson</th>" : ""}<th>Date</th><th>Status</th></tr></thead>
      <tbody>${list.map((r) => `<tr>
        <td><b>${esc(r.name)}</b><div class="muted small">${esc(r.phone)} ${esc(r.email)}</div></td>
        <td>${esc(r.interest)}</td>
        <td><a href="#" data-cust="${r.customer_id}">${esc(r.referred_by)}</a></td>
        ${mgr ? `<td>${esc(r.salesperson)}</td>` : ""}
        <td>${fmtDate(r.date)}</td>
        <td><select data-ref="${r.id}" style="width:auto">${["new", "contacted", "sold"].map((s) => `<option ${s === r.status ? "selected" : ""}>${s}</option>`).join("")}</select></td>
      </tr>`).join("") || `<tr><td colspan="6" class="empty">No referrals yet. They'll show up here.</td></tr>`}</tbody>
    </table></div>`;
  bindFilter();
  document.querySelectorAll("[data-ref]").forEach((s) => (s.onchange = async () => {
    await api(`/api/referrals/${s.dataset.ref}`, { method: "PATCH", body: { status: s.value } });
    toast(s.value === "sold" ? "🎉 Referral sale!" : "Updated");
  }));
  document.querySelectorAll("[data-cust]").forEach((a) => (a.onclick = (e) => { e.preventDefault(); openCustomer(Number(a.dataset.cust)); }));
}

// ---------- templates ----------
async function renderTemplates() {
  const { templates, can_edit } = await api("/api/templates");
  $("#view").innerHTML = banner() + `
    <h2>Follow-up plan</h2>
    <p class="muted small">These messages are scheduled for every new sold customer, counted from the delivery day.
    Blanks like <code>{first_name}</code>, <code>{vehicle}</code>, <code>{salesperson}</code>, <code>{dealership}</code> and <code>{referral_link}</code> fill in automatically.
    ${can_edit ? "" : "<br><b>Your dealership manager controls the shared plan.</b>"}</p>
    ${templates.map((t) => `<div class="card t-${t.channel}" style="margin-bottom:10px;border-left:4px solid var(--c)" data-t="${t.id}">
      <div class="row">
        <span style="font-size:18px">${ICONS[t.channel]}</span>
        <b>Day</b><input type="number" min="0" max="3650" value="${t.offset_days}" data-f="offset_days" style="width:80px" ${can_edit ? "" : "disabled"}>
        <input value="${esc(t.title)}" data-f="title" style="flex:1;min-width:160px" ${can_edit ? "" : "disabled"}>
        <span class="pill">${LABELS[t.channel]}</span>
        <label class="check" style="margin:0"><input type="checkbox" data-f="active" ${t.active ? "checked" : ""} ${can_edit ? "" : "disabled"}> On</label>
      </div>
      <textarea data-f="body" style="margin-top:8px" ${can_edit ? "" : "disabled"}>${esc(t.body)}</textarea>
      ${can_edit ? `<div class="row" style="margin-top:6px"><div class="spacer"></div><button class="btn small primary" data-save="${t.id}">Save</button></div>` : ""}
    </div>`).join("")}
    <p class="muted small">Changes apply to customers added from now on.</p>`;
  document.querySelectorAll("[data-save]").forEach((b) => (b.onclick = async () => {
    const card = b.closest("[data-t]");
    const v = (f) => card.querySelector(`[data-f=${f}]`);
    try {
      await api(`/api/templates/${b.dataset.save}`, { method: "PATCH", body: { title: v("title").value, body: v("body").value, offset_days: Number(v("offset_days").value), active: v("active").checked } });
      toast("Saved");
    } catch (e) { toast(e.message); }
  }));
}

// ---------- team ----------
async function renderTeam() {
  const team = await api("/api/team");
  const canReset = !state.user.demo;
  $("#view").innerHTML = banner() + `
    <h2>${esc(state.user.dealership)} team</h2>
    <p class="muted small">Ranked by referral sales, then referrals in. Tip: pick a salesperson on the Calendar to see their schedule.</p>
    <div class="card" style="padding:0;overflow-x:auto"><table>
      <thead><tr><th>#</th><th>Salesperson</th><th>Customers</th><th>Messages sent</th><th>Replies</th><th>Overdue</th><th>Referrals</th><th>Referral sales</th>${canReset ? "<th></th>" : ""}</tr></thead>
      <tbody>${team.map((t, i) => `<tr><td class="lb-rank">${i + 1}</td><td><b>${esc(t.name)}</b>${t.role === "manager" ? ' <span class="pill">Manager</span>' : ""}</td>
        <td>${t.customers}</td><td>${t.sent}</td><td>${t.replies}</td><td>${t.overdue ? `<b style="color:var(--danger)">${t.overdue}</b>` : 0}</td><td>${t.referrals}</td><td><b>${t.referral_sales}</b></td>
        ${canReset ? `<td>${t.id !== state.user.id ? `<button class="btn small" data-reset="${t.id}" data-name="${esc(t.name)}">Reset password</button>` : ""}</td>` : ""}</tr>`).join("")}</tbody>
    </table></div>`;
  document.querySelectorAll("[data-reset]").forEach((b) => (b.onclick = () => {
    modal(`<h2>Reset ${esc(b.dataset.name)}'s password?</h2>
      <p class="small">They'll be signed out everywhere and get a one-time password to give them. They choose a new one when they log in.</p>
      <button class="btn primary" id="do-reset">Reset password</button>`, () => {
      $("#do-reset").onclick = async () => {
        try {
          const r = await api(`/api/team/${b.dataset.reset}/reset-password`, { method: "POST" });
          modal(`<h2>One-time password</h2>
            <p class="small">Give this to ${esc(b.dataset.name)}. It won't be shown again.</p>
            <div class="row"><code class="invite">${esc(r.temp_password)}</code><button class="btn small" id="copy-temp">Copy</button></div>`, () => {
            $("#copy-temp").onclick = async () => { await navigator.clipboard?.writeText(r.temp_password); toast("Copied"); };
          });
        } catch (e) { toast(e.message); }
      };
    });
  }));
}

// ---------- settings ----------
async function renderSettings() {
  const u = state.user;
  const plan = u.role === "solo" ? "Solo plan · $29/month" : "Dealership plan · $35 per seat/month";
  $("#view").innerHTML = banner() + `
    <h2>Settings</h2>
    <div class="card" style="margin-bottom:12px">
      <b>${esc(u.name)}</b> <span class="pill">${esc(plan)}</span>
      <p class="muted small">${esc(u.dealership || "")}</p>
    </div>
    <div class="card" style="margin-bottom:12px">
      <h3 style="margin-top:0">How texts are sent</h3>
      <div class="seg" id="mode">
        <button data-m="tap" class="${u.send_mode === "tap" ? "active" : ""}">📱 Tap to send from my iPhone</button>
        <button data-m="auto" class="${u.send_mode === "auto" ? "active" : ""}">⚡ Fully automatic</button>
      </div>
      <p class="small">${u.send_mode === "tap"
        ? "Each message opens your phone's Messages app with everything filled in. You hit send, and it goes out as an iMessage from your own number."
        : "Messages go out on their own from a registered business number at 10am, and replies come back into the app."}</p>
      <p class="muted small">Texts only go to customers who agreed. No texts before 8am or after 9pm. STOP replies cancel all future texts automatically.</p>
    </div>
    ${u.role === "manager" && u.invite_code ? `<div class="card" style="margin-bottom:12px">
      <h3 style="margin-top:0">Invite your salespeople</h3>
      <p class="small">Have each salesperson create an account, pick <b>Join my team</b>, and enter this code:</p>
      <div class="row"><code class="invite">${esc(u.invite_code)}</code><button class="btn small" id="copy-invite">Copy</button></div>
    </div>` : ""}
    ${u.demo ? "" : `<div class="card" style="margin-bottom:12px">
      <h3 style="margin-top:0">Password</h3>
      <form id="chpw" class="grid2">
        <label>Current password<input name="current" type="password" autocomplete="current-password" required></label>
        <label>New password<input name="new" type="password" autocomplete="new-password" required minlength="8" maxlength="72"></label>
        <div><button class="btn small primary" type="submit">Change password</button></div>
      </form>
      <p class="muted small">Changing it signs you out on your other devices.</p>
    </div>`}
    <div class="card">
      <h3 style="margin-top:0">Social posting</h3>
      <p class="small">${u.role === "solo"
        ? "Sold! posts are shared from your phone to your personal Facebook or Instagram."
        : `Sold! posts go to <b>${esc(u.facebook_page || "your dealership Page")}</b> automatically (after Meta approves the app).`}</p>
    </div>`;
  $("#chpw") && ($("#chpw").onsubmit = async (ev) => {
    ev.preventDefault();
    try {
      await api("/api/password", { method: "POST", body: { current: ev.target.current.value, new: ev.target.new.value } });
      ev.target.reset();
      toast("Password changed");
    } catch (e) { toast(e.message); }
  });
  $("#copy-invite") && ($("#copy-invite").onclick = async () => { await navigator.clipboard?.writeText(u.invite_code); toast("Invite code copied"); });
  document.querySelectorAll("#mode [data-m]").forEach((b) => (b.onclick = async () => {
    state.user = await api("/api/me", { method: "PATCH", body: { send_mode: b.dataset.m } });
    toast("Saved"); render();
  }));
}

boot();
