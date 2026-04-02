package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

// Task represents a single task extracted by the LLM from the user's message.
type Task struct {
	Title      string   `json:"title"`
	Notes      string   `json:"notes,omitempty"`
	DueDate    string   `json:"due_date,omitempty"` // YYYY-MM-DD
	DueTime    string   `json:"due_time,omitempty"` // HH:MM (24h)
	Recurrence string   `json:"recurrence,omitempty"` // daily, weekly, monthly, yearly
	Category   string   `json:"category"`             // personal, office, shopping, others
	Priority   string   `json:"priority"`             // low, normal, high
	Subtasks   []string `json:"subtasks,omitempty"`   // List of subtasks for a project
}

// LLMResponse wraps the expected JSON response from the LLM.
type LLMResponse struct {
	Tasks []Task `json:"tasks"`
}

// TaskMatch represents a matched task from the active task list.
type TaskMatch struct {
	TaskID     int64  `json:"task_id"`
	Title      string `json:"title"`
	Confidence string `json:"confidence"` // high, medium, low
}

// CompletionResponse represents the LLM's analysis of a completion intent.
type CompletionResponse struct {
	IsCompletionIntent bool        `json:"is_completion_intent"`
	Matches            []TaskMatch `json:"matches"`
	Clarification      string      `json:"clarification,omitempty"`
}

// EditIntent represents the LLM's analysis of an edit intent.
type EditIntent struct {
	IsEditIntent  bool              `json:"is_edit_intent"`
	TaskID        int64             `json:"task_id"`
	TaskTitle     string            `json:"task_title"`
	Updates       map[string]string `json:"updates"`
	Confidence    string            `json:"confidence"`
	Clarification string            `json:"clarification,omitempty"`
}

// BulkIntent represents a parsed bulk operation.
type BulkIntent struct {
	IsBulkIntent bool   `json:"is_bulk_intent"`
	Action       string `json:"action"`   // complete, snooze
	Filter       string `json:"filter"`   // category:<name>, overdue, all
	Category     string `json:"category,omitempty"`
	NewDate      string `json:"new_date,omitempty"` // YYYY-MM-DD for snooze
}

// ActiveTask is a simplified task representation sent to the LLM for matching.
type ActiveTask struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	DueDate  string `json:"due_date,omitempty"`
}

// Client wraps the Gemini client.
type Client struct {
	client *genai.Client
}

// NewClient creates a new Gemini client configured for task parsing.
func NewClient(ctx context.Context, apiKey string) (*Client, error) {
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create gemini client: %w", err)
	}
	return &Client{client: client}, nil
}

// Close releases the underlying Gemini client resources.
func (c *Client) Close() error {
	return c.client.Close()
}

// ParseTasks sends a user message to Gemini and returns structured tasks.
func (c *Client) ParseTasks(ctx context.Context, message, timezone string) ([]Task, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	weekday := now.Weekday().String()

	systemPrompt := buildSystemPrompt(today, weekday, timezone)
	raw, err := c.callGemini(ctx, systemPrompt, message)
	if err != nil {
		return nil, err
	}

	var result LLMResponse
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("failed to parse llm JSON: %w (raw: %s)", err, raw)
	}

	// Normalize categories
	for i := range result.Tasks {
		result.Tasks[i].Category = normalizeCategory(result.Tasks[i].Category)
		if result.Tasks[i].Priority == "" {
			result.Tasks[i].Priority = "normal"
		}
	}

	return result.Tasks, nil
}

// IdentifyTaskToComplete checks if a message expresses completion intent and matches active tasks.
func (c *Client) IdentifyTaskToComplete(ctx context.Context, message string, activeTasks []ActiveTask) (*CompletionResponse, error) {
	tasksJSON, _ := json.Marshal(activeTasks)

	prompt := fmt.Sprintf(`You are a task completion detector. The user may be telling you they finished or completed a task.

ACTIVE TASKS:
%s

USER MESSAGE: "%s"

Determine if the user is saying they completed/finished/done with a task. If yes, match it to one of the active tasks above.

Return JSON:
{
  "is_completion_intent": true/false,
  "matches": [{"task_id": 123, "title": "matched task title", "confidence": "high|medium|low"}],
  "clarification": "optional message if ambiguous"
}

RULES:
- Only set is_completion_intent to true if the message clearly indicates something was done/finished/completed
- Match by semantic similarity, not exact string match ("bought milk" matches "Buy milk")
- If multiple tasks could match, return all with appropriate confidence
- If the message is clearly about creating NEW tasks (not completing existing ones), set is_completion_intent to false
- "done with X", "finished X", "completed X", "X is done", "did X" are completion patterns
- "buy X", "do X tomorrow", "remind me to X" are NOT completion patterns`, string(tasksJSON), message)

	raw, err := c.callGemini(ctx, prompt, message)
	if err != nil {
		return nil, err
	}

	var result CompletionResponse
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("failed to parse completion response: %w (raw: %s)", err, raw)
	}
	return &result, nil
}

// ParseEditIntent checks if a message expresses intent to edit an existing task.
func (c *Client) ParseEditIntent(ctx context.Context, message string, activeTasks []ActiveTask, timezone string) (*EditIntent, error) {
	loc, _ := time.LoadLocation(timezone)
	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	weekday := now.Weekday().String()

	tasksJSON, _ := json.Marshal(activeTasks)

	prompt := fmt.Sprintf(`You are a task edit detector. Today is %s (%s), timezone %s.

ACTIVE TASKS:
%s

USER MESSAGE: "%s"

Determine if the user wants to EDIT/UPDATE an existing task (change due date, priority, title, etc).

Return JSON:
{
  "is_edit_intent": true/false,
  "task_id": 123,
  "task_title": "matched task title",
  "updates": {"due_date": "YYYY-MM-DD", "priority": "high", "title": "new title"},
  "confidence": "high|medium|low",
  "clarification": "optional if ambiguous"
}

RULES:
- Only include fields in "updates" that the user wants to change
- Resolve relative dates ("tomorrow", "next Monday") to YYYY-MM-DD
- "change X to tomorrow", "move X to friday", "postpone X", "make X high priority" are edit patterns
- "reschedule X to next week" = edit due_date
- If the user says "buy milk" without referencing an existing task, that's NOT an edit
- Match tasks by semantic similarity`, today, weekday, timezone, string(tasksJSON), message)

	raw, err := c.callGemini(ctx, prompt, message)
	if err != nil {
		return nil, err
	}

	var result EditIntent
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("failed to parse edit response: %w (raw: %s)", err, raw)
	}
	return &result, nil
}

// ParseBulkIntent checks if a message expresses a bulk operation.
func (c *Client) ParseBulkIntent(ctx context.Context, message, timezone string) (*BulkIntent, error) {
	loc, _ := time.LoadLocation(timezone)
	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	weekday := now.Weekday().String()

	prompt := fmt.Sprintf(`You are a bulk task operation detector. Today is %s (%s), timezone %s.

USER MESSAGE: "%s"

Determine if the user wants to perform a BULK operation on multiple tasks.

Return JSON:
{
  "is_bulk_intent": true/false,
  "action": "complete|snooze",
  "filter": "category|overdue|all",
  "category": "personal|office|shopping|others",
  "new_date": "YYYY-MM-DD"
}

RULES:
- "snooze all shopping tasks to Monday" → action=snooze, filter=category, category=shopping, new_date=resolved
- "complete all overdue tasks" → action=complete, filter=overdue
- "mark everything done" → action=complete, filter=all
- Only set is_bulk_intent=true if the message clearly references MULTIPLE tasks or ALL tasks
- Single task operations are NOT bulk
- Resolve relative dates to YYYY-MM-DD`, today, weekday, timezone, message)

	raw, err := c.callGemini(ctx, prompt, message)
	if err != nil {
		return nil, err
	}

	var result BulkIntent
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("failed to parse bulk response: %w (raw: %s)", err, raw)
	}
	return &result, nil
}

// callGemini sends a prompt to Gemini and returns the cleaned JSON string.
func (c *Client) callGemini(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	model := c.client.GenerativeModel("gemini-2.5-flash")
	model.ResponseMIMEType = "application/json"
	model.SystemInstruction = &genai.Content{
		Parts: []genai.Part{genai.Text(systemPrompt)},
	}

	resp, err := model.GenerateContent(ctx, genai.Text(userMessage))
	if err != nil {
		return "", fmt.Errorf("gemini request failed: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini returned empty response")
	}

	var content string
	for _, part := range resp.Candidates[0].Content.Parts {
		if text, ok := part.(genai.Text); ok {
			content += string(text)
		}
	}

	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```json") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimSuffix(content, "```")
	} else if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(content, "```")
	}
	return strings.TrimSpace(content), nil
}

func buildSystemPrompt(today, weekday, timezone string) string {
	return fmt.Sprintf(`You are a task extraction assistant. Today's date is %s (%s).
The user's timezone is %s.

Extract ALL tasks from the user's message and return ONLY a valid JSON object.
No preamble, no explanation, no markdown — raw JSON only.

CATEGORY INFERENCE (never ask the user — always decide yourself):
You must infer the category from context, vocabulary, and intent alone.
The user will NEVER label or prefix tasks — it's your job to figure it out.

- "office": mentions of meetings, standups, reports, PRs, code reviews, deployments,
  clients, invoices, presentations, colleagues in a professional context, work deadlines,
  anything that sounds like it belongs in a workplace
- "shopping": any "buy", "get", "order", "pick up", "grab", item names with quantities,
  grocery items, household supplies, anything being purchased
- "personal": health appointments, family, friends, hobbies, self-care, personal calls,
  birthdays, travel plans, anything clearly about the user's personal life
- "others": genuinely ambiguous tasks that don't fit any above — use sparingly

USE SURROUNDING CONTEXT: If a message has multiple tasks, use the overall theme to help
classify ambiguous ones. "call raj" in a message full of work tasks → office.
"call raj for his birthday" → personal. Read the whole message before classifying.

When genuinely unsure, prefer "personal" over "others".

DATE RULES:
- Resolve all relative dates ("tomorrow", "next Friday", "in 3 days") to YYYY-MM-DD
- If no date mentioned, omit due_date entirely
- "EOD" / "end of day" = same day, no specific time needed
- If the user specifies a time (e.g., "3pm", "14:00"), extract it into due_time in HH:MM format (24h clock). Otherwise omit due_time.

OUTPUT FORMAT:
{
  "tasks": [
    {
      "title": "Short actionable title (max 60 chars)",
      "notes": "Any extra context, quantity, details, or full original phrasing",
      "due_date": "YYYY-MM-DD",
      "due_time": "HH:MM",
      "recurrence": "daily|weekly|monthly|yearly",
      "category": "personal|office|shopping|others",
      "priority": "low|normal|high",
      "subtasks": ["subtask 1", "subtask 2"]
    }
  ]
}

RULES:
- Shopping lists: split each item into its own task object UNLESS it's explicitly phrased as a single parent task with steps (e.g., "Plan trip: 1. book flight 2. reserve hotel" -> parent "Plan trip", subtasks "book flight", "reserve hotel")
- Extract multiple tasks from a single message
- title should be action-oriented: "Call dentist" not "dentist"
- If priority unclear, default to "normal"
- If the task is explicitly repeating (e.g. "every tuesday", "monthly"), set recurrence to "daily", "weekly", "monthly", or "yearly". Otherwise omit it.
- High priority keywords: urgent, ASAP, important, critical, must`, today, weekday, timezone)
}

func normalizeCategory(cat string) string {
	cat = strings.ToLower(strings.TrimSpace(cat))
	switch cat {
	case "personal", "office", "shopping", "others":
		return cat
	case "work":
		return "office"
	case "shop", "grocery", "groceries":
		return "shopping"
	default:
		return "others"
	}
}
