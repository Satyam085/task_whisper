package scheduler

import (
	"fmt"
	"log"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"taskwhisperer/config"
	"taskwhisperer/tasks"
)

// MessageSender is the interface for sending Telegram messages.
type MessageSender interface {
	SendMessage(chatID int64, text string)
}

// Scheduler handles the daily summary goroutine.
type Scheduler struct {
	tasksSvc *tasks.Service
	lists    *tasks.ListMapping
	sender   MessageSender
	cfg      *config.Config
}

// NewScheduler creates a new daily summary scheduler.
func NewScheduler(tasksSvc *tasks.Service, lists *tasks.ListMapping, sender MessageSender, cfg *config.Config) *Scheduler {
	return &Scheduler{
		tasksSvc: tasksSvc,
		lists:    lists,
		sender:   sender,
		cfg:      cfg,
	}
}

// Start begins the daily summary scheduler in a goroutine.
// It calculates the next summary time and sleeps until then.
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
	s.sender.SendMessage(s.cfg.ChatID, text)
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

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("☀️ Good morning! Here's your %s briefing:\n\n", now.Format("Jan 2")))

	categories := []struct {
		name string
		cat  string
	}{
		{"Personal", "personal"},
		{"Work", "office"},
		{"Shopping", "shopping"},
		{"Miscellaneous", "others"},
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
				log.Printf("⚠️ Error fetching %s tasks for today: %v", cat.name, err)
			} else {
				var tTasks []string
				for _, t := range dayTasks {
					if t.Status == "needsAction" {
						tTasks = append(tTasks, fmt.Sprintf("  ☐ %s — %s", t.Title, cat.name))
					}
				}
				catTodayTasks[idx] = tTasks
			}

			// Upcoming (next 3 days, excluding today)
			tomorrow := today.Add(24 * time.Hour)
			upcoming, err := s.tasksSvc.GetUpcomingTasks(listID, tomorrow, 3)
			if err != nil {
				log.Printf("⚠️ Error fetching upcoming %s tasks: %v", cat.name, err)
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
			noDue, err := s.tasksSvc.GetTasksWithoutDueDate(listID)
			if err != nil {
				log.Printf("⚠️ Error fetching tasks without due date for %s: %v", cat.name, err)
			} else {
				var ndTasks []string
				for _, t := range noDue {
					ndTasks = append(ndTasks, fmt.Sprintf("  ☐ %s — %s", t.Title, cat.name))
				}
				catNoDueTasks[idx] = ndTasks
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
		sb.WriteString("📋 DUE TODAY:\n")
		for _, line := range todayTasks {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("📋 Nothing due today — enjoy the free time! 🎉\n\n")
	}

	if len(upcomingTasks) > 0 {
		sb.WriteString("📆 COMING UP:\n")
		for _, line := range upcomingTasks {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	if len(noDueTasks) > 0 {
		sb.WriteString("📌 NO DUE DATE:\n")
		for _, line := range noDueTasks {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Have a great day! \U0001f680")
	return sb.String()
}
