package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TaskRecord represents a logged task in SQLite.
type TaskRecord struct {
	ID        int64
	Title     string
	Category  string
	DueDate   string
	Priority  string
	CreatedAt time.Time
}

// Store provides SQLite-backed task logging.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) the SQLite database and ensures the schema exists.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath)
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
			priority TEXT DEFAULT 'normal',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create tasks table: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// LogTask records a newly created task.
func (s *Store) LogTask(title, category, dueDate, priority string) error {
	_, err := s.db.Exec(
		`INSERT INTO tasks (title, category, due_date, priority) VALUES (?, ?, ?, ?)`,
		title, category, dueDate, priority,
	)
	return err
}

// GetTasksCreatedSince returns tasks created after the given time.
func (s *Store) GetTasksCreatedSince(since time.Time) ([]TaskRecord, error) {
	rows, err := s.db.Query(
		`SELECT id, title, category, due_date, priority, created_at FROM tasks WHERE created_at >= ? ORDER BY created_at DESC`,
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
		`SELECT id, title, category, due_date, priority, created_at FROM tasks WHERE due_date = ? ORDER BY category, title`,
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
		var createdAt string
		if err := rows.Scan(&r.ID, &r.Title, &r.Category, &dueDate, &r.Priority, &createdAt); err != nil {
			return nil, err
		}
		if dueDate.Valid {
			r.DueDate = dueDate.String
		}
		if t, err := time.Parse("2006-01-02 15:04:05", createdAt); err == nil {
			r.CreatedAt = t
		}
		records = append(records, r)
	}
	return records, rows.Err()
}
