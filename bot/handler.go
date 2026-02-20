package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"taskwhisperer/config"
	"taskwhisperer/llm"
	"taskwhisperer/store"
	"taskwhisperer/tasks"
)

// Handler processes incoming Telegram messages via long polling.
type Handler struct {
	bot    *tgbotapi.BotAPI
	llm    *llm.Client
	tasks  *tasks.Service
	lists  *tasks.ListMapping
	store  *store.Store
	cfg    *config.Config
	
	// summaryGen is a callback to generate the daily summary on-demand Let's use a simple function type
	summaryGen func() string
}

// NewHandler creates a new Telegram bot handler with all dependencies.
func NewHandler(cfg *config.Config, llmClient *llm.Client, taskSvc *tasks.Service, lists *tasks.ListMapping, st *store.Store) (*Handler, error) {
	bot, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	log.Printf("🤖 Authorized on Telegram as @%s", bot.Self.UserName)

	return &Handler{
		bot:    bot,
		llm:    llmClient,
		tasks:  taskSvc,
		lists:  lists,
		store:  st,
		cfg:    cfg,
	}, nil
}

// SetSummaryGenerator allows injecting the summary generation logic
// without creating a circular dependency between bot and scheduler.
func (h *Handler) SetSummaryGenerator(gen func() string) {
	h.summaryGen = gen
}

// StartPolling begins long-polling for Telegram updates.
// This runs in the foreground and blocks until stopCh is closed.
func (h *Handler) StartPolling(stopCh <-chan struct{}) {
	// Remove any existing webhook so polling works
	removeWebhook := tgbotapi.DeleteWebhookConfig{DropPendingUpdates: false}
	if _, err := h.bot.Request(removeWebhook); err != nil {
		log.Printf("⚠️ Failed to remove webhook (may not exist): %v", err)
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = h.cfg.PollingInterval

	updates := h.bot.GetUpdatesChan(u)

	log.Printf("📡 Polling for updates every %ds...", h.cfg.PollingInterval)

	for {
		select {
		case <-stopCh:
			h.bot.StopReceivingUpdates()
			log.Println("📡 Polling stopped")
			return
		case update := <-updates:
			if update.Message == nil || update.Message.Text == "" {
				continue
			}

			// Whitelist check — silently ignore unauthorized users
			if update.Message.From.ID != h.cfg.AllowedUserID {
				continue
			}

			h.processMessage(update.Message)
		}
	}
}

func (h *Handler) processMessage(msg *tgbotapi.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Printf("📨 Received: %q", msg.Text)

	// Handle commands
	if msg.IsCommand() {
		switch msg.Command() {
		case "start", "help":
			helpText := `🤖 *TaskWhisperer Rules*

Just message me naturally:
• "buy milk tomorrow"
• "dentist appt on friday 3pm"
• "finish the quarterly report EOD"

Commands:
/today - Get your daily summary right now
/help  - Show this message`
			h.sendMessage(msg.Chat.ID, helpText)
			return
		case "today":
			if h.summaryGen != nil {
				h.sendMessage(msg.Chat.ID, "⏳ Generating summary...")
				summary := h.summaryGen()
				h.sendMessage(msg.Chat.ID, summary)
			} else {
				h.sendMessage(msg.Chat.ID, "❌ Summary generator not configured.")
			}
			return
		default:
			h.sendMessage(msg.Chat.ID, "🤔 Unknown command. Type /help to see what I can do.")
			return
		}
	}

	// Parse with OpenRouter
	parsedTasks, err := h.llm.ParseTasks(ctx, msg.Text, h.cfg.Timezone)
	if err != nil {
		log.Printf("❌ LLM parse error: %v", err)
		h.sendMessage(msg.Chat.ID, "❌ Sorry, I couldn't understand that. Try rephrasing?")
		return
	}

	if len(parsedTasks) == 0 {
		h.sendMessage(msg.Chat.ID, "🤔 I couldn't find any tasks in your message. Try something like \"buy milk tomorrow\"")
		return
	}

	// Insert each task into Google Tasks
	var added []string
	var errors int
	for _, t := range parsedTasks {
		listID := h.lists.GetListID(t.Category)

		if err := h.tasks.InsertTask(listID, t.Title, t.Notes, t.DueDate); err != nil {
			log.Printf("❌ Failed to insert task %q: %v", t.Title, err)
			errors++
			continue
		}

		// Log to SQLite
		if h.store != nil {
			_ = h.store.LogTask(t.Title, t.Category, t.DueDate, t.Priority)
		}

		added = append(added, formatTaskLine(t))
		log.Printf("✅ Added: %s → %s", t.Title, tasks.CategoryName(t.Category))
	}

	// Send confirmation
	reply := buildConfirmation(added, errors)
	h.sendMessage(msg.Chat.ID, reply)
}

// SendMessage sends a text message to a Telegram chat (exported for scheduler).
func (h *Handler) SendMessage(chatID int64, text string) {
	h.sendMessage(chatID, text)
}

func (h *Handler) sendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	if _, err := h.bot.Send(msg); err != nil {
		log.Printf("❌ Failed to send message: %v", err)
	}
}

func formatTaskLine(t llm.Task) string {
	line := fmt.Sprintf("• %s → %s", t.Title, tasks.CategoryName(t.Category))

	if t.DueDate != "" {
		if due, err := time.Parse("2006-01-02", t.DueDate); err == nil {
			now := time.Now()
			today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			tomorrow := today.Add(24 * time.Hour)
			dueDay := time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, due.Location())

			var dateStr string
			switch {
			case dueDay.Equal(today):
				dateStr = fmt.Sprintf("Today (%s)", due.Format("Jan 2"))
			case dueDay.Equal(tomorrow):
				dateStr = fmt.Sprintf("Tomorrow (%s)", due.Format("Jan 2"))
			default:
				dateStr = due.Format("Mon, Jan 2")
			}
			line += fmt.Sprintf(" | Due: %s", dateStr)
		}
	}

	if t.Priority == "high" {
		line += " 🔴"
	}

	return line
}

func buildConfirmation(added []string, errors int) string {
	if len(added) == 0 && errors > 0 {
		return "❌ Failed to add tasks. Please try again."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✅ Added %d task", len(added)))
	if len(added) != 1 {
		sb.WriteString("s")
	}
	sb.WriteString(":\n")

	for _, line := range added {
		sb.WriteString("  ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	if errors > 0 {
		sb.WriteString(fmt.Sprintf("\n⚠️ %d task(s) failed to save.", errors))
	}

	return sb.String()
}
