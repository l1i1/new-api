package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/require"
)

func TestRenderBrandedEmailZhCN(t *testing.T) {
	require.NoError(t, i18n.Init())
	oldName := common.SystemName
	common.SystemName = "Tokeness"
	defer func() { common.SystemName = oldName }()

	out := RenderBrandedEmail(i18n.LangZhCN, "电子邮箱验证码", "<p>您好</p>\n第二行")
	require.Contains(t, out, `lang="zh-CN"`)
	require.Contains(t, out, "Tokeness")
	require.Contains(t, out, "电子邮箱验证码")
	require.Contains(t, out, "此致")
	require.Contains(t, out, "Tokeness 团队")
	require.Contains(t, out, "此为系统邮件，请勿回复。")
	require.Contains(t, out, "Copyright ©")
	// The frame must carry the web brand accent (signal red #d7192a) so emails
	// match the console/Home theme instead of a hardcoded blue.
	require.Contains(t, out, "#d7192a")
	require.NotContains(t, out, "#1f6feb")
	// All outbound mail is text/html, so plaintext newlines must become <br>.
	require.Contains(t, out, "<p>您好</p><br>第二行")
}

func TestRenderBrandedEmailEn(t *testing.T) {
	require.NoError(t, i18n.Init())
	oldName := common.SystemName
	common.SystemName = "Tokeness"
	defer func() { common.SystemName = oldName }()

	out := RenderBrandedEmail(i18n.LangEn, "Email Verification Code", "<p>Hello</p>")
	require.Contains(t, out, "Best regards,")
	require.Contains(t, out, "The Tokeness Team")
	require.Contains(t, out, "This is an automated message. Please do not reply.")
}

// The frame interpolates the title and site name into raw HTML, so both must
// be escaped to keep a hostile SystemName option value from injecting markup.
func TestRenderBrandedEmailEscapesTitleAndName(t *testing.T) {
	require.NoError(t, i18n.Init())
	oldName := common.SystemName
	common.SystemName = `<img src=x onerror=alert(1)>`
	defer func() { common.SystemName = oldName }()

	out := RenderBrandedEmail(i18n.LangEn, `<script>alert(1)</script>`, "<p>body</p>")
	require.NotContains(t, out, "<script>alert(1)</script>")
	require.Contains(t, out, "&lt;script&gt;alert(1)&lt;/script&gt;")
	require.NotContains(t, out, `<img src=x onerror=alert(1)>`)
}

func TestRenderBrandedEmailEscapesDynamicBodyValues(t *testing.T) {
	require.NoError(t, i18n.Init())
	data := map[string]any{
		"SystemName": `<img src=x onerror=alert(1)>`,
		"Link":       `javascript:alert(1)`,
		"Minutes":    30,
	}
	// Exercise the ordinary text renderer first; HTML rendering must not reuse
	// its cached parser result for the same localized message.
	require.Contains(t, i18n.Translate(i18n.LangEn, i18n.MsgEmailPasswordResetBody, data), `<img src=x onerror=alert(1)>`)
	body := i18n.TranslateHTML(i18n.LangEn, i18n.MsgEmailPasswordResetBody, data)

	out := RenderBrandedEmail(i18n.LangEn, "Password Reset", body)
	require.Contains(t, out, "&lt;img src=x onerror=alert(1)&gt;")
	require.NotContains(t, out, `<img src=x onerror=alert(1)>`)
	require.NotContains(t, out, `href='javascript:alert(1)'`)
}

func TestRenderBrandedEmailRejectsUnsafeFooterURL(t *testing.T) {
	require.NoError(t, i18n.Init())
	oldServerAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "javascript:alert(1)"
	defer func() { system_setting.ServerAddress = oldServerAddress }()

	out := RenderBrandedEmail(i18n.LangEn, "Subject", "<p>Body</p>")
	require.NotContains(t, out, `href="javascript:alert(1)"`)
	require.NotContains(t, out, "javascript:alert(1)")
}

// TestRenderBrandedEmailPreview writes sample framed emails for every
// supported language into the directory named by EMAIL_PREVIEW_DIR so the
// templates can be reviewed visually in a browser. No-op unless set.
func TestRenderBrandedEmailPreview(t *testing.T) {
	dir := os.Getenv("EMAIL_PREVIEW_DIR")
	if dir == "" {
		t.Skip("EMAIL_PREVIEW_DIR not set")
	}
	require.NoError(t, i18n.Init())
	oldName := common.SystemName
	common.SystemName = "Tokeness"
	defer func() { common.SystemName = oldName }()
	oldServerAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://tokeness.io"
	defer func() { system_setting.ServerAddress = oldServerAddress }()

	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, lang := range []string{i18n.LangZhCN, i18n.LangZhTW, i18n.LangEn, i18n.LangFr, i18n.LangRu, i18n.LangJa, i18n.LangVi} {
		verificationBody := i18n.TranslateHTML(lang, i18n.MsgEmailVerificationBody, map[string]any{
			"SystemName": common.SystemName,
			"Code":       "933202",
			"Minutes":    30,
		})
		writeEmailPreview(t, dir, "verification-"+lang+".html",
			RenderBrandedEmail(lang, i18n.Translate(lang, i18n.MsgEmailVerificationTitle), verificationBody))

		resetBody := i18n.TranslateHTML(lang, i18n.MsgEmailPasswordResetBody, map[string]any{
			"SystemName": common.SystemName,
			"Link":       "https://tokeness.io/user/reset?email=demo%40tokeness.io&token=abc123def456",
			"Minutes":    30,
		})
		writeEmailPreview(t, dir, "password-reset-"+lang+".html",
			RenderBrandedEmail(lang, i18n.Translate(lang, i18n.MsgEmailPasswordResetTitle), resetBody))

		quotaBody := i18n.TranslateHTML(lang, i18n.MsgNotifyQuotaExceedBody, map[string]any{
			"Prompt":    i18n.Translate(lang, i18n.MsgNotifyQuotaExceedSubject),
			"Quota":     "$1.23",
			"TopUpLink": "https://tokeness.io/console/topup",
		})
		writeEmailPreview(t, dir, "quota-"+lang+".html",
			RenderBrandedEmail(lang, i18n.Translate(lang, i18n.MsgNotifyQuotaExceedSubject), quotaBody))
	}
}

func writeEmailPreview(t *testing.T, dir string, name string, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}
