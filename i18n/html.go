package i18n

import (
	"bytes"
	htmltemplate "html/template"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	goi18ntemplate "github.com/nicksnyder/go-i18n/v2/i18n/template"
)

// TranslateHTML renders a localized HTML template with contextual escaping.
// Locale files own the trusted markup; html/template escapes each value based
// on where the translation places it (text, attribute, or URL context).
func TranslateHTML(lang, key string, args ...map[string]any) htmltemplate.HTML {
	loc := GetLocalizer(lang)
	config := &goi18n.LocalizeConfig{
		MessageID:      key,
		TemplateParser: htmlTemplateParser{},
	}
	if len(args) > 0 && args[0] != nil {
		config.TemplateData = args[0]
	}

	msg, err := loc.Localize(config)
	if err != nil {
		return htmltemplate.HTML(htmltemplate.HTMLEscapeString(key))
	}
	return htmltemplate.HTML(msg)
}

// TranslateTemplate returns the localized template without executing its Go
// template actions. Notification delivery backends use it to keep structured
// data separate until they can apply the correct text or HTML escaping rules.
func TranslateTemplate(lang, key string) string {
	loc := GetLocalizer(lang)
	msg, err := loc.Localize(&goi18n.LocalizeConfig{
		MessageID:      key,
		TemplateParser: goi18ntemplate.IdentityParser{},
	})
	if err != nil {
		return key
	}
	return msg
}

type htmlTemplateParser struct{}

func (htmlTemplateParser) Cacheable() bool {
	// A message template is shared by Translate (text/template) and
	// TranslateHTML. Avoid go-i18n's per-message parser cache returning a text
	// parser result for a later HTML render.
	return false
}

func (htmlTemplateParser) Parse(src, leftDelim, rightDelim string) (goi18ntemplate.ParsedTemplate, error) {
	if leftDelim == "" {
		leftDelim = "{{"
	}
	if rightDelim == "" {
		rightDelim = "}}"
	}
	tmpl, err := htmltemplate.New("message").
		Delims(leftDelim, rightDelim).
		Option("missingkey=default").
		Parse(src)
	if err != nil {
		return nil, err
	}
	return htmlParsedTemplate{template: tmpl}, nil
}

type htmlParsedTemplate struct {
	template *htmltemplate.Template
}

func (t htmlParsedTemplate) Execute(data any) (string, error) {
	var buf bytes.Buffer
	if err := t.template.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
