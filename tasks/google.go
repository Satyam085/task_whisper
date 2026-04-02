package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	gtasks "google.golang.org/api/tasks/v1"
)

// Service wraps the Google Tasks API client.
type Service struct {
	svc *gtasks.Service
}

// NewService creates a Google Tasks API service using saved OAuth2 credentials.
func NewService(ctx context.Context, credPath, tokenPath string) (*Service, error) {
	credBytes, err := os.ReadFile(credPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read credentials file %s: %w", credPath, err)
	}

	oauthCfg, err := google.ConfigFromJSON(credBytes, gtasks.TasksScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse credentials: %w", err)
	}

	tok, err := loadToken(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("unable to load token from %s: %w", tokenPath, err)
	}

	client := oauthCfg.Client(ctx, tok)
	svc, err := gtasks.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("unable to create tasks service: %w", err)
	}

	return &Service{svc: svc}, nil
}

// InsertTask adds a single task to the specified Google Tasks list and returns it.
func (s *Service) InsertTask(listID, title, notes, dueDate, parent string) (*gtasks.Task, error) {
	task := &gtasks.Task{
		Title: title,
		Notes: notes,
	}

	if dueDate != "" {
		// Google Tasks API expects RFC 3339 format for due dates
		t, err := time.Parse("2006-01-02", dueDate)
		if err == nil {
			task.Due = t.Format(time.RFC3339)
		}
	}

	call := s.svc.Tasks.Insert(listID, task)
	if parent != "" {
		call = call.Parent(parent)
	}

	created, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("failed to insert task %q: %w", title, err)
	}
	return created, nil
}

// CompleteTask marks a specific task as completed in Google Tasks.
func (s *Service) CompleteTask(listID, taskID string) error {
	task, err := s.svc.Tasks.Get(listID, taskID).Do()
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}

	task.Status = "completed"
	_, err = s.svc.Tasks.Update(listID, taskID, task).Do()
	if err != nil {
		return fmt.Errorf("failed to complete task: %w", err)
	}
	return nil
}

// UpdateTaskDueDate changes a task's due date in Google Tasks.
func (s *Service) UpdateTaskDueDate(listID, taskID, newDueDate string) error {
	task, err := s.svc.Tasks.Get(listID, taskID).Do()
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}

	if newDueDate != "" {
		t, err := time.Parse("2006-01-02", newDueDate)
		if err == nil {
			task.Due = t.Format(time.RFC3339)
		}
	} else {
		task.Due = ""
	}

	_, err = s.svc.Tasks.Update(listID, taskID, task).Do()
	if err != nil {
		return fmt.Errorf("failed to update task due date: %w", err)
	}
	return nil
}

// TaskInfo holds basic info about a task retrieved from Google Tasks.
type TaskInfo struct {
	Title   string
	Notes   string
	DueDate string // YYYY-MM-DD
	Status  string // needsAction or completed
}

// GetTasksDueOn returns tasks from a list that are due on the given date.
func (s *Service) GetTasksDueOn(listID string, date time.Time) ([]TaskInfo, error) {
	// Set time range for the full day
	dayStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	dayEnd := dayStart.Add(24 * time.Hour)

	resp, err := s.svc.Tasks.List(listID).
		DueMin(dayStart.Format(time.RFC3339)).
		DueMax(dayEnd.Format(time.RFC3339)).
		ShowCompleted(true).
		MaxResults(100).
		Do()
	if err != nil {
		return nil, fmt.Errorf("failed to list tasks: %w", err)
	}

	var result []TaskInfo
	for _, t := range resp.Items {
		info := TaskInfo{
			Title:  t.Title,
			Notes:  t.Notes,
			Status: t.Status,
		}
		if t.Due != "" {
			if due, err := time.Parse(time.RFC3339, t.Due); err == nil {
				info.DueDate = due.Format("2006-01-02")
			}
		}
		result = append(result, info)
	}
	return result, nil
}

// GetUpcomingTasks returns incomplete tasks from a list due within the next N days.
func (s *Service) GetUpcomingTasks(listID string, from time.Time, days int) ([]TaskInfo, error) {
	dayStart := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, from.Location())
	dayEnd := dayStart.Add(time.Duration(days) * 24 * time.Hour)

	resp, err := s.svc.Tasks.List(listID).
		DueMin(dayStart.Format(time.RFC3339)).
		DueMax(dayEnd.Format(time.RFC3339)).
		ShowCompleted(false).
		MaxResults(100).
		Do()
	if err != nil {
		return nil, fmt.Errorf("failed to list upcoming tasks: %w", err)
	}

	var result []TaskInfo
	for _, t := range resp.Items {
		info := TaskInfo{
			Title:  t.Title,
			Notes:  t.Notes,
			Status: t.Status,
		}
		if t.Due != "" {
			if due, err := time.Parse(time.RFC3339, t.Due); err == nil {
				info.DueDate = due.Format("2006-01-02")
			}
		}
		result = append(result, info)
	}
	return result, nil
}

// GetTasksWithoutDueDate returns incomplete tasks from a list that have no due date set.
func (s *Service) GetTasksWithoutDueDate(listID string) ([]TaskInfo, error) {
	resp, err := s.svc.Tasks.List(listID).
		ShowCompleted(false).
		MaxResults(100).
		Do()
	if err != nil {
		return nil, fmt.Errorf("failed to list tasks: %w", err)
	}

	var result []TaskInfo
	for _, t := range resp.Items {
		// Only include tasks that are incomplete and have no due date
		if t.Due == "" && t.Status == "needsAction" {
			info := TaskInfo{
				Title:  t.Title,
				Notes:  t.Notes,
				Status: t.Status,
			}
			result = append(result, info)
		}
	}
	return result, nil
}

// GetOverdueTasks returns incomplete tasks from a list that are past their due date.
func (s *Service) GetOverdueTasks(listID string, before time.Time) ([]TaskInfo, error) {
	resp, err := s.svc.Tasks.List(listID).
		DueMax(before.Format(time.RFC3339)).
		ShowCompleted(false).
		MaxResults(100).
		Do()
	if err != nil {
		return nil, fmt.Errorf("failed to list overdue tasks: %w", err)
	}

	var result []TaskInfo
	for _, t := range resp.Items {
		info := TaskInfo{
			Title:  t.Title,
			Notes:  t.Notes,
			Status: t.Status,
		}
		if t.Due != "" {
			if due, err := time.Parse(time.RFC3339, t.Due); err == nil {
				info.DueDate = due.Format("2006-01-02")
			}
		}
		result = append(result, info)
	}
	return result, nil
}

// UpdateTask updates a task's title, notes, and/or due date. Empty strings are skipped.
func (s *Service) UpdateTask(listID, taskID, title, notes, dueDate string) error {
	task, err := s.svc.Tasks.Get(listID, taskID).Do()
	if err != nil {
		return fmt.Errorf("failed to get task: %w", err)
	}

	if title != "" {
		task.Title = title
	}
	if notes != "" {
		task.Notes = notes
	}
	if dueDate != "" {
		t, err := time.Parse("2006-01-02", dueDate)
		if err == nil {
			task.Due = t.Format(time.RFC3339)
		}
	}

	_, err = s.svc.Tasks.Update(listID, taskID, task).Do()
	if err != nil {
		return fmt.Errorf("failed to update task: %w", err)
	}
	return nil
}

func loadToken(path string) (*oauth2.Token, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	return tok, json.NewDecoder(f).Decode(tok)
}
