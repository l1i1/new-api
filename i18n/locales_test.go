package i18n

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

var keyPattern = regexp.MustCompile(`(?m)^([a-z0-9_]+(?:\.[a-z0-9_]+)+):`)

func localeKeys(t *testing.T, file string) map[string]bool {
	t.Helper()
	data, err := localeFS.ReadFile(file)
	require.NoError(t, err)
	set := map[string]bool{}
	for _, m := range keyPattern.FindAllStringSubmatch(string(data), -1) {
		set[m[1]] = true
	}
	return set
}

func keyDiff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	return out
}

// TestLocaleKeyParity keeps every locale's key set identical to the English
// source, so a message never silently falls back to a raw key name for a
// supported language. Run it after adding keys or a new locale.
func TestLocaleKeyParity(t *testing.T) {
	require.NoError(t, Init())
	base := localeKeys(t, "locales/en.yaml")
	other := []string{"locales/zh-CN.yaml", "locales/zh-TW.yaml",
		"locales/fr.yaml", "locales/ru.yaml", "locales/ja.yaml", "locales/vi.yaml"}
	for _, file := range other {
		set := localeKeys(t, file)
		require.Empty(t, keyDiff(base, set), "%s is missing keys", file)
		require.Empty(t, keyDiff(set, base), "%s has unexpected keys", file)
	}
}

func TestNormalizeLang(t *testing.T) {
	cases := map[string]string{
		"fr": LangFr, "fr-FR": LangFr, "FR": LangFr,
		"ru": LangRu, "ru-RU": LangRu,
		"ja": LangJa, "ja-JP": LangJa,
		"vi": LangVi, "vi-VN": LangVi,
		"en": LangEn, "en-US": LangEn,
		"zh": LangZhCN, "zh-CN": LangZhCN, "zhtw": LangZhTW, "zh-TW": LangZhTW, "ZH": LangZhCN,
		"de": DefaultLang, "": DefaultLang,
	}
	for in, want := range cases {
		require.Equalf(t, want, normalizeLang(in), "normalizeLang(%q)", in)
	}
}

func TestTranslateTemplatePreservesActions(t *testing.T) {
	require.NoError(t, Init())
	template := TranslateTemplate(LangEn, MsgNotifyChannelDisabledBody)
	require.Contains(t, template, "{{.ChannelName}}")
	require.Contains(t, template, "{{.Reason}}")
	require.NotContains(t, template, "<no value>")
}

func TestSubscriptionQuotaSubjectIsDistinct(t *testing.T) {
	require.NoError(t, Init())
	for _, lang := range SupportedLanguages() {
		walletSubject := Translate(lang, MsgNotifyQuotaExceedSubject)
		subscriptionSubject := Translate(lang, MsgNotifySubscriptionQuotaExceedSubject)
		require.NotEqualf(t, walletSubject, subscriptionSubject, "subscription subject must be distinct for %s", lang)
	}
}
