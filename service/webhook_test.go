package service

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
)

func TestRenderWebhookNotifyPreservesLegacyFormatting(t *testing.T) {
	title, content := renderWebhookNotify(dto.Notify{
		Title:   "Quota warning",
		Content: "used %s",
		Values:  []interface{}{"42"},
	})

	require.Equal(t, "Quota warning", title)
	require.Equal(t, "used 42", content)
}

func TestRenderWebhookNotifyUsesTemplateData(t *testing.T) {
	title, content := renderWebhookNotify(dto.Notify{
		Title:        "{{.title}}",
		Content:      "{{.name}} used {{.quota}}",
		Values:       []interface{}{"legacy"},
		TemplateData: map[string]any{"title": "Quota warning", "name": "Alice", "quota": 42},
	})

	require.Equal(t, "Quota warning", title)
	require.Equal(t, "Alice used 42", content)
}
