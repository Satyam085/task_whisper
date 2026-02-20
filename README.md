# 🧠 TaskWhisperer

### *Whisper to Telegram. OpenRouter Listens. Google Tasks Obeys.*

> A lean Go binary that turns your casual Telegram messages into perfectly structured Google Tasks — powered by OpenRouter free AI.

## Quick Start

### 1. Prerequisites

- Go 1.24+
- Google Cloud project with **Google Tasks API** enabled
- Telegram bot token from [@BotFather](https://t.me/BotFather)
- OpenRouter API key from [OpenRouter](https://openrouter.ai/)

### 2. Google OAuth Setup

```bash
# Place your credentials.json in the project root, then:
go run scripts/get_list_ids.go
```

This opens a browser for OAuth consent, saves `token.json`, and prints your Google Task list IDs.

### 3. Configure

```bash
cp .env.example .env
# Edit .env with your tokens, API keys, and list IDs
```

### 4. Run

```bash
go run .
# or
make run
```

### 5. Deploy

See the full [plan.md](plan.md) for Cloudflare Tunnel setup, systemd service configuration, and VPS deployment instructions.

## How It Works

1. You send a casual message to your Telegram bot
2. OpenRouter AI parses it into structured tasks with categories and due dates
3. Tasks are inserted into the correct Google Tasks lists
4. You get a clean confirmation reply
5. Every morning at 8 AM, you get a daily summary

## Examples

```
You: "buy milk tomorrow + dentist appt friday 3pm"

Bot: ✅ Added 2 tasks:
  • Buy milk → Shopping | Due: Tomorrow (Feb 19)
  • Book dentist appointment → Personal | Due: Fri, Feb 21
```

## Architecture

```
Telegram → Cloudflare Tunnel → Go webhook → OpenRouter AI → Google Tasks API
```

## Cost

| Resource | Cost |
|---|---|
| OpenRouter openrouter/free | $0/mo |
| Google Tasks API | $0/mo |
| Telegram Bot API | $0/mo |
| Cloudflare Tunnel | $0/mo |
| Oracle Free Tier VPS | $0/mo |

---

*Built with Go • Powered by OpenRouter • Deployed with ❤️ and zero regrets*
