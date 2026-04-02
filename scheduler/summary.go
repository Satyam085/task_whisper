package scheduler

import (
	"fmt"
	"log"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"taskwhisperer/config"
	"taskwhisperer/store"
	"taskwhisperer/tasks"
)

// MessageSender is the interface for sending notifications.
type MessageSender interface {
	SendNotification(text string)
}

// Scheduler handles the daily summary and timezone-aware reminders.
type Scheduler struct {
	tasksSvc *tasks.Service
	lists    *tasks.ListMapping
	senders  []MessageSender
	store    *store.Store
	cfg      *config.Config
}

// NewScheduler creates a new daily summary and reminders scheduler.
func NewScheduler(tasksSvc *tasks.Service, lists *tasks.ListMapping, senders []MessageSender, st *store.Store, cfg *config.Config) *Scheduler {
	return &Scheduler{
		tasksSvc: tasksSvc,
		lists:    lists,
		senders:  senders,
		store:    st,
		cfg:      cfg,
	}
}

// Start begins the daily summary and reminder scheduler in goroutines.
func (s *Scheduler) Start() {
	go func() {
		for {
			nextRun := s.nextSummaryTime()
			waitDuration := time.Until(nextRun)
			log.Printf("⏰ Next daily summary at %s (in %s)", nextRun.Format("2006-01-02 15:04"), waitDuration.Round(time.Minute))

			time.Sleep(waitDuration)

			log.Println("📬 Sending daily summary...")
			s.sendDailySummary()
		}
	}()
	
	go s.runRemindersLoop()
	go s.runOverdueLoop()
	go s.runWeeklySummaryLoop()
}

func (s *Scheduler) runRemindersLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	
	for range ticker.C {
		s.checkReminders()
	}
}

func (s *Scheduler) checkReminders() {
	loc, err := time.LoadLocation(s.cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	dateStr := now.Format("2006-01-02")
	timeStr := now.Format("15:04") // HH:MM
	
	dueTasks, err := s.store.GetTasksDueAt(dateStr, timeStr)
	if err != nil || len(dueTasks) == 0 {
		return
	}
	
	for _, t := range dueTasks {
		msg := fmt.Sprintf("⏰ *Reminder*: %s\n_Category: %s_", t.Title, tasks.CategoryName(t.Category))
		for _, sender := range s.senders {
			sender.SendNotification(msg)
		}
		_ = s.store.MarkTaskReminded(t.ID)
		log.Printf("⏰ Sent timezone-aware reminder for task: %q", t.Title)
	}
}

func (s *Scheduler) runOverdueLoop() {
	interval := time.Duration(s.cfg.OverdueCheckInterval) * time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("⏰ Overdue check every %d minutes", s.cfg.OverdueCheckInterval)

	// Check immediately on start, then on ticker
	s.checkOverdue()
	for range ticker.C {
		s.checkOverdue()
	}
}

func (s *Scheduler) checkOverdue() {
	loc, err := time.LoadLocation(s.cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	today := time.Now().In(loc).Format("2006-01-02")

	overdue, err := s.store.GetOverdueTasks(today)
	if err != nil || len(overdue) == 0 {
		return
	}

	for _, t := range overdue {
		dateLabel := t.DueDate
		if due, err := time.Parse("2006-01-02", t.DueDate); err == nil {
			dateLabel = due.Format("Jan 2")
		}
		msg := fmt.Sprintf("*Overdue*: %s\n_Was due: %s | %s_", t.Title, dateLabel, tasks.CategoryName(t.Category))
		for _, sender := range s.senders {
			sender.SendNotification(msg)
		}
		_ = s.store.MarkOverdueNotified(t.ID)
		log.Printf("⚠️ Sent overdue alert for task: %q (due %s)", t.Title, t.DueDate)
	}
}

func (s *Scheduler) runWeeklySummaryLoop() {
	for {
		nextRun := s.nextWeeklySummaryTime()
		waitDuration := time.Until(nextRun)
		log.Printf("📊 Next weekly summary at %s (in %s)", nextRun.Format("2006-01-02 15:04"), waitDuration.Round(time.Minute))

		time.Sleep(waitDuration)

		log.Println("📊 Sending weekly summary...")
		text := s.GenerateWeeklySummary()
		for _, sender := range s.senders {
			sender.SendNotification(text)
		}
		log.Println("✅ Weekly summary sent")
	}
}

func (s *Scheduler) nextWeeklySummaryTime() time.Time {
	loc, err := time.LoadLocation(s.cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)

	hour, min := 20, 0
	if parts := strings.Split(s.cfg.WeeklySummaryTime, ":"); len(parts) == 2 {
		fmt.Sscanf(parts[0], "%d", &hour)
		fmt.Sscanf(parts[1], "%d", &min)
	}

	targetDay := parseDayOfWeek(s.cfg.WeeklySummaryDay)
	daysUntil := (int(targetDay) - int(now.Weekday()) + 7) % 7
	if daysUntil == 0 {
		candidate := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, loc)
		if now.After(candidate) {
			daysUntil = 7
		}
	}

	next := time.Date(now.Year(), now.Month(), now.Day()+daysUntil, hour, min, 0, 0, loc)
	return next
}

func parseDayOfWeek(day string) time.Weekday {
	switch strings.ToLower(day) {
	case "monday":
		return time.Monday
	case "tuesday":
		return time.Tuesday
	case "wednesday":
		return time.Wednesday
	case "thursday":
		return time.Thursday
	case "friday":
		return time.Friday
	case "saturday":
		return time.Saturday
	default:
		return time.Sunday
	}
}

// GenerateWeeklySummary creates the weekly rollup text.
func (s *Scheduler) GenerateWeeklySummary() string {
	loc, err := time.LoadLocation(s.cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)

	// Find Monday of this week
	weekday := now.Weekday()
	if weekday == time.Sunday {
		weekday = 7
	}
	monday := now.AddDate(0, 0, -int(weekday-time.Monday))
	weekStart := time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, loc)
	weekEnd := weekStart.AddDate(0, 0, 7)

	weekStartStr := weekStart.Format("2006-01-02")
	weekEndStr := weekEnd.Format("2006-01-02")

	completed, _ := s.store.GetCompletedInRange(weekStartStr, weekEndStr)
	created, _ := s.store.GetTasksCreatedInRange(weekStartStr, weekEndStr)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*Weekly Rollup* (%s - %s)\n\n", weekStart.Format("Jan 2"), weekEnd.AddDate(0, 0, -1).Format("Jan 2")))

	// Completed
	sb.WriteString(fmt.Sprintf("*COMPLETED THIS WEEK:* %d\n", len(completed)))
	for _, t := range completed {
		sb.WriteString(fmt.Sprintf("  • %s — %s\n", t.Title, tasks.CategoryName(t.Category)))
	}
	sb.WriteString("\n")

	// Added
	sb.WriteString(fmt.Sprintf("*ADDED THIS WEEK:* %d\n\n", len(created)))

	// Upcoming next week
	nextWeekStart := weekEnd
	var upcomingTasks []string
	for _, cat := range s.lists.AllCategories() {
		listID := s.lists.GetListID(cat)
		upcoming, err := s.tasksSvc.GetUpcomingTasks(listID, nextWeekStart, 7)
		if err != nil {
			continue
		}
		for _, t := range upcoming {
			dateLabel := t.DueDate
			if due, err := time.Parse("2006-01-02", t.DueDate); err == nil {
				dateLabel = due.Format("Jan 2")
			}
			upcomingTasks = append(upcomingTasks, fmt.Sprintf("  • %s — %s — %s", t.Title, dateLabel, tasks.CategoryName(cat)))
		}
	}
	if len(upcomingTasks) > 0 {
		sb.WriteString(fmt.Sprintf("*UPCOMING NEXT WEEK:* %d\n", len(upcomingTasks)))
		for _, line := range upcomingTasks {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	// Overdue
	today := now.Format("2006-01-02")
	overdue, _ := s.store.GetOverdueTasks(today)
	if len(overdue) > 0 {
		sb.WriteString(fmt.Sprintf("*OVERDUE:* %d\n", len(overdue)))
		for _, t := range overdue {
			sb.WriteString(fmt.Sprintf("  • %s — %s — %s\n", t.Title, t.DueDate, tasks.CategoryName(t.Category)))
		}
	}

	return sb.String()
}

func (s *Scheduler) nextSummaryTime() time.Time {
	loc, err := time.LoadLocation(s.cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}

	now := time.Now().In(loc)

	// Parse configured summary time (HH:MM)
	hour, min := 8, 0
	if parts := strings.Split(s.cfg.SummaryTime, ":"); len(parts) == 2 {
		fmt.Sscanf(parts[0], "%d", &hour)
		fmt.Sscanf(parts[1], "%d", &min)
	}

	next := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, loc)

	// If it's already past today's summary time, schedule for tomorrow
	if now.After(next) {
		next = next.Add(24 * time.Hour)
	}

	return next
}

func (s *Scheduler) sendDailySummary() {
	text := s.GenerateSummary()
	for _, sender := range s.senders {
		sender.SendNotification(text)
	}
	log.Println("✅ Daily summary sent")
}

// GenerateSummary explicitly runs and generates the text of the daily summary.
func (s *Scheduler) GenerateSummary() string {
	loc, err := time.LoadLocation(s.cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	// Load preferences
	skipCatsStr, _ := s.store.GetPreference("digest_skip_categories")
	skipCats := make(map[string]bool)
	for _, c := range strings.Split(skipCatsStr, ",") {
		c = strings.TrimSpace(c)
		if c != "" {
			skipCats[c] = true
		}
	}
	lookaheadDays := 3
	if val, _ := s.store.GetPreference("digest_lookahead_days"); val != "" {
		if n, err := fmt.Sscanf(val, "%d", &lookaheadDays); n == 0 || err != nil {
			lookaheadDays = 3
		}
	}
	showNoDue := true
	if val, _ := s.store.GetPreference("digest_show_no_due"); val == "off" {
		showNoDue = false
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Good morning! Here's your %s briefing:\n\n", now.Format("Jan 2")))

	allCategories := []struct {
		name string
		cat  string
	}{
		{"Personal", "personal"},
		{"Work", "office"},
		{"Shopping", "shopping"},
		{"Miscellaneous", "others"},
	}

	// Filter out skipped categories
	var categories []struct {
		name string
		cat  string
	}
	for _, c := range allCategories {
		if !skipCats[c.cat] {
			categories = append(categories, c)
		}
	}

	var todayTasks []string
	var upcomingTasks []string
	var noDueTasks []string

	var eg errgroup.Group
	catTodayTasks := make([][]string, len(categories))
	catUpcomingTasks := make([][]string, len(categories))
	catNoDueTasks := make([][]string, len(categories))

	for i, c := range categories {
		idx := i
		cat := c

		eg.Go(func() error {
			listID := s.lists.GetListID(cat.cat)

			// Today's tasks
			dayTasks, err := s.tasksSvc.GetTasksDueOn(listID, today)
			if err != nil {
				log.Printf("Warning: Error fetching %s tasks for today: %v", cat.name, err)
			} else {
				var tTasks []string
				for _, t := range dayTasks {
					if t.Status == "needsAction" {
						tTasks = append(tTasks, fmt.Sprintf("  - %s — %s", t.Title, cat.name))
					}
				}
				catTodayTasks[idx] = tTasks
			}

			// Upcoming (configurable lookahead, excluding today)
			tomorrow := today.Add(24 * time.Hour)
			upcoming, err := s.tasksSvc.GetUpcomingTasks(listID, tomorrow, lookaheadDays)
			if err != nil {
				log.Printf("Warning: Error fetching upcoming %s tasks: %v", cat.name, err)
			} else {
				var uTasks []string
				for _, t := range upcoming {
					dateLabel := t.DueDate
					if due, err := time.Parse("2006-01-02", t.DueDate); err == nil {
						dateLabel = due.Format("Jan 2")
					}
					uTasks = append(uTasks, fmt.Sprintf("  %s — %s — %s", dateLabel, t.Title, cat.name))
				}
				catUpcomingTasks[idx] = uTasks
			}

			// Tasks without a due date
			if showNoDue {
				noDue, err := s.tasksSvc.GetTasksWithoutDueDate(listID)
				if err != nil {
					log.Printf("Warning: Error fetching tasks without due date for %s: %v", cat.name, err)
				} else {
					var ndTasks []string
					for _, t := range noDue {
						ndTasks = append(ndTasks, fmt.Sprintf("  - %s — %s", t.Title, cat.name))
					}
					catNoDueTasks[idx] = ndTasks
				}
			}

			return nil
		})
	}

	_ = eg.Wait()

	for i := range categories {
		todayTasks = append(todayTasks, catTodayTasks[i]...)
		upcomingTasks = append(upcomingTasks, catUpcomingTasks[i]...)
		noDueTasks = append(noDueTasks, catNoDueTasks[i]...)
	}

	if len(todayTasks) > 0 {
		sb.WriteString("*DUE TODAY:*\n")
		for _, line := range todayTasks {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("Nothing due today!\n\n")
	}

	if len(upcomingTasks) > 0 {
		sb.WriteString("*COMING UP:*\n")
		for _, line := range upcomingTasks {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	if showNoDue && len(noDueTasks) > 0 {
		sb.WriteString("*NO DUE DATE:*\n")
		for _, line := range noDueTasks {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Have a great day!")
	return sb.String()
}
