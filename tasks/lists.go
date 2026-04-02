package tasks

import "taskwhisperer/config"

// ListMapping maps task categories to Google Tasks list IDs.
type ListMapping struct {
	lists map[string]string
}

// NewListMapping creates a new ListMapping from the application config.
func NewListMapping(cfg *config.Config) *ListMapping {
	return &ListMapping{
		lists: map[string]string{
			"personal": cfg.GTListPersonal,
			"office":   cfg.GTListOffice,
			"shopping": cfg.GTListShopping,
			"others":   cfg.GTListOthers,
		},
	}
}

// AllCategories returns all supported category keys.
func (m *ListMapping) AllCategories() []string {
	return []string{"personal", "office", "shopping", "others"}
}

// GetListID returns the Google Tasks list ID for a given category.
// Falls back to "others" if the category is unknown.
func (m *ListMapping) GetListID(category string) string {
	if id, ok := m.lists[category]; ok {
		return id
	}
	return m.lists["others"]
}

// CategoryName returns a human-friendly label for a category.
func CategoryName(category string) string {
	names := map[string]string{
		"personal": "Personal",
		"office":   "Work",
		"shopping": "Shopping",
		"others":   "Miscellaneous",
	}
	if name, ok := names[category]; ok {
		return name
	}
	return "Miscellaneous"
}
