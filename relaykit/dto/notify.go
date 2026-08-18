package dto

type Notify struct {
	Type    string        `json:"type"`
	Title   string        `json:"title"`
	Content string        `json:"content"`
	Values  []interface{} `json:"values"`
	// TemplateData is an optional map used to render {{.field}} placeholders in
	// Title/Content. When set, it takes precedence over the legacy Values list.
	TemplateData map[string]any `json:"template_data,omitempty"`
}

const ContentValueParam = "{{value}}"

const (
	NotifyTypeQuotaExceed   = "quota_exceed"
	NotifyTypeChannelUpdate = "channel_update"
	NotifyTypeChannelTest   = "channel_test"
)

func NewNotify(t string, title string, content string, values []interface{}) Notify {
	return Notify{
		Type:    t,
		Title:   title,
		Content: content,
		Values:  values,
	}
}

func NewNotifyWithData(t string, title string, content string, data map[string]any) Notify {
	return Notify{
		Type:         t,
		Title:        title,
		Content:      content,
		TemplateData: data,
	}
}
