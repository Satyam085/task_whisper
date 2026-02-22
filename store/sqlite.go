package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// TaskRecord represents a logged task in SQLite.
type TaskRecord struct {
	ID         int64
	Title      string
	Category   string
	DueDate    string
	DueTime    string
	Recurrence string
	Priority   string
	GTaskID    string
	GListID    string
	Reminder   bool
	CreatedAt  time.Time
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
		`SELECT id, title, category, due_date, due_time, recurrence, priority, gtask_id, glist_id, reminder_sent, created_at FROM tasks WHERE created_at >= ? ORDER BY created_at DESC`,
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
		`SELECT id, title, category, due_date, due_time, recurrence, priority, gtask_id, glist_id, reminder_sent, created_at FROM tasks WHERE due_date = ? ORDER BY category, title`,
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

func scanRecords(rows *sql.Rows) ([]TaskRecord, error) {
	var records []TaskRecord
	for rows.Next() {
		var r TaskRecord
		var dueDate sql.NullString
		var dueTime sql.NullString
		var recurrence sql.NullString
		var gTaskID sql.NullString
		var gListID sql.NullString
		var reminderSent int
		var createdAt string
		if err := rows.Scan(&r.ID, &r.Title, &r.Category, &dueDate, &dueTime, &recurrence, &r.Priority, &gTaskID, &gListID, &reminderSent, &createdAt); err != nil {
			return nil, err
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
		r.Reminder = (reminderSent == 1)
		if t, err := time.Parse("2006-01-02 15:04:05", createdAt); err == nil {
			r.CreatedAt = t
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// GetTaskByID retrieves a single task by its internal ID.
func (s *Store) GetTaskByID(id int64) (*TaskRecord, error) {
	row := s.db.QueryRow(
		`SELECT id, title, category, due_date, due_time, recurrence, priority, gtask_id, glist_id, reminder_sent, created_at FROM tasks WHERE id = ?`,
		id,
	)
	var r TaskRecord
	var dueDate, dueTime, recurrence, gTaskID, gListID sql.NullString
	var reminderSent int
	var createdAt string
	
	err := row.Scan(&r.ID, &r.Title, &r.Category, &dueDate, &dueTime, &recurrence, &r.Priority, &gTaskID, &gListID, &reminderSent, &createdAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("task not found")
		}
		return nil, err
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
	r.Reminder = (reminderSent == 1)
	if t, err := time.Parse("2006-01-02 15:04:05", createdAt); err == nil {
		r.CreatedAt = t
	}
	return &r, nil
}

// GetTasksDueAt returns tasks due at a specific date and time that haven't been reminded.
func (s *Store) GetTasksDueAt(date, time string) ([]TaskRecord, error) {
	rows, err := s.db.Query(
		`SELECT id, title, category, due_date, due_time, recurrence, priority, gtask_id, glist_id, reminder_sent, created_at FROM tasks WHERE due_date = ? AND due_time = ? AND reminder_sent = 0`,
		date, time,
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
