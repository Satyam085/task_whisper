package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// TaskRecord represents a logged task in SQLite.
type TaskRecord struct {
	ID              int64
	Title           string
	Category        string
	DueDate         string
	DueTime         string
	Recurrence      string
	Priority        string
	GTaskID         string
	GListID         string
	Reminder        bool
	CompletedAt     string
	OverdueNotified bool
	CreatedAt       time.Time
}

// Store provides SQLite-backed task logging.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) the SQLite database and ensures the schema exists.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	}

	// Create table if not exists
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			category TEXT NOT NULL,
			due_date TEXT,
			due_time TEXT DEFAULT '',
			recurrence TEXT DEFAULT '',
			priority TEXT DEFAULT 'normal',
			gtask_id TEXT DEFAULT '',
			glist_id TEXT DEFAULT '',
			reminder_sent INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create tasks table: %w", err)
	}

	// Add new columns to existing tables (ignoring errors if they already exist)
	_, _ = db.Exec(`ALTER TABLE tasks ADD COLUMN gtask_id TEXT DEFAULT '';`)
	_, _ = db.Exec(`ALTER TABLE tasks ADD COLUMN glist_id TEXT DEFAULT '';`)
	_, _ = db.Exec(`ALTER TABLE tasks ADD COLUMN due_time TEXT DEFAULT '';`)
	_, _ = db.Exec(`ALTER TABLE tasks ADD COLUMN reminder_sent INTEGER DEFAULT 0;`)
	_, _ = db.Exec(`ALTER TABLE tasks ADD COLUMN recurrence TEXT DEFAULT '';`)
	_, _ = db.Exec(`ALTER TABLE tasks ADD COLUMN completed_at TEXT DEFAULT '';`)
	_, _ = db.Exec(`ALTER TABLE tasks ADD COLUMN overdue_notified INTEGER DEFAULT 0;`)

	// User preferences table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS user_preferences (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create user_preferences table: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// LogTask records a newly created task.
func (s *Store) LogTask(title, category, dueDate, dueTime, recurrence, priority, gtaskID, glistID string) (int64, error) {
	result, err := s.db.Exec(
		`INSERT INTO tasks (title, category, due_date, due_time, recurrence, priority, gtask_id, glist_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		title, category, dueDate, dueTime, recurrence, priority, gtaskID, glistID,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetTasksCreatedSince returns tasks created after the given time.
func (s *Store) GetTasksCreatedSince(since time.Time) ([]TaskRecord, error) {
	rows, err := s.db.Query(
		`SELECT `+selectAllFields+` FROM tasks WHERE created_at >= ? ORDER BY created_at DESC`,
		since.Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRecords(rows)
}

// GetTodaysTasks returns tasks with due_date matching today.
func (s *Store) GetTodaysTasks() ([]TaskRecord, error) {
	today := time.Now().Format("2006-01-02")
	rows, err := s.db.Query(
		`SELECT `+selectAllFields+` FROM tasks WHERE due_date = ? ORDER BY category, title`,
		today,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRecords(rows)
}

// CountCompletedYesterday returns the count of tasks created yesterday (proxy for completed).
func (s *Store) CountCompletedYesterday() (int, error) {
	yesterday := time.Now().Add(-24 * time.Hour).Format("2006-01-02")
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM tasks WHERE due_date = ?`,
		yesterday,
	).Scan(&count)
	return count, err
}

const selectAllFields = `id, title, category, due_date, due_time, recurrence, priority, gtask_id, glist_id, reminder_sent, completed_at, overdue_notified, created_at`

func scanRecords(rows *sql.Rows) ([]TaskRecord, error) {
	var records []TaskRecord
	for rows.Next() {
		r, err := scanOneRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

type scannable interface {
	Scan(dest ...interface{}) error
}

func scanOneRecord(s scannable) (TaskRecord, error) {
	var r TaskRecord
	var dueDate, dueTime, recurrence, gTaskID, gListID, completedAt sql.NullString
	var reminderSent, overdueNotified int
	var createdAt string
	err := s.Scan(&r.ID, &r.Title, &r.Category, &dueDate, &dueTime, &recurrence, &r.Priority, &gTaskID, &gListID, &reminderSent, &completedAt, &overdueNotified, &createdAt)
	if err != nil {
		return r, err
	}
	if dueDate.Valid {
		r.DueDate = dueDate.String
	}
	if dueTime.Valid {
		r.DueTime = dueTime.String
	}
	if recurrence.Valid {
		r.Recurrence = recurrence.String
	}
	if gTaskID.Valid {
		r.GTaskID = gTaskID.String
	}
	if gListID.Valid {
		r.GListID = gListID.String
	}
	if completedAt.Valid {
		r.CompletedAt = completedAt.String
	}
	r.Reminder = (reminderSent == 1)
	r.OverdueNotified = (overdueNotified == 1)
	if t, err := time.Parse("2006-01-02 15:04:05", createdAt); err == nil {
		r.CreatedAt = t
	}
	return r, nil
}

// GetTaskByID retrieves a single task by its internal ID.
func (s *Store) GetTaskByID(id int64) (*TaskRecord, error) {
	row := s.db.QueryRow(
		`SELECT `+selectAllFields+` FROM tasks WHERE id = ?`,
		id,
	)
	r, err := scanOneRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("task not found")
		}
		return nil, err
	}
	return &r, nil
}

// GetTasksDueAt returns tasks due at a specific date and time that haven't been reminded.
func (s *Store) GetTasksDueAt(date, timeStr string) ([]TaskRecord, error) {
	rows, err := s.db.Query(
		`SELECT `+selectAllFields+` FROM tasks WHERE due_date = ? AND due_time = ? AND reminder_sent = 0 AND completed_at = ''`,
		date, timeStr,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRecords(rows)
}

// MarkTaskReminded sets reminder_sent to 1 for the given task.
func (s *Store) MarkTaskReminded(id int64) error {
	_, err := s.db.Exec(`UPDATE tasks SET reminder_sent = 1 WHERE id = ?`, id)
	return err
}

// MarkCompleted sets completed_at to current timestamp.
func (s *Store) MarkCompleted(id int64) error {
	_, err := s.db.Exec(`UPDATE tasks SET completed_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

// GetOverdueTasks returns incomplete tasks past their due date that haven't been notified.
func (s *Store) GetOverdueTasks(today string) ([]TaskRecord, error) {
	rows, err := s.db.Query(
		`SELECT `+selectAllFields+` FROM tasks WHERE due_date != '' AND due_date < ? AND completed_at = '' AND overdue_notified = 0 ORDER BY due_date`,
		today,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

// MarkOverdueNotified sets overdue_notified to 1.
func (s *Store) MarkOverdueNotified(id int64) error {
	_, err := s.db.Exec(`UPDATE tasks SET overdue_notified = 1 WHERE id = ?`, id)
	return err
}

// GetCompletedInRange returns tasks completed within the given date range.
func (s *Store) GetCompletedInRange(from, to string) ([]TaskRecord, error) {
	rows, err := s.db.Query(
		`SELECT `+selectAllFields+` FROM tasks WHERE completed_at >= ? AND completed_at < ? ORDER BY completed_at DESC`,
		from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

// GetTasksCreatedInRange returns tasks created within the given date range.
func (s *Store) GetTasksCreatedInRange(from, to string) ([]TaskRecord, error) {
	rows, err := s.db.Query(
		`SELECT `+selectAllFields+` FROM tasks WHERE created_at >= ? AND created_at < ? ORDER BY created_at DESC`,
		from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

// GetActiveTasks returns all incomplete tasks.
func (s *Store) GetActiveTasks() ([]TaskRecord, error) {
	rows, err := s.db.Query(
		`SELECT ` + selectAllFields + ` FROM tasks WHERE completed_at = '' ORDER BY due_date, category`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

// GetActiveTasksByCategory returns incomplete tasks for a specific category.
func (s *Store) GetActiveTasksByCategory(category string) ([]TaskRecord, error) {
	rows, err := s.db.Query(
		`SELECT `+selectAllFields+` FROM tasks WHERE completed_at = '' AND category = ? ORDER BY due_date`,
		category,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

// GetConsecutiveCompletionDays counts consecutive days with at least one completion, walking back from asOf.
func (s *Store) GetConsecutiveCompletionDays(asOf string) (int, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT DATE(completed_at) as d FROM tasks WHERE completed_at != '' ORDER BY d DESC`,
	)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	streak := 0
	expected, err := time.Parse("2006-01-02", asOf)
	if err != nil {
		return 0, err
	}

	for rows.Next() {
		var dateStr string
		if err := rows.Scan(&dateStr); err != nil {
			return streak, err
		}
		d, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		if d.Equal(expected) {
			streak++
			expected = expected.AddDate(0, 0, -1)
		} else if d.Before(expected) {
			break
		}
	}
	return streak, rows.Err()
}

// UpdateTaskFields updates arbitrary fields on a task record.
func (s *Store) UpdateTaskFields(id int64, fields map[string]interface{}) error {
	if len(fields) == 0 {
		return nil
	}
	allowed := map[string]bool{
		"title": true, "due_date": true, "due_time": true,
		"priority": true, "category": true, "recurrence": true,
	}
	var setClauses []string
	var args []interface{}
	for k, v := range fields {
		if !allowed[k] {
			continue
		}
		setClauses = append(setClauses, k+" = ?")
		args = append(args, v)
	}
	if len(setClauses) == 0 {
		return nil
	}
	args = append(args, id)
	query := "UPDATE tasks SET " + strings.Join(setClauses, ", ") + " WHERE id = ?"
	_, err := s.db.Exec(query, args...)
	return err
}

// GetPreference retrieves a user preference value by key.
func (s *Store) GetPreference(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM user_preferences WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SetPreference sets a user preference value.
func (s *Store) SetPreference(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO user_preferences (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = ?`,
		key, value, value,
	)
	return err
}
