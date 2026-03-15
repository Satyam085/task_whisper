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

// Client wraps the Gemini client.
type Client struct {
	apiKey string
}

// NewClient creates a new Gemini client configured for task parsing.
func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
	}
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

	client, err := genai.NewClient(ctx, option.WithAPIKey(c.apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create gemini client: %w", err)
	}
	defer client.Close()

	model := client.GenerativeModel("gemini-2.5-flash")
	model.ResponseMIMEType = "application/json"
	
	systemPrompt := buildSystemPrompt(today, weekday, timezone)
	model.SystemInstruction = &genai.Content{
		Parts: []genai.Part{genai.Text(systemPrompt)},
	}

	resp, err := model.GenerateContent(ctx, genai.Text(message))
	if err != nil {
		return nil, fmt.Errorf("gemini request failed: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("gemini returned empty response")
	}

	var content string
	for _, part := range resp.Candidates[0].Content.Parts {
		if text, ok := part.(genai.Text); ok {
			content += string(text)
		}
	}

	// Remove markdown code blocks if the model wrapped the JSON
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```json") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimSuffix(content, "```")
	} else if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(content, "```")
	}
	content = strings.TrimSpace(content)

	var result LLMResponse
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("failed to parse llm JSON: %w (raw: %s)", err, content)
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
