package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

// Task represents a single task extracted by Gemini from the user's message.
type Task struct {
	Title    string `json:"title"`
	Notes    string `json:"notes,omitempty"`
	DueDate  string `json:"due_date,omitempty"` // YYYY-MM-DD
	Category string `json:"category"`           // personal, office, shopping, others
	Priority string `json:"priority"`           // low, normal, high
}

// GeminiResponse wraps the JSON response from Gemini.
type GeminiResponse struct {
	Tasks []Task `json:"tasks"`
}

// Client wraps the Gemini generative AI client.
type Client struct {
	client *genai.Client
	model  *genai.GenerativeModel
}

// NewClient creates a new Gemini client configured for task parsing.
func NewClient(ctx context.Context, apiKey string) (*Client, error) {
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create gemini client: %w", err)
	}

	model := client.GenerativeModel("gemini-2.5-flash")
	model.ResponseMIMEType = "application/json"

	return &Client{
		client: client,
		model:  model,
	}, nil
}

// Close releases the Gemini client resources.
func (c *Client) Close() {
	c.client.Close()
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
	c.model.SystemInstruction = genai.NewUserContent(genai.Text(systemPrompt))

	resp, err := c.model.GenerateContent(ctx, genai.Text(message))
	if err != nil {
		return nil, fmt.Errorf("gemini generate failed: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("gemini returned empty response")
	}

	text, ok := resp.Candidates[0].Content.Parts[0].(genai.Text)
	if !ok {
		return nil, fmt.Errorf("unexpected response part type")
	}

	var result GeminiResponse
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("failed to parse gemini JSON: %w (raw: %s)", err, string(text))
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

OUTPUT FORMAT:
{
  "tasks": [
    {
      "title": "Short actionable title (max 60 chars)",
      "notes": "Any extra context, quantity, details, or full original phrasing",
      "due_date": "YYYY-MM-DD",
      "category": "personal|office|shopping|others",
      "priority": "low|normal|high"
    }
  ]
}

RULES:
- Shopping lists: split each item into its own task object
- Extract multiple tasks from a single message
- title should be action-oriented: "Call dentist" not "dentist"
- If priority unclear, default to "normal"
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
