package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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

// OpenRouterRequest models the request payload for OpenRouter.
type OpenRouterRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ResponseFormat struct {
	Type string `json:"type"`
}

// OpenRouterResponse models the response payload from OpenRouter.
type OpenRouterResponse struct {
	Choices []Choice `json:"choices"`
}

type Choice struct {
	Message Message `json:"message"`
}

// Client wraps the OpenRouter HTTP client.
type Client struct {
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a new OpenRouter client configured for task parsing.
func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// ParseTasks sends a user message to OpenRouter and returns structured tasks.
func (c *Client) ParseTasks(ctx context.Context, message, timezone string) ([]Task, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	weekday := now.Weekday().String()

	systemPrompt := buildSystemPrompt(today, weekday, timezone)

	reqPayload := OpenRouterRequest{
		Model: "openrouter/free",
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: message},
		},
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}

	reqBody, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	var content string
	var lastErr error

	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(reqBody))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.apiKey)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("openrouter request failed (attempt %d): %w", attempt, err)
			time.Sleep(1 * time.Second)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("openrouter returned status %d (attempt %d): %s", resp.StatusCode, attempt, string(respBody))
			time.Sleep(1 * time.Second)
			continue
		}

		var openRouterResp OpenRouterResponse
		if err := json.NewDecoder(resp.Body).Decode(&openRouterResp); err != nil {
			resp.Body.Close()
			lastErr = fmt.Errorf("failed to decode openrouter response (attempt %d): %w", attempt, err)
			time.Sleep(1 * time.Second)
			continue
		}
		resp.Body.Close()

		if len(openRouterResp.Choices) == 0 || openRouterResp.Choices[0].Message.Content == "" {
			lastErr = fmt.Errorf("openrouter returned empty response")
			time.Sleep(1 * time.Second)
			continue
		}

		content = openRouterResp.Choices[0].Message.Content
		lastErr = nil
		break
	}

	if lastErr != nil {
		return nil, lastErr
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
