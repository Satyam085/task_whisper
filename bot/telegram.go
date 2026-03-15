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

// TelegramBot processes incoming Telegram messages via long polling.
type TelegramBot struct {
	bot    *tgbotapi.BotAPI
	llm    *llm.Client
	tasks  *tasks.Service
	lists  *tasks.ListMapping
	store  *store.Store
	cfg    *config.Config
	
	// summaryGen is a callback to generate the daily summary on-demand Let's use a simple function type
	summaryGen func() string
}

// NewTelegramBot creates a new Telegram bot handler with all dependencies.
func NewTelegramBot(cfg *config.Config, llmClient *llm.Client, taskSvc *tasks.Service, lists *tasks.ListMapping, st *store.Store) (*TelegramBot, error) {
	bot, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	log.Printf("🤖 Authorized on Telegram as @%s", bot.Self.UserName)

	return &TelegramBot{
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
func (h *TelegramBot) SetSummaryGenerator(gen func() string) {
	h.summaryGen = gen
}

// StartPolling begins long-polling for Telegram updates.
// This runs in the foreground and blocks until stopCh is closed.
func (h *TelegramBot) StartPolling(stopCh <-chan struct{}) {
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
			if update.CallbackQuery != nil {
				// We don't necessarily whitelist callback queries strictly here
				// but check the original message Chat.ID or From.ID
				if update.CallbackQuery.From.ID == h.cfg.AllowedUserID {
					h.handleCallbackQuery(update.CallbackQuery)
				}
				continue
			}

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

func (h *TelegramBot) processMessage(msg *tgbotapi.Message) {
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
		case "list":
			args := strings.TrimSpace(msg.CommandArguments())
			if args == "" {
				h.sendMessage(msg.Chat.ID, "🤔 Please specify a category. Example: `/list office`, `/list personal`")
				return
			}
			
			cat := strings.ToLower(args)
			listID := h.lists.GetListID(cat)
			if listID == "" {
				h.sendMessage(msg.Chat.ID, fmt.Sprintf("❌ Unknown category: %s", cat))
				return
			}

			h.sendMessage(msg.Chat.ID, fmt.Sprintf("⏳ Fetching active tasks for %s...", tasks.CategoryName(cat)))
			
			now := time.Now()
			upcoming, _ := h.tasks.GetUpcomingTasks(listID, now, 90) // Next 90 days
			noDue, _ := h.tasks.GetTasksWithoutDueDate(listID)
			
			if len(upcoming) == 0 && len(noDue) == 0 {
				h.sendMessage(msg.Chat.ID, fmt.Sprintf("🎉 You have no pending tasks in %s!", tasks.CategoryName(cat)))
				return
			}
			
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("📋 Active Tasks for %s:\n\n", tasks.CategoryName(cat)))
			for _, t := range upcoming {
				sb.WriteString(fmt.Sprintf("• %s (Due: %s)\n", t.Title, t.DueDate))
			}
			for _, t := range noDue {
				sb.WriteString(fmt.Sprintf("• %s (No Due Date)\n", t.Title))
			}
			
			h.sendMessage(msg.Chat.ID, sb.String())
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
		if strings.Contains(err.Error(), "gemini") {
			h.sendMessage(msg.Chat.ID, "❌ The AI request failed. Please try again later.")
		} else {
			h.sendMessage(msg.Chat.ID, "❌ Sorry, I couldn't understand that. Try rephrasing?")
		}
		return
	}

	if len(parsedTasks) == 0 {
		h.sendMessage(msg.Chat.ID, "🤔 I couldn't find any tasks in your message. Try something like \"buy milk tomorrow\"")
		return
	}

	// Insert each task into Google Tasks
	var added []string
	var errorsCount int
	var addedIDs []int64
	var addedTitles []string

	for _, t := range parsedTasks {
		listID := h.lists.GetListID(t.Category)

		gTask, err := h.tasks.InsertTask(listID, t.Title, t.Notes, t.DueDate, "")
		if err != nil {
			log.Printf("❌ Failed to insert task %q: %v", t.Title, err)
			errorsCount++
			continue
		}

		// Log to SQLite
		var internalID int64
		if h.store != nil {
			internalID, _ = h.store.LogTask(t.Title, t.Category, t.DueDate, t.DueTime, t.Recurrence, t.Priority, gTask.Id, listID)
			addedIDs = append(addedIDs, internalID)
			addedTitles = append(addedTitles, t.Title)
		}

		added = append(added, formatTaskLine(t))
		log.Printf("✅ Added: %s → %s", t.Title, tasks.CategoryName(t.Category))
		
		// Insert Subtasks if any
		if len(t.Subtasks) > 0 {
			for _, sub := range t.Subtasks {
				subTask, err := h.tasks.InsertTask(listID, sub, "", t.DueDate, gTask.Id)
				if err != nil {
					log.Printf("⚠️ Failed to insert subtask %q: %v", sub, err)
					errorsCount++
					continue
				}
				if h.store != nil {
					// We don't add subtasks to the interactive keyboard actions right now to avoid clutter
					h.store.LogTask(sub, t.Category, t.DueDate, t.DueTime, "", t.Priority, subTask.Id, listID)
				}
				added = append(added, fmt.Sprintf("    ↳ %s", sub))
			}
		}
	}

	// Send confirmation
	reply := buildConfirmation(added, errorsCount)
	var keyboard *tgbotapi.InlineKeyboardMarkup
	if len(addedIDs) > 0 {
		var rows [][]tgbotapi.InlineKeyboardButton
		for i, id := range addedIDs {
			if id > 0 {
				title := addedTitles[i]
				if len(title) > 20 {
					title = title[:17] + "..."
				}
				row := tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("✅ "+title, fmt.Sprintf("c:%d", id)),
					tgbotapi.NewInlineKeyboardButtonData("💤 Snooze", fmt.Sprintf("s:%d", id)),
				)
				rows = append(rows, row)
			}
		}
		if len(rows) > 0 {
			kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
			keyboard = &kb
		}
	}
	
	if keyboard != nil {
		h.sendMessageWithKeyboard(msg.Chat.ID, reply, keyboard)
	} else {
		h.sendMessage(msg.Chat.ID, reply)
	}
}

func (h *TelegramBot) handleCallbackQuery(cq *tgbotapi.CallbackQuery) {
	// Acknowledge callback query immediately
	callback := tgbotapi.NewCallback(cq.ID, "")
	if _, err := h.bot.Request(callback); err != nil {
		log.Printf("⚠️ Failed to answer callback query: %v", err)
	}

	data := cq.Data
	parts := strings.Split(data, ":")
	if len(parts) != 2 {
		return
	}

	action := parts[0]
	var taskID int64
	fmt.Sscanf(parts[1], "%d", &taskID)

	taskRec, err := h.store.GetTaskByID(taskID)
	if err != nil {
		h.sendMessage(cq.Message.Chat.ID, "❌ Task not found in internal database.")
		return
	}

	if taskRec.GTaskID == "" || taskRec.GListID == "" {
		h.sendMessage(cq.Message.Chat.ID, "❌ Internal task is missing Google Tasks link.")
		return
	}

	switch action {
	case "c":
		err := h.tasks.CompleteTask(taskRec.GListID, taskRec.GTaskID)
		if err != nil {
			h.sendMessage(cq.Message.Chat.ID, fmt.Sprintf("❌ Failed to complete '%s'.", taskRec.Title))
			return
		}
		
		reply := fmt.Sprintf("✅ Marked '%s' as completed!", taskRec.Title)
		
		// Handle Recurrence
		if taskRec.Recurrence != "" {
			nextDue := calculateNextDueDate(taskRec.DueDate, taskRec.Recurrence)
			if nextDue != "" {
				// Insert next occurrence
				gTask, err := h.tasks.InsertTask(taskRec.GListID, taskRec.Title, "Recurring: "+taskRec.Recurrence, nextDue, "")
				if err == nil {
					h.store.LogTask(taskRec.Title, taskRec.Category, nextDue, taskRec.DueTime, taskRec.Recurrence, taskRec.Priority, gTask.Id, taskRec.GListID)
					reply += fmt.Sprintf("\n🔄 Next occurrence scheduled for %s.", nextDue)
				}
			}
		}
		h.sendMessage(cq.Message.Chat.ID, reply)
	case "s":
		// Snooze: push due date forward 1 day
		var currentDue time.Time
		if taskRec.DueDate != "" {
			currentDue, _ = time.Parse("2006-01-02", taskRec.DueDate)
		} else {
			currentDue = time.Now()
		}
		newDue := currentDue.Add(24 * time.Hour).Format("2006-01-02")

		err := h.tasks.UpdateTaskDueDate(taskRec.GListID, taskRec.GTaskID, newDue)
		if err != nil {
			h.sendMessage(cq.Message.Chat.ID, fmt.Sprintf("❌ Failed to snooze '%s'.", taskRec.Title))
			return
		}
		
		h.sendMessage(cq.Message.Chat.ID, fmt.Sprintf("💤 Snoozed '%s' to tomorrow (%s).", taskRec.Title, newDue))
	}
}

// SendNotification sends a notification text (exported for scheduler).
func (h *TelegramBot) SendNotification(text string) {
	h.sendMessage(h.cfg.ChatID, text)
}

func (h *TelegramBot) sendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	if _, err := h.bot.Send(msg); err != nil {
		log.Printf("❌ Failed to send message: %v", err)
	}
}

func (h *TelegramBot) sendMessageWithKeyboard(chatID int64, text string, keyboard *tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	if keyboard != nil {
		msg.ReplyMarkup = *keyboard
	}
	if _, err := h.bot.Send(msg); err != nil {
		log.Printf("❌ Failed to send message with keyboard: %v", err)
	}
}

func calculateNextDueDate(currentDue, recurrence string) string {
	var baseDate time.Time
	if currentDue != "" {
		t, err := time.Parse("2006-01-02", currentDue)
		if err == nil {
			baseDate = t
		} else {
			baseDate = time.Now()
		}
	} else {
		baseDate = time.Now()
	}

	rec := strings.ToLower(recurrence)
	var next time.Time
	switch {
	case strings.Contains(rec, "daily") || strings.Contains(rec, "day") || strings.Contains(rec, "tomorrow"):
		next = baseDate.AddDate(0, 0, 1)
	case strings.Contains(rec, "weekly") || strings.Contains(rec, "week"):
		next = baseDate.AddDate(0, 0, 7)
	case strings.Contains(rec, "monthly") || strings.Contains(rec, "month"):
		next = baseDate.AddDate(0, 1, 0)
	case strings.Contains(rec, "yearly") || strings.Contains(rec, "year"):
		next = baseDate.AddDate(1, 0, 0)
	default:
		return ""
	}
	return next.Format("2006-01-02")
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
