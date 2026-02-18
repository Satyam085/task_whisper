# 🧠 TaskWhisperer
### *Whisper to Telegram. Gemini Listens. Google Tasks Obeys.*

> A lean, mean, single Go binary that turns your casual Telegram messages into perfectly structured Google Tasks — powered by Gemini AI, running on your VPS for nearly free.

---

## 📋 Table of Contents

1. [Project Overview](#project-overview)
2. [Name & Vibe](#name--vibe)
3. [Tech Stack](#tech-stack)
4. [Architecture](#architecture)
5. [Free Tunnel Setup (No Domain Needed)](#free-tunnel-setup)
6. [Project Structure](#project-structure)
7. [Core Features](#core-features)
8. [Gemini Prompt & Schema Design](#gemini-prompt--schema-design)
9. [Google Tasks Setup](#google-tasks-setup)
10. [Telegram Bot Setup](#telegram-bot-setup)
11. [Environment Config](#environment-config)
12. [Deployment on VPS](#deployment-on-vps)
13. [Build Order & Timeline](#build-order--timeline)
14. [Cost Breakdown](#cost-breakdown)
15. [Example Conversations](#example-conversations)

---

## Project Overview

TaskWhisperer is a personal productivity bot that bridges Telegram and Google Tasks using Gemini AI as the brain. You send a casual natural language message like *"remind me to call mum tomorrow and pick up eggs"* — and it creates properly categorized, dated tasks in your Google Tasks automatically.

No UI, no dashboards, no fluff. Just a Telegram message and done.

**Runs as:** Single Go binary (~12-20MB RAM idle, near 0% CPU when idle)
**Designed for:** Personal use only (1 user, whitelisted by Telegram user ID)
**Deployment:** Any cheap VPS or even Oracle Free Tier ARM

---

## Name & Vibe

```
 _____         _   _    _    _ _     _
|_   _|_ _ ___| |_| |  | |  | | |__ (_)___ _ __  ___ _ __ ___ _ __
  | |/ _` / __| / / |  | |/\| | '_ \| / __| '_ \/ _ \ '__/ _ \ '__|
  | | (_| \__ \ < | |  \  /\  / | | | \__ \ |_) \  __/ | |  __/ |
  |_|\__,_|___/_|\_\_|   \/  \/_| |_|_|___/ .__/ \___|_|  \___|_|
                                           |_|
```

**Name:** `TaskWhisperer`
**Tagline:** *"You whisper. Tasks appear."*
**Alt names considered:** TeleTask, GhostTasker, BrainDump Bot, NudgeBot
**Why TaskWhisperer:** Because you just casually whisper tasks into Telegram in plain english — no forms, no apps, no structured input — and it *just works*.

---

## Tech Stack

| Layer | Choice | Why |
|---|---|---|
| Language | **Go** | Single binary, ~15MB RAM, no runtime, fast cold start |
| Telegram | `go-telegram-bot-api` | Lightweight, well-maintained |
| LLM | **Gemini 1.5 Flash** | Free tier (1500 req/day), JSON mode, fast |
| Tasks | **Google Tasks API** | Free, no credit card, perfect for personal use |
| Tunnel | **Cloudflare Tunnel** | Free forever, no domain needed, works with webhook |
| Scheduler | Native `time.Ticker` | No cron daemon, no extra deps |
| Storage | **SQLite** (optional) | Lightweight local log of what was created |
| Deploy | Systemd service | Auto-restart, logging, lightweight |

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        YOUR VPS                                 │
│                                                                 │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │                   taskwhisperer (Go binary)              │   │
│  │                                                          │   │
│  │  ┌─────────┐   ┌──────────┐   ┌────────┐   ┌────────┐  │   │
│  │  │Telegram │──▶│ Gemini   │──▶│ Google │   │ Daily  │  │   │
│  │  │ Handler │   │ Parser   │   │ Tasks  │   │Summary │  │   │
│  │  └─────────┘   └──────────┘   └────────┘   └────────┘  │   │
│  │       ▲                                         │        │   │
│  │       │                                         ▼        │   │
│  │  Webhook (HTTP)                          Telegram msg    │   │
│  └──────────────────────────────────────────────────────────┘   │
│           ▲                                                      │
│           │                                                      │
│  ┌────────┴──────────┐                                          │
│  │ cloudflared tunnel│  ◀── Free! No domain needed!            │
│  └───────────────────┘                                          │
└─────────────────────────────────────────────────────────────────┘
           ▲
           │ HTTPS
           │
    ┌──────┴──────┐
    │  Telegram   │
    │   Servers   │
    └─────────────┘
```

**Data flow for a message:**

```
1. You send "buy milk tomorrow + dentist appt friday 3pm"
2. Telegram → Cloudflare Tunnel → Your VPS (webhook)
3. Go handler receives → calls Gemini 1.5 Flash with structured prompt
4. Gemini returns JSON → { tasks: [{title, category, due_date, notes}] }
5. Google Tasks API → inserts into correct list with due date
6. Bot replies "✅ 2 tasks added!"
```

---

## Free Tunnel Setup

Since you have no domain, we use **Cloudflare Tunnel** (formerly Argo Tunnel). It's completely free and gives you a permanent `*.trycloudflare.com` HTTPS URL.

### Step 1 — Install cloudflared on your VPS

```bash
# For Ubuntu/Debian (x86_64)
wget https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64.deb
sudo dpkg -i cloudflared-linux-amd64.deb

# For ARM (Oracle Free Tier)
wget https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-arm64.deb
sudo dpkg -i cloudflared-linux-arm64.deb
```

### Step 2 — Create a free tunnel (no Cloudflare account needed!)

```bash
# This creates a temporary public URL that forwards to your local port 8080
cloudflared tunnel --url http://localhost:8080
```

You'll get a URL like: `https://something-random.trycloudflare.com`

> ⚠️ **Heads up:** The free `trycloudflare.com` URL changes every time you restart cloudflared. To fix this, either:
> - Create a free Cloudflare account and use a **Named Tunnel** (URL stays permanent)
> - OR write a small script that re-registers the webhook with Telegram whenever the URL changes

### Step 3 — Persistent Named Tunnel (Recommended, still free)

```bash
# Login to Cloudflare (free account needed)
cloudflared tunnel login

# Create a named tunnel
cloudflared tunnel create taskwhisperer

# Get your tunnel ID (shown in output above)
# Create config file
mkdir -p ~/.cloudflared
cat > ~/.cloudflared/config.yml << EOF
tunnel: <YOUR-TUNNEL-ID>
credentials-file: /root/.cloudflared/<YOUR-TUNNEL-ID>.json

ingress:
  - hostname: taskwhisperer.<your-subdomain>.workers.dev
    service: http://localhost:8080
  - service: http_status:404
EOF

# Run it
cloudflared tunnel run taskwhisperer
```

> You can use any free subdomain via Cloudflare's free plan. Even without a custom domain, `trycloudflare.com` works fine for personal use.

### Step 4 — Systemd service for cloudflared

```ini
# /etc/systemd/system/cloudflared.service
[Unit]
Description=Cloudflare Tunnel for TaskWhisperer
After=network.target

[Service]
Type=simple
ExecStart=/usr/bin/cloudflared tunnel run taskwhisperer
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable cloudflared
sudo systemctl start cloudflared
```

---

## Project Structure

```
taskwhisperer/
├── main.go                  # Entry point, wires everything together
├── config/
│   └── config.go            # Loads .env, validates required vars
├── bot/
│   └── handler.go           # Telegram webhook handler, user whitelist
├── gemini/
│   └── parser.go            # Calls Gemini Flash, returns structured tasks
├── tasks/
│   ├── google.go            # Google Tasks API: insert, list, fetch
│   └── lists.go             # Maps category → Google Task List ID
├── scheduler/
│   └── summary.go           # Daily 8AM summary goroutine
├── store/
│   └── sqlite.go            # Optional: log of all tasks created (for summary)
├── scripts/
│   └── get_list_ids.go      # One-time script to print your Google Task list IDs
├── .env                     # Your secrets (never commit this)
├── .env.example             # Template (safe to commit)
├── go.mod
├── go.sum
├── Makefile                 # build, deploy, logs shortcuts
└── README.md
```

---

## Core Features

### ✅ Natural Language Task Parsing
Send anything casual. Gemini figures out the rest — no prefixes, no structure needed.
- "call dentist tomorrow" → personal task, due tomorrow
- "submit quarterly report by friday EOD" → office task, due Friday
- "milk, eggs, bread, shampoo" → 4 separate shopping tasks
- "remind me to wish john happy birthday on march 5" → personal, due March 5

### 📂 Category Auto-Sorting (Fully AI-Inferred)
You never need to tag or prefix anything. Gemini reads the full context of your message and classifies each task automatically:

| Category | Google Tasks List | How Gemini figures it out |
|---|---|---|
| `personal` | Personal | Health, family, friends, hobbies, self-care, personal calls |
| `office` | Work | Meetings, reports, PRs, colleagues, deadlines, code, clients, invoices |
| `shopping` | Shopping List | "buy", "get", "order", "pick up", item names with quantities |
| `others` | Miscellaneous | Anything ambiguous — safe default |

**Gemini uses surrounding context too.** If you say *"call raj about the deployment"* in a message with other work tasks, it goes to Work. If you say *"call raj for his birthday"*, it goes to Personal. No manual hints needed — ever.

### 📅 Due Date + Time Parsing
Gemini resolves relative dates using today's date as context:
- "tomorrow" → next calendar day
- "next monday" → correct date calculated
- "end of week" → Friday
- "3pm friday" → Friday with time note
- "in 2 days" → current date + 2

### 🌅 Daily Summary (8 AM)
Every morning at 8 AM your local time, the bot sends you:
```
☀️ Good morning! Here's your day:

📋 TODAY (Feb 19):
  • [ ] Call dentist — Personal
  • [ ] Submit project report — Work

📆 UPCOMING (next 3 days):
  • Feb 20 — Review John's PR — Work
  • Feb 21 — Buy protein powder — Shopping

✅ COMPLETED YESTERDAY: 3 tasks
```

### 🔒 Security
- Only your Telegram user ID can interact with the bot
- All other users get silently ignored (no error message — ghost mode)

### 📝 Confirmation Messages
Every insertion gets a clean confirmation reply:
```
✅ Added 3 tasks:
  • Call dentist → Personal | Due: Tomorrow (Feb 19)
  • Buy milk → Shopping | Due: Tomorrow (Feb 19)
  • Buy eggs → Shopping | Due: Tomorrow (Feb 19)
```

---

## Gemini Prompt & Schema Design

### System Prompt

```
You are a task extraction assistant. Today's date is {TODAY} ({WEEKDAY}).
The user's timezone is {TIMEZONE}.

Extract ALL tasks from the user's message and return ONLY a valid JSON object.
No preamble, no explanation, no markdown — raw JSON only.

CATEGORY INFERENCE (never ask the user — always decide yourself):
You must infer the category from context, vocabulary, and intent alone.
The user will NEVER label or prefix tasks — it's your job to figure it out.

- "office": mentions of meetings, standups, reports, PRs, code reviews, deployments,
  clients, invoices, presentations, colleagues in a professional context, work deadlines,
  anything that sounds like it belongs in a workplace
- "shopping": any "buy", "get", "order", "pick up", "grab", item names with quantities,
  grocery items, household supplies, anything being purchased
- "personal": health appointments, family, friends, hobbies, self-care, personal calls,
  birthdays, travel plans, anything clearly about the user's personal life
- "others": genuinely ambiguous tasks that don't fit any above — use sparingly

USE SURROUNDING CONTEXT: If a message has multiple tasks, use the overall theme to help
classify ambiguous ones. "call raj" in a message full of work tasks → office.
"call raj for his birthday" → personal. Read the whole message before classifying.

When genuinely unsure, prefer "personal" over "others".

DATE RULES:
- Resolve all relative dates ("tomorrow", "next Friday", "in 3 days") to YYYY-MM-DD
- If no date mentioned, omit due_date entirely
- "EOD" / "end of day" = same day, no specific time needed

OUTPUT FORMAT:
{
  "tasks": [
    {
      "title": "Short actionable title (max 60 chars)",
      "notes": "Any extra context, quantity, details, or full original phrasing",
      "due_date": "YYYY-MM-DD",
      "category": "personal|office|shopping|others",
      "priority": "low|normal|high"
    }
  ]
}

RULES:
- Shopping lists: split each item into its own task object
- Extract multiple tasks from a single message
- title should be action-oriented: "Call dentist" not "dentist"
- If priority unclear, default to "normal"
- High priority keywords: urgent, ASAP, important, critical, must
```

### Go Struct

```go
type Task struct {
    Title    string `json:"title"`
    Notes    string `json:"notes,omitempty"`
    DueDate  string `json:"due_date,omitempty"`  // YYYY-MM-DD
    Category string `json:"category"`
    Priority string `json:"priority"`
}

type GeminiResponse struct {
    Tasks []Task `json:"tasks"`
}
```

---

## Google Tasks Setup

### Step 1 — Google Cloud Console (free)

1. Go to [console.cloud.google.com](https://console.cloud.google.com)
2. Create new project: `taskwhisperer`
3. Enable **Google Tasks API**
4. Go to **Credentials** → Create → **OAuth 2.0 Client ID**
5. Application type: **Desktop App**
6. Download `credentials.json`

### Step 2 — One-time Auth (run locally)

Write a small Go script (`scripts/get_list_ids.go`) that:
1. Opens OAuth flow in browser
2. Saves `token.json` locally
3. Prints all your Google Task list names and IDs

```go
// Run once: go run scripts/get_list_ids.go
// Output:
// Personal       → MDc4NjU...
// Work           → OWNmYWR...
// Shopping List  → ZmQ4OWM...
```

Copy these IDs into your `.env` file.

### Step 3 — Upload to VPS

```bash
scp credentials.json token.json user@your-vps:/home/user/taskwhisperer/
```

> ⚠️ These files are your Google credentials. Treat like a password. Add to `.gitignore`.

---

## Telegram Bot Setup

1. Message `@BotFather` on Telegram
2. `/newbot` → choose name `TaskWhisperer` → username `taskwhisperer_yourname_bot`
3. Save the bot token
4. Message `@userinfobot` to get your personal Telegram user ID
5. Set webhook after deployment:

```bash
curl "https://api.telegram.org/bot<TOKEN>/setWebhook?url=https://your-tunnel-url.trycloudflare.com/webhook"
```

> The Go app will do this automatically on startup if you set `AUTO_REGISTER_WEBHOOK=true`

---

## Environment Config

### `.env`

```env
# Telegram
TELEGRAM_TOKEN=7123456789:AAF...
ALLOWED_USER_ID=123456789
CHAT_ID=123456789

# Gemini
GEMINI_API_KEY=AIza...

# Google Tasks List IDs (get these from scripts/get_list_ids.go)
GTASKS_LIST_PERSONAL=MDc4NjU...
GTASKS_LIST_OFFICE=OWNmYWR...
GTASKS_LIST_SHOPPING=ZmQ4OWM...
GTASKS_LIST_OTHERS=abc123...

# Google Auth files
GOOGLE_CREDENTIALS_PATH=/home/user/taskwhisperer/credentials.json
GOOGLE_TOKEN_PATH=/home/user/taskwhisperer/token.json

# App config
PORT=8080
WEBHOOK_URL=https://your-tunnel-url.trycloudflare.com/webhook
AUTO_REGISTER_WEBHOOK=true
TIMEZONE=Asia/Kolkata
SUMMARY_TIME=08:00

# Optional
SQLITE_PATH=/home/user/taskwhisperer/tasks.db
LOG_LEVEL=info
```

### `.env.example` (safe to commit)

```env
TELEGRAM_TOKEN=
ALLOWED_USER_ID=
CHAT_ID=
GEMINI_API_KEY=
GTASKS_LIST_PERSONAL=
GTASKS_LIST_OFFICE=
GTASKS_LIST_SHOPPING=
GTASKS_LIST_OTHERS=
GOOGLE_CREDENTIALS_PATH=./credentials.json
GOOGLE_TOKEN_PATH=./token.json
PORT=8080
WEBHOOK_URL=
AUTO_REGISTER_WEBHOOK=true
TIMEZONE=Asia/Kolkata
SUMMARY_TIME=08:00
```

---

## Deployment on VPS

### Build

```bash
# Cross-compile from your machine (or build on VPS directly)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o taskwhisperer .

# For Oracle ARM free tier
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o taskwhisperer .

# Binary size: ~8-12MB
# RAM usage: ~12-18MB idle
```

### Transfer to VPS

```bash
scp taskwhisperer .env credentials.json token.json user@your-vps:/home/user/taskwhisperer/
```

### Systemd Service

```ini
# /etc/systemd/system/taskwhisperer.service
[Unit]
Description=TaskWhisperer Telegram Bot
After=network.target cloudflared.service

[Service]
Type=simple
User=user
WorkingDirectory=/home/user/taskwhisperer
EnvironmentFile=/home/user/taskwhisperer/.env
ExecStart=/home/user/taskwhisperer/taskwhisperer
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal

# Resource limits (it barely needs any)
MemoryMax=64M
CPUQuota=10%

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable taskwhisperer
sudo systemctl start taskwhisperer

# Check logs
journalctl -u taskwhisperer -f
```

### Makefile (handy shortcuts)

```makefile
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o taskwhisperer .

deploy:
	scp taskwhisperer user@vps:/home/user/taskwhisperer/
	ssh user@vps "sudo systemctl restart taskwhisperer"

logs:
	ssh user@vps "journalctl -u taskwhisperer -f"

status:
	ssh user@vps "sudo systemctl status taskwhisperer"
```

---

## Build Order & Timeline

| Step | Task | Est. Time |
|---|---|---|
| 1 | Project scaffold, `config.go`, `.env` loading | 30 min |
| 2 | Google OAuth flow + `get_list_ids.go` script | 45 min |
| 3 | Gemini parser (`gemini/parser.go`) + test with raw Go | 1 hr |
| 4 | Google Tasks inserter (`tasks/google.go`) | 45 min |
| 5 | Telegram webhook handler (`bot/handler.go`) | 1 hr |
| 6 | Wire everything in `main.go` | 30 min |
| 7 | Daily summary scheduler (`scheduler/summary.go`) | 45 min |
| 8 | Cloudflare tunnel + systemd deployment | 30 min |
| 9 | Testing, edge cases, polish | 1 hr |
| **Total** | | **~7 hours** |

### Suggested Build Sequence

```
Week 1, Day 1:  Steps 1-3 (foundation + AI parsing works locally)
Week 1, Day 2:  Steps 4-6 (full pipeline works end-to-end locally)
Week 1, Day 3:  Steps 7-9 (deploy, tunnel, daily summary, done!)
```

---

## Cost Breakdown

| Resource | Cost |
|---|---|
| Oracle Cloud Free Tier (ARM, 1GB RAM, 1 OCPU) | **$0/mo forever** |
| Hetzner CAX11 (if preferred, 2 VCPU, 4GB RAM) | ~€3.29/mo |
| Gemini 1.5 Flash API (free tier: 1500 req/day) | **$0/mo** |
| Google Tasks API | **$0/mo** |
| Telegram Bot API | **$0/mo** |
| Cloudflare Tunnel | **$0/mo** |
| **Total (Oracle)** | **$0/mo** |
| **Total (Hetzner)** | **~€3.29/mo** |

> **Gemini Free Tier limits:** 15 requests/minute, 1 million tokens/day, 1500 requests/day.
> For personal use you'll never come close to hitting these limits.

---

## Example Conversations

### Multi-task message

```
You: "submit the jenkins report by thursday, review mike's PR today.
      book dentist for next week. buy shampoo and conditioner"

Bot: ✅ Added 5 tasks:
     • Submit Jenkins report → Work | Due: Thu, Feb 20
     • Review Mike's PR → Work | Due: Today (Feb 18)
     • Book dentist appointment → Personal | Due: Feb 24
     • Buy shampoo → Shopping | No due date
     • Buy conditioner → Shopping | No due date
```

### Shopping dump

```
You: "grocery run: eggs, milk 2%, greek yogurt, olive oil, pasta, tomato sauce x2"

Bot: ✅ Added 6 shopping tasks:
     • Buy eggs → Shopping
     • Buy milk (2%) → Shopping
     • Buy Greek yogurt → Shopping
     • Buy olive oil → Shopping
     • Buy pasta → Shopping
     • Buy tomato sauce (×2) → Shopping
```

### Daily Summary (8 AM auto-message)

```
Bot: ☀️ Good morning! Here's your Feb 19 briefing:

     📋 DUE TODAY:
       ☐ Review Mike's PR — Work
       ☐ Call dentist — Personal

     📆 COMING UP:
       Feb 20 — Submit Jenkins report — Work
       Feb 24 — Book dentist appointment — Personal

     🛒 SHOPPING LIST (4 items):
       milk, eggs, shampoo, conditioner

     Have a great day! 🚀
```

### Context-aware categorization (same name, different context)

```
You: "call raj about the jenkins deployment issue"
Bot: ✅ Call Raj about Jenkins deployment → Work | Due: —

You: "call raj for his birthday next saturday"
Bot: ✅ Call Raj for birthday → Personal | Due: Feb 22

You: "urgent - production is down, need to hotfix the auth service ASAP"
Bot: ✅ Hotfix auth service → Work | Priority: HIGH | Due: Today

You: "wish sarah happy bday on march 15"
Bot: ✅ Wish Sarah happy birthday → Personal | Due: Mar 15

You: "order birthday cake for mom - chocolate, from that bakery near office"
Bot: ✅ Order birthday cake for mom → Shopping
     Notes: chocolate cake, bakery near office
```

---

## Future Ideas (Post-MVP)

- `/list` command — show all pending tasks in Telegram
- `/done` command — mark a task complete from Telegram
- Voice message support — Telegram voice → Whisper API → text → tasks
- Snooze/reschedule via bot command
- Weekly review summary (Sunday evenings)
- Priority tagging with 🔴🟡🟢 in summary messages
- Duplicate detection (Gemini flags if similar task already exists)

---

*Built with Go • Powered by Gemini • Deployed with ❤️ and zero regrets*