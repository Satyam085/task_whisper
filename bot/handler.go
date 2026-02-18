package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"taskwhisperer/config"
	"taskwhisperer/gemini"
	"taskwhisperer/store"
	"taskwhisperer/tasks"
)

// Handler processes incoming Telegram webhook requests.
type Handler struct {
	bot    *tgbotapi.BotAPI
	gemini *gemini.Client
	tasks  *tasks.Service
	lists  *tasks.ListMapping
	store  *store.Store
	cfg    *config.Config
}

// NewHandler creates a new Telegram bot handler with all dependencies.
func NewHandler(cfg *config.Config, gem *gemini.Client, taskSvc *tasks.Service, lists *tasks.ListMapping, st *store.Store) (*Handler, error) {
	bot, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	log.Printf("🤖 Authorized on Telegram as @%s", bot.Self.UserName)

	return &Handler{
		bot:    bot,
		gemini: gem,
		tasks:  taskSvc,
		lists:  lists,
		store:  st,
		cfg:    cfg,
	}, nil
}

// GetBot returns the underlying Telegram Bot API instance.
func (h *Handler) GetBot() *tgbotapi.BotAPI {
	return h.bot
}

// RegisterWebhook sets the Telegram webhook URL.
func (h *Handler) RegisterWebhook() error {
	if h.cfg.WebhookURL == "" {
		return fmt.Errorf("WEBHOOK_URL is not configured")
	}

	wh, err := tgbotapi.NewWebhook(h.cfg.WebhookURL)
	if err != nil {
		return fmt.Errorf("failed to create webhook: %w", err)
	}

	_, err = h.bot.Request(wh)
	if err != nil {
		return fmt.Errorf("failed to set webhook: %w", err)
	}

	log.Printf("🔗 Webhook registered: %s", h.cfg.WebhookURL)
	return nil
}

// HandleWebhook is the HTTP handler for Telegram webhook updates.
func (h *Handler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("❌ Error reading request body: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var update tgbotapi.Update
	if err := json.Unmarshal(body, &update); err != nil {
		log.Printf("❌ Error parsing update: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Only process text messages
	if update.Message == nil || update.Message.Text == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Whitelist check — silently ignore unauthorized users
	if update.Message.From.ID != h.cfg.AllowedUserID {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Process asynchronously so we don't block the webhook response
	go h.processMessage(update.Message)

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) processMessage(msg *tgbotapi.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Printf("📨 Received: %q", msg.Text)

	// Parse with Gemini
	parsedTasks, err := h.gemini.ParseTasks(ctx, msg.Text, h.cfg.Timezone)
	if err != nil {
		log.Printf("❌ Gemini parse error: %v", err)
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

func formatTaskLine(t gemini.Task) string {
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
