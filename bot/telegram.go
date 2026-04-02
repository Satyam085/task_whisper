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
	
	// summaryGen is a callback to generate the daily summary on-demand
	summaryGen       func() string
	weeklySummaryGen func() string
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

// SetWeeklySummaryGenerator allows injecting the weekly summary generation logic.
func (h *TelegramBot) SetWeeklySummaryGenerator(gen func() string) {
	h.weeklySummaryGen = gen
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
			helpText := `*TaskWhisperer*

Just message me naturally:
• "buy milk tomorrow" → creates a task
• "done with the milk" → completes the task
• "change milk to friday" → edits the task
• "snooze all shopping to monday" → bulk operation

*Commands:*
/today - Daily summary
/weekly - Weekly rollup
/stats - Your streak and stats
/list <category> - Show tasks in a category
/config - Customize your digest
/snooze <category> <date> - Bulk snooze
/complete overdue|<category> - Bulk complete
/help - Show this message`
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
		case "stats":
			h.handleStats(msg.Chat.ID)
			return
		case "config":
			h.handleConfig(msg.Chat.ID, strings.TrimSpace(msg.CommandArguments()))
			return
		case "snooze":
			h.handleSnoozeCommand(msg.Chat.ID, strings.TrimSpace(msg.CommandArguments()))
			return
		case "complete":
			h.handleCompleteCommand(msg.Chat.ID, strings.TrimSpace(msg.CommandArguments()))
			return
		case "weekly":
			if h.weeklySummaryGen != nil {
				h.sendMessage(msg.Chat.ID, "⏳ Generating weekly summary...")
				summary := h.weeklySummaryGen()
				h.sendMessage(msg.Chat.ID, summary)
			} else {
				h.sendMessage(msg.Chat.ID, "❌ Weekly summary generator not configured.")
			}
			return
		default:
			h.sendMessage(msg.Chat.ID, "🤔 Unknown command. Type /help to see what I can do.")
			return
		}
	}

	// Step 1: Check for completion intent
	if handled := h.tryCompletion(ctx, msg); handled {
		return
	}

	// Step 2: Check for edit intent
	if handled := h.tryEdit(ctx, msg); handled {
		return
	}

	// Step 3: Check for bulk intent
	if handled := h.tryBulk(ctx, msg); handled {
		return
	}

	// Step 4: Create new tasks (default)
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
	case "c", "cc":
		err := h.tasks.CompleteTask(taskRec.GListID, taskRec.GTaskID)
		if err != nil {
			h.sendMessage(cq.Message.Chat.ID, fmt.Sprintf("❌ Failed to complete '%s'.", taskRec.Title))
			return
		}
		_ = h.store.MarkCompleted(taskID)

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

func (h *TelegramBot) getActiveLLMTasks() []llm.ActiveTask {
	records, err := h.store.GetActiveTasks()
	if err != nil || len(records) == 0 {
		return nil
	}
	tasks := make([]llm.ActiveTask, len(records))
	for i, r := range records {
		tasks[i] = llm.ActiveTask{
			ID:       r.ID,
			Title:    r.Title,
			Category: r.Category,
			DueDate:  r.DueDate,
		}
	}
	return tasks
}

func (h *TelegramBot) tryCompletion(ctx context.Context, msg *tgbotapi.Message) bool {
	activeTasks := h.getActiveLLMTasks()
	if len(activeTasks) == 0 {
		return false
	}

	resp, err := h.llm.IdentifyTaskToComplete(ctx, msg.Text, activeTasks)
	if err != nil {
		log.Printf("⚠️ Completion intent check failed: %v", err)
		return false
	}

	if !resp.IsCompletionIntent {
		return false
	}

	// Filter to high/medium confidence matches
	var matches []llm.TaskMatch
	for _, m := range resp.Matches {
		if m.Confidence == "high" || m.Confidence == "medium" {
			matches = append(matches, m)
		}
	}

	if len(matches) == 0 {
		if resp.Clarification != "" {
			h.sendMessage(msg.Chat.ID, resp.Clarification)
		} else {
			h.sendMessage(msg.Chat.ID, "I think you're marking something done, but I couldn't match it to your active tasks.")
		}
		return true
	}

	if len(matches) == 1 && matches[0].Confidence == "high" {
		// Auto-complete single high-confidence match
		h.completeTaskByID(msg.Chat.ID, matches[0].TaskID)
		return true
	}

	// Ambiguous — show inline keyboard
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, m := range matches {
		title := m.Title
		if len(title) > 30 {
			title = title[:27] + "..."
		}
		row := tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ "+title, fmt.Sprintf("cc:%d", m.TaskID)),
		)
		rows = append(rows, row)
	}
	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	h.sendMessageWithKeyboard(msg.Chat.ID, "Which task did you complete?", &kb)
	return true
}

func (h *TelegramBot) completeTaskByID(chatID int64, taskID int64) {
	taskRec, err := h.store.GetTaskByID(taskID)
	if err != nil {
		h.sendMessage(chatID, "Could not find that task.")
		return
	}
	if taskRec.GTaskID == "" || taskRec.GListID == "" {
		h.sendMessage(chatID, "Task is missing Google Tasks link.")
		return
	}
	err = h.tasks.CompleteTask(taskRec.GListID, taskRec.GTaskID)
	if err != nil {
		h.sendMessage(chatID, fmt.Sprintf("Failed to complete '%s'.", taskRec.Title))
		return
	}
	_ = h.store.MarkCompleted(taskID)

	reply := fmt.Sprintf("Marked '%s' as completed!", taskRec.Title)
	if taskRec.Recurrence != "" {
		nextDue := calculateNextDueDate(taskRec.DueDate, taskRec.Recurrence)
		if nextDue != "" {
			gTask, err := h.tasks.InsertTask(taskRec.GListID, taskRec.Title, "Recurring: "+taskRec.Recurrence, nextDue, "")
			if err == nil {
				h.store.LogTask(taskRec.Title, taskRec.Category, nextDue, taskRec.DueTime, taskRec.Recurrence, taskRec.Priority, gTask.Id, taskRec.GListID)
				reply += fmt.Sprintf("\nNext occurrence scheduled for %s.", nextDue)
			}
		}
	}
	h.sendMessage(chatID, reply)
}

func (h *TelegramBot) tryEdit(ctx context.Context, msg *tgbotapi.Message) bool {
	activeTasks := h.getActiveLLMTasks()
	if len(activeTasks) == 0 {
		return false
	}

	resp, err := h.llm.ParseEditIntent(ctx, msg.Text, activeTasks, h.cfg.Timezone)
	if err != nil {
		log.Printf("⚠️ Edit intent check failed: %v", err)
		return false
	}

	if !resp.IsEditIntent {
		return false
	}

	if resp.Confidence != "high" || resp.TaskID == 0 {
		if resp.Clarification != "" {
			h.sendMessage(msg.Chat.ID, resp.Clarification)
		} else {
			h.sendMessage(msg.Chat.ID, "I think you want to edit a task, but I'm not sure which one. Could you be more specific?")
		}
		return true
	}

	taskRec, err := h.store.GetTaskByID(resp.TaskID)
	if err != nil {
		h.sendMessage(msg.Chat.ID, "Could not find that task.")
		return true
	}

	// Apply updates to Google Tasks
	newTitle := resp.Updates["title"]
	newDue := resp.Updates["due_date"]
	newNotes := resp.Updates["notes"]

	if newTitle != "" || newDue != "" || newNotes != "" {
		if taskRec.GTaskID != "" && taskRec.GListID != "" {
			err = h.tasks.UpdateTask(taskRec.GListID, taskRec.GTaskID, newTitle, newNotes, newDue)
			if err != nil {
				log.Printf("⚠️ Failed to update Google Task: %v", err)
			}
		}
	}

	// Apply updates to local store
	fields := make(map[string]interface{})
	for k, v := range resp.Updates {
		if v != "" {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		_ = h.store.UpdateTaskFields(resp.TaskID, fields)
	}

	// Build response
	var changes []string
	if newTitle != "" {
		changes = append(changes, fmt.Sprintf("title → %s", newTitle))
	}
	if newDue != "" {
		if due, err := time.Parse("2006-01-02", newDue); err == nil {
			changes = append(changes, fmt.Sprintf("due → %s", due.Format("Mon, Jan 2")))
		}
	}
	if p, ok := resp.Updates["priority"]; ok && p != "" {
		changes = append(changes, fmt.Sprintf("priority → %s", p))
	}

	reply := fmt.Sprintf("Updated '%s':\n", taskRec.Title)
	for _, ch := range changes {
		reply += fmt.Sprintf("  • %s\n", ch)
	}
	h.sendMessage(msg.Chat.ID, reply)
	return true
}

func (h *TelegramBot) tryBulk(ctx context.Context, msg *tgbotapi.Message) bool {
	resp, err := h.llm.ParseBulkIntent(ctx, msg.Text, h.cfg.Timezone)
	if err != nil {
		log.Printf("⚠️ Bulk intent check failed: %v", err)
		return false
	}

	if !resp.IsBulkIntent {
		return false
	}

	h.executeBulk(msg.Chat.ID, resp)
	return true
}

func (h *TelegramBot) executeBulk(chatID int64, intent *llm.BulkIntent) {
	var targetTasks []store.TaskRecord
	var err error

	switch intent.Filter {
	case "category":
		targetTasks, err = h.store.GetActiveTasksByCategory(intent.Category)
	case "overdue":
		loc, _ := time.LoadLocation(h.cfg.Timezone)
		if loc == nil {
			loc = time.UTC
		}
		today := time.Now().In(loc).Format("2006-01-02")
		targetTasks, err = h.store.GetOverdueTasks(today)
	case "all":
		targetTasks, err = h.store.GetActiveTasks()
	default:
		h.sendMessage(chatID, "I couldn't understand the bulk filter. Try: `/snooze shopping monday` or `/complete overdue`")
		return
	}

	if err != nil || len(targetTasks) == 0 {
		h.sendMessage(chatID, "No matching tasks found.")
		return
	}

	var successCount int
	switch intent.Action {
	case "complete":
		for _, t := range targetTasks {
			if t.GTaskID != "" && t.GListID != "" {
				if err := h.tasks.CompleteTask(t.GListID, t.GTaskID); err == nil {
					_ = h.store.MarkCompleted(t.ID)
					successCount++
				}
			}
		}
		h.sendMessage(chatID, fmt.Sprintf("Completed %d/%d tasks.", successCount, len(targetTasks)))

	case "snooze":
		if intent.NewDate == "" {
			h.sendMessage(chatID, "Please specify a date to snooze to.")
			return
		}
		for _, t := range targetTasks {
			if t.GTaskID != "" && t.GListID != "" {
				if err := h.tasks.UpdateTaskDueDate(t.GListID, t.GTaskID, intent.NewDate); err == nil {
					_ = h.store.UpdateTaskFields(t.ID, map[string]interface{}{"due_date": intent.NewDate})
					successCount++
				}
			}
		}
		dateLabel := intent.NewDate
		if due, err := time.Parse("2006-01-02", intent.NewDate); err == nil {
			dateLabel = due.Format("Mon, Jan 2")
		}
		h.sendMessage(chatID, fmt.Sprintf("Snoozed %d/%d tasks to %s.", successCount, len(targetTasks), dateLabel))

	default:
		h.sendMessage(chatID, "Unknown bulk action.")
	}
}

func (h *TelegramBot) handleSnoozeCommand(chatID int64, args string) {
	parts := strings.Fields(args)
	if len(parts) < 2 {
		h.sendMessage(chatID, "Usage: `/snooze <category> <date>`\nExample: `/snooze shopping monday`")
		return
	}

	cat := normalizeConfigCategory(parts[0])
	if cat == "" {
		h.sendMessage(chatID, "Unknown category. Use: personal, office, shopping, others")
		return
	}

	// Use LLM to resolve date, or parse simple ones
	dateStr := strings.Join(parts[1:], " ")
	intent := &llm.BulkIntent{
		IsBulkIntent: true,
		Action:       "snooze",
		Filter:       "category",
		Category:     cat,
		NewDate:      resolveDateString(dateStr, h.cfg.Timezone),
	}

	if intent.NewDate == "" {
		h.sendMessage(chatID, "Could not understand the date. Try: `monday`, `tomorrow`, `2026-04-07`")
		return
	}

	h.executeBulk(chatID, intent)
}

func (h *TelegramBot) handleCompleteCommand(chatID int64, args string) {
	args = strings.ToLower(strings.TrimSpace(args))
	if args == "" {
		h.sendMessage(chatID, "Usage: `/complete overdue` or `/complete <category>`")
		return
	}

	intent := &llm.BulkIntent{
		IsBulkIntent: true,
		Action:       "complete",
	}

	if args == "overdue" {
		intent.Filter = "overdue"
	} else if args == "all" {
		intent.Filter = "all"
	} else {
		cat := normalizeConfigCategory(args)
		if cat == "" {
			h.sendMessage(chatID, "Unknown filter. Use: `overdue`, `all`, or a category name")
			return
		}
		intent.Filter = "category"
		intent.Category = cat
	}

	h.executeBulk(chatID, intent)
}

func resolveDateString(s string, timezone string) string {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	s = strings.ToLower(strings.TrimSpace(s))

	// Try direct parse
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Format("2006-01-02")
	}

	switch s {
	case "today":
		return now.Format("2006-01-02")
	case "tomorrow":
		return now.AddDate(0, 0, 1).Format("2006-01-02")
	}

	// Day of week
	dayMap := map[string]time.Weekday{
		"monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
		"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday, "sunday": time.Sunday,
	}
	if target, ok := dayMap[s]; ok {
		daysUntil := (int(target) - int(now.Weekday()) + 7) % 7
		if daysUntil == 0 {
			daysUntil = 7
		}
		return now.AddDate(0, 0, daysUntil).Format("2006-01-02")
	}

	return ""
}

func (h *TelegramBot) handleStats(chatID int64) {
	loc, err := time.LoadLocation(h.cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")

	// Find Monday of this week
	weekday := now.Weekday()
	if weekday == time.Sunday {
		weekday = 7
	}
	monday := now.AddDate(0, 0, -int(weekday-time.Monday))
	weekStart := time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, loc).Format("2006-01-02")
	weekEnd := now.AddDate(0, 0, 1).Format("2006-01-02")

	completed, _ := h.store.GetCompletedInRange(weekStart, weekEnd)
	created, _ := h.store.GetTasksCreatedInRange(weekStart, weekEnd)
	streak, _ := h.store.GetConsecutiveCompletionDays(today)

	// Category breakdown
	catCount := map[string]int{}
	for _, t := range completed {
		catCount[t.Category]++
	}

	var sb strings.Builder
	sb.WriteString("*Your Stats*\n\n")

	if streak > 0 {
		sb.WriteString(fmt.Sprintf("Current Streak: %d day", streak))
		if streak != 1 {
			sb.WriteString("s")
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("Completed this week: %d\n", len(completed)))
	sb.WriteString(fmt.Sprintf("Added this week: %d\n", len(created)))

	if len(catCount) > 0 {
		sb.WriteString("\n*Category Breakdown (this week):*\n")
		for _, cat := range []string{"office", "personal", "shopping", "others"} {
			if count, ok := catCount[cat]; ok {
				sb.WriteString(fmt.Sprintf("  %s: %d\n", tasks.CategoryName(cat), count))
			}
		}
	}

	h.sendMessage(chatID, sb.String())
}

func (h *TelegramBot) handleConfig(chatID int64, args string) {
	if args == "" {
		// Show current settings
		skipCats, _ := h.store.GetPreference("digest_skip_categories")
		lookahead, _ := h.store.GetPreference("digest_lookahead_days")
		showNoDue, _ := h.store.GetPreference("digest_show_no_due")
		if lookahead == "" {
			lookahead = "3"
		}
		if showNoDue == "" {
			showNoDue = "on"
		}
		if skipCats == "" {
			skipCats = "none"
		}

		var sb strings.Builder
		sb.WriteString("*Digest Settings*\n\n")
		sb.WriteString(fmt.Sprintf("Lookahead days: %s\n", lookahead))
		sb.WriteString(fmt.Sprintf("Show no-due tasks: %s\n", showNoDue))
		sb.WriteString(fmt.Sprintf("Skipped categories: %s\n", skipCats))
		sb.WriteString("\n*Usage:*\n")
		sb.WriteString("`/config lookahead 5`\n")
		sb.WriteString("`/config skip shopping`\n")
		sb.WriteString("`/config unskip shopping`\n")
		sb.WriteString("`/config nodue off|on`\n")
		h.sendMessage(chatID, sb.String())
		return
	}

	parts := strings.Fields(args)
	if len(parts) < 2 {
		h.sendMessage(chatID, "Usage: `/config <setting> <value>`")
		return
	}

	setting := strings.ToLower(parts[0])
	value := strings.ToLower(parts[1])

	switch setting {
	case "lookahead":
		if n, err := fmt.Sscanf(value, "%s", &value); n == 0 || err != nil {
			h.sendMessage(chatID, "Please provide a number. Example: `/config lookahead 5`")
			return
		}
		_ = h.store.SetPreference("digest_lookahead_days", value)
		h.sendMessage(chatID, fmt.Sprintf("Lookahead set to %s days.", value))

	case "skip":
		cat := normalizeConfigCategory(value)
		if cat == "" {
			h.sendMessage(chatID, "Unknown category. Use: personal, office, shopping, others")
			return
		}
		current, _ := h.store.GetPreference("digest_skip_categories")
		cats := splitCSV(current)
		for _, c := range cats {
			if c == cat {
				h.sendMessage(chatID, fmt.Sprintf("%s is already skipped.", tasks.CategoryName(cat)))
				return
			}
		}
		cats = append(cats, cat)
		_ = h.store.SetPreference("digest_skip_categories", strings.Join(cats, ","))
		h.sendMessage(chatID, fmt.Sprintf("Skipping %s in daily digest.", tasks.CategoryName(cat)))

	case "unskip":
		cat := normalizeConfigCategory(value)
		if cat == "" {
			h.sendMessage(chatID, "Unknown category. Use: personal, office, shopping, others")
			return
		}
		current, _ := h.store.GetPreference("digest_skip_categories")
		cats := splitCSV(current)
		var newCats []string
		for _, c := range cats {
			if c != cat {
				newCats = append(newCats, c)
			}
		}
		_ = h.store.SetPreference("digest_skip_categories", strings.Join(newCats, ","))
		h.sendMessage(chatID, fmt.Sprintf("Re-included %s in daily digest.", tasks.CategoryName(cat)))

	case "nodue":
		if value != "on" && value != "off" {
			h.sendMessage(chatID, "Use: `/config nodue on` or `/config nodue off`")
			return
		}
		_ = h.store.SetPreference("digest_show_no_due", value)
		h.sendMessage(chatID, fmt.Sprintf("No-due-date section: %s", value))

	default:
		h.sendMessage(chatID, "Unknown setting. Available: `lookahead`, `skip`, `unskip`, `nodue`")
	}
}

func normalizeConfigCategory(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "personal", "office", "shopping", "others":
		return s
	case "work":
		return "office"
	default:
		return ""
	}
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
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
