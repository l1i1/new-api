package service

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
)

func TestRenderNotifyHTMLEscapesTemplateDataByContext(t *testing.T) {
	data := dto.NewNotifyWithData(
		dto.NotifyTypeChannelUpdate,
		"subject",
		`<p>Hello {{.Name}}</p><a href='{{.Link}}'>{{.Link}}</a>`,
		map[string]any{
			"Name": `<img src=x onerror=alert(1)>`,
			"Link": `javascript:alert(1)`,
		},
	)

	out := renderNotifyHTML(data)
	require.Contains(t, out, `<p>Hello &lt;img src=x onerror=alert(1)&gt;</p>`)
	require.Contains(t, out, `<a href='#ZgotmplZ'>javascript:alert(1)</a>`)
	require.NotContains(t, out, `<img src=x onerror=alert(1)>`)
	require.NotContains(t, out, `href='javascript:alert(1)'`)
}

func TestRenderNotifyHTMLEscapesLegacyValues(t *testing.T) {
	data := dto.NewNotify(
		dto.NotifyTypeChannelUpdate,
		"subject",
		`<p>{{value}}</p>`,
		[]interface{}{`<img src=x onerror=alert(1)>`},
	)

	out := renderNotifyHTML(data)
	require.Contains(t, out, `&lt;img src=x onerror=alert(1)&gt;`)
	require.NotContains(t, out, `<img src=x onerror=alert(1)>`)
}

func TestRenderNotifyHTMLDoesNotInterpretLegacyTemplateSyntax(t *testing.T) {
	data := dto.NewNotify(
		dto.NotifyTypeChannelUpdate,
		"subject",
		`<p>{{.Literal}} {{value}}</p>`,
		[]interface{}{"rendered"},
	)

	out := renderNotifyHTML(data)
	require.Contains(t, out, `{{.Literal}} rendered`)
}
