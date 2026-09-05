package router

import (
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testIndexTemplate = `<!doctype html>
<html>
  <head>
    <meta charset="UTF-8" />
    <title></title>
    <!--head-html-->
  </head>
  <body></body>
</html>
`

func withCustomHeadHTMLOption(t *testing.T, value string) {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	previous, hadPrevious := common.OptionMap["CustomHeadHTML"]
	common.OptionMap["CustomHeadHTML"] = value
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		defer common.OptionMapRWMutex.Unlock()
		if hadPrevious {
			common.OptionMap["CustomHeadHTML"] = previous
		} else {
			delete(common.OptionMap, "CustomHeadHTML")
		}
	})
}

func TestRenderIndexPageDefaultHead(t *testing.T) {
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	delete(common.OptionMap, "CustomHeadHTML")
	common.OptionMapRWMutex.Unlock()

	page := renderIndexPage([]byte(testIndexTemplate))

	assert.Contains(t, string(page), common.DefaultCustomHeadHTML)
	assert.Contains(t, string(page), "<title>Tokeness - One Entry, All Models | AI API</title>")
	assert.NotContains(t, string(page), "<!--head-html-->")
	// The build tool's empty <title> is stripped so the page keeps a single
	// title tag.
	assert.NotContains(t, string(page), "<title></title>")
	assert.Equal(t, 1, strings.Count(string(page), "<title>"))
}

func TestRenderIndexPageUsesConfiguredHead(t *testing.T) {
	withCustomHeadHTMLOption(t, "<title>Custom</title><meta name=\"theme-color\" content=\"#000\" />")

	page := renderIndexPage([]byte(testIndexTemplate))

	assert.Contains(t, string(page), "<title>Custom</title>")
	assert.Contains(t, string(page), "<meta name=\"theme-color\" content=\"#000\" />")
	assert.NotContains(t, string(page), common.DefaultCustomHeadHTML)
}

func TestRenderIndexPageWhitespaceFallsBackToDefault(t *testing.T) {
	withCustomHeadHTMLOption(t, "   \n\t ")

	page := renderIndexPage([]byte(testIndexTemplate))

	assert.Contains(t, string(page), common.DefaultCustomHeadHTML)
	assert.NotContains(t, string(page), "<!--head-html-->")
}

// The embedded index.html is built from web/index.html; this guards against
// removing the placeholder from the template, which would silently disable
// the CustomHeadHTML option.
func TestIndexTemplateKeepsHeadPlaceholder(t *testing.T) {
	raw, err := os.ReadFile("../web/index.html")
	require.NoError(t, err)

	assert.Contains(t, string(raw), "<!--head-html-->")
}

// The shipped default head must keep the protected project branding.
func TestDefaultCustomHeadHTMLKeepsBranding(t *testing.T) {
	assert.Contains(t, common.DefaultCustomHeadHTML, `<meta name="generator" content="New API" />`)
}
