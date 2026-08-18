package service

import (
	"fmt"
	"html"
	htmltemplate "html/template"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

// emailFontStack is repeated inline because most email clients strip <style>
// and external CSS; only <style>-aware clients get the link-color rule.
const emailFontStack = "-apple-system,'Segoe UI',Helvetica,Arial,'PingFang SC','Microsoft YaHei',sans-serif"

// Email colors mirror the Tokeness web design system: a single signal red
// (#d7192a, --tokeness-red / the Home page primary) for interactive accents on
// the Home dark palette (#0b0f14 paper, #14181d card, #24303c rule, #eef1f5
// foreground, #aab3bf muted). f05260 is a lighter red-tint reserved for
// small link text so it stays readable on the dark card while keeping the
// brand hue.
const (
	emailBrandRed    = "#d7192a"
	emailLinkTint    = "#f05260"
	emailBg          = "#0b0f14"
	emailCardBg      = "#14181d"
	emailCardRule    = "#24303c"
	emailAccentText  = "#f5f7fb"
	emailBodyText    = "#eef1f5"
	emailMutedText   = "#aab3bf"
	emailCopyrightBg = "#9aa4b2"
)

// RenderBrandedEmail wraps a localized email body in the site-branded HTML
// frame: dark header with logo/site name above a brand-red accent line, a
// content card with title and signature, and a muted footer carrying the
// system notice, copyright, and site link. All presentation is inline on table
// cells for email client compatibility. Newlines in content are converted to
// <br> because every outbound message is declared text/html.
func RenderBrandedEmail(lang string, title string, contentHTML htmltemplate.HTML) string {
	escapedName := html.EscapeString(common.SystemName)
	escapedTitle := html.EscapeString(title)

	logoHTML := ""
	if strings.HasPrefix(common.Logo, "https://") || strings.HasPrefix(common.Logo, "http://") {
		logoHTML = fmt.Sprintf(`<img src="%s" alt="%s" height="28" style="display:inline-block;vertical-align:middle;height:28px;margin-right:10px;border:0;">`,
			html.EscapeString(common.Logo), escapedName)
	}

	siteURL := emailHTTPURL(system_setting.ServerAddress)
	copyright := i18n.Translate(lang, i18n.MsgEmailFrameCopyright, map[string]any{
		"Year":       time.Now().Year(),
		"SystemName": escapedName,
	})
	if siteURL != "" {
		escapedSite := html.EscapeString(siteURL)
		copyright += fmt.Sprintf(` · <a href="%s" style="color:%s;text-decoration:underline;">%s</a>`, escapedSite, emailCopyrightBg, escapedSite)
	}

	regards := i18n.Translate(lang, i18n.MsgEmailFrameRegards)
	team := i18n.Translate(lang, i18n.MsgEmailFrameTeam, map[string]any{"SystemName": escapedName})
	notice := i18n.Translate(lang, i18n.MsgEmailFrameSystemNotice)

	content := strings.ReplaceAll(string(contentHTML), "\n", "<br>")

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="%s">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="color-scheme" content="dark">
<title>%s</title>
<style>a{color:%s;}</style>
</head>
<body style="margin:0;padding:0;background-color:%s;-webkit-text-size-adjust:100%%;">
<div style="display:none;max-height:0;overflow:hidden;mso-hide:all;">%s</div>
<table role="presentation" cellpadding="0" cellspacing="0" width="100%%" style="background-color:%s;">
<tr><td align="center" style="padding:40px 16px;">
<table role="presentation" cellpadding="0" cellspacing="0" width="600" style="width:600px;max-width:100%%;">
<tr><td style="background-color:%s;border-bottom:2px solid %s;padding:22px 32px;">%s<span style="font-family:%s;font-size:19px;font-weight:700;color:%s;vertical-align:middle;">%s</span></td></tr>
<tr><td style="background-color:%s;padding:8px 32px 36px;">
<h1 style="margin:24px 0 20px;font-family:%s;font-size:21px;line-height:1.4;font-weight:700;color:%s;">%s</h1>
<div style="font-family:%s;font-size:14px;line-height:1.9;color:%s;">%s</div>
<p style="margin:36px 0 0;font-family:%s;font-size:14px;line-height:1.9;color:%s;">%s<br><strong style="color:%s;">%s</strong></p>
</td></tr>
</table>
<table role="presentation" cellpadding="0" cellspacing="0" width="600" style="width:600px;max-width:100%%;">
<tr><td align="center" style="padding:28px 32px;">
<p style="margin:0 0 10px;font-family:%s;font-size:12px;line-height:1.6;color:%s;">%s</p>
<p style="margin:0;font-family:%s;font-size:12px;line-height:1.6;color:%s;">%s</p>
</td></tr>
</table>
</td></tr>
</table>
</body>
</html>`,
		html.EscapeString(lang),
		escapedTitle,
		emailLinkTint,
		emailBg,
		escapedTitle, // preheader preview text
		emailBg,
		emailCardBg, emailBrandRed, logoHTML, emailFontStack, emailAccentText, escapedName,
		emailCardBg,
		emailFontStack, emailAccentText, escapedTitle,
		emailFontStack, emailBodyText, content,
		emailFontStack, emailMutedText, html.EscapeString(regards), emailAccentText, team,
		emailFontStack, emailMutedText, html.EscapeString(notice),
		emailFontStack, emailCopyrightBg, copyright,
	)
}

func emailHTTPURL(raw string) string {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	return value
}
