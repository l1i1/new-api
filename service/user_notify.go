package service

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"net/http"
	"net/url"
	"strings"
	texttemplate "text/template"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

func NotifyRootUser(t string, subject string, content string) {
	NotifyRootUserWithData(t, subject, content, nil)
}

// NotifyRootUserWithData keeps notification template data until the selected
// delivery backend renders it. This is required for email's contextual HTML
// escaping and also keeps webhook/Bark/Gotify text rendering consistent.
func NotifyRootUserWithData(t string, subject string, content string, templateData map[string]any) {
	user := model.GetRootUser().ToBaseUser()
	var notification dto.Notify
	if templateData == nil {
		notification = dto.NewNotify(t, subject, content, nil)
	} else {
		notification = dto.NewNotifyWithData(t, subject, content, templateData)
	}
	err := NotifyUser(user.Id, user.Email, user.GetSetting(), notification)
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to notify root user: %s", err.Error()))
	}
}

// NotifyUpstreamModelUpdateWatchers preserves the original fixed-content API.
func NotifyUpstreamModelUpdateWatchers(title string, content string) {
	NotifyUpstreamModelUpdateWatchersLocalized(func(string) (string, string) {
		return title, content
	})
}

// NotifyUpstreamModelUpdateWatchersLocalized invokes the renderer once per
// watcher so each recipient receives content in their preferred language.
func NotifyUpstreamModelUpdateWatchersLocalized(render func(lang string) (title string, content string)) {
	var users []model.User
	if err := model.DB.
		Select("id", "email", "role", "status", "setting").
		Where("status = ? AND role >= ?", common.UserStatusEnabled, common.RoleAdminUser).
		Find(&users).Error; err != nil {
		common.SysLog(fmt.Sprintf("failed to query upstream update notification users: %s", err.Error()))
		return
	}

	sentCount := 0
	for _, user := range users {
		userSetting := user.GetSetting()
		if !userSetting.UpstreamModelUpdateNotifyEnabled {
			continue
		}
		title, content := render(i18n.ResolveUserLang(user.Id))
		// Upstream summaries are assembled from plain text fragments. Keep the
		// whole summary as one template value so email escapes it as text while
		// webhook/Bark/Gotify preserve the original plain-text representation.
		notification := dto.NewNotifyWithData(dto.NotifyTypeChannelUpdate, title, "{{.Content}}", map[string]any{
			"Content": content,
		})
		if err := NotifyUser(user.Id, user.Email, userSetting, notification); err != nil {
			common.SysLog(fmt.Sprintf("failed to notify user %d for upstream model update: %s", user.Id, err.Error()))
			continue
		}
		sentCount++
	}
	common.SysLog(fmt.Sprintf("upstream model update notifications sent: %d", sentCount))
}

func NotifyUser(userId int, userEmail string, userSetting dto.UserSetting, data dto.Notify) error {
	notifyType := userSetting.NotifyType
	if notifyType == "" {
		notifyType = dto.NotifyTypeEmail
	}

	// Check notification limit
	canSend, err := CheckNotificationLimit(userId, data.Type)
	if err != nil {
		common.SysLog(fmt.Sprintf("failed to check notification limit: %s", err.Error()))
		return err
	}
	if !canSend {
		return fmt.Errorf("notification limit exceeded for user %d with type %s", userId, notifyType)
	}

	switch notifyType {
	case dto.NotifyTypeEmail:
		// 优先使用设置中的通知邮箱，如果为空则使用用户的默认邮箱
		emailToUse := userSetting.NotificationEmail
		if emailToUse == "" {
			emailToUse = userEmail
		}
		if emailToUse == "" {
			common.SysLog(fmt.Sprintf("user %d has no email, skip sending email", userId))
			return nil
		}
		return sendEmailNotify(emailToUse, data, userId)
	case dto.NotifyTypeWebhook:
		webhookURLStr := userSetting.WebhookUrl
		if webhookURLStr == "" {
			common.SysLog(fmt.Sprintf("user %d has no webhook url, skip sending webhook", userId))
			return nil
		}

		// 获取 webhook secret
		webhookSecret := userSetting.WebhookSecret
		return SendWebhookNotify(webhookURLStr, webhookSecret, data)
	case dto.NotifyTypeBark:
		barkURL := userSetting.BarkUrl
		if barkURL == "" {
			common.SysLog(fmt.Sprintf("user %d has no bark url, skip sending bark", userId))
			return nil
		}
		return sendBarkNotify(barkURL, data)
	case dto.NotifyTypeGotify:
		gotifyUrl := userSetting.GotifyUrl
		gotifyToken := userSetting.GotifyToken
		if gotifyUrl == "" || gotifyToken == "" {
			common.SysLog(fmt.Sprintf("user %d has no gotify url or token, skip sending gotify", userId))
			return nil
		}
		return sendGotifyNotify(gotifyUrl, gotifyToken, userSetting.GotifyPriority, data)
	}
	return nil
}

func sendEmailNotify(userEmail string, data dto.Notify, userId int) error {
	title, _ := renderNotify(data)
	content := renderNotifyHTML(data)
	lang := i18n.ResolveUserLang(userId)
	return common.SendEmail(title, userEmail, RenderBrandedEmail(lang, title, content))
}

func sendBarkNotify(barkURL string, data dto.Notify) error {
	title, content := renderNotify(data)

	// 替换模板变量
	finalURL := strings.ReplaceAll(barkURL, "{{title}}", url.QueryEscape(title))
	finalURL = strings.ReplaceAll(finalURL, "{{content}}", url.QueryEscape(content))

	// 发送GET请求到Bark
	var req *http.Request
	var resp *http.Response
	var err error

	if system_setting.EnableWorker() {
		// 使用worker发送请求
		workerReq := &WorkerRequest{
			URL:    finalURL,
			Key:    system_setting.WorkerValidKey,
			Method: http.MethodGet,
			Headers: map[string]string{
				"User-Agent": "OneAPI-Bark-Notify/1.0",
			},
		}

		resp, err = DoWorkerRequest(workerReq)
		if err != nil {
			return fmt.Errorf("failed to send bark request through worker: %v", err)
		}
		defer resp.Body.Close()

		// 检查响应状态
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("bark request failed with status code: %d", resp.StatusCode)
		}
	} else {
		// SSRF防护：验证Bark URL（非Worker模式）
		if err := ValidateSSRFProtectedFetchURL(finalURL); err != nil {
			return fmt.Errorf("request reject: %v", err)
		}

		// 直接发送请求
		req, err = http.NewRequest(http.MethodGet, finalURL, nil)
		if err != nil {
			return fmt.Errorf("failed to create bark request: %v", err)
		}

		// 设置User-Agent
		req.Header.Set("User-Agent", "OneAPI-Bark-Notify/1.0")

		// 发送请求
		client := GetSSRFProtectedHTTPClient()
		resp, err = client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to send bark request: %v", err)
		}
		defer resp.Body.Close()

		// 检查响应状态
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("bark request failed with status code: %d", resp.StatusCode)
		}
	}

	return nil
}

func sendGotifyNotify(gotifyUrl string, gotifyToken string, priority int, data dto.Notify) error {
	title, content := renderNotify(data)
	// 构建完整的 Gotify API URL
	// 确保 URL 以 /message 结尾
	finalURL := strings.TrimSuffix(gotifyUrl, "/") + "/message?token=" + url.QueryEscape(gotifyToken)

	// Gotify优先级范围0-10，如果超出范围则使用默认值5
	if priority < 0 || priority > 10 {
		priority = 5
	}

	// 构建 JSON payload
	type GotifyMessage struct {
		Title    string `json:"title"`
		Message  string `json:"message"`
		Priority int    `json:"priority"`
	}

	payload := GotifyMessage{
		Title:    title,
		Message:  content,
		Priority: priority,
	}

	// 序列化为 JSON
	payloadBytes, err := common.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal gotify payload: %v", err)
	}

	var req *http.Request
	var resp *http.Response

	if system_setting.EnableWorker() {
		// 使用worker发送请求
		workerReq := &WorkerRequest{
			URL:    finalURL,
			Key:    system_setting.WorkerValidKey,
			Method: http.MethodPost,
			Headers: map[string]string{
				"Content-Type": "application/json; charset=utf-8",
				"User-Agent":   "OneAPI-Gotify-Notify/1.0",
			},
			Body: payloadBytes,
		}

		resp, err = DoWorkerRequest(workerReq)
		if err != nil {
			return fmt.Errorf("failed to send gotify request through worker: %v", err)
		}
		defer resp.Body.Close()

		// 检查响应状态
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("gotify request failed with status code: %d", resp.StatusCode)
		}
	} else {
		// SSRF防护：验证Gotify URL（非Worker模式）
		if err := ValidateSSRFProtectedFetchURL(finalURL); err != nil {
			return fmt.Errorf("request reject: %v", err)
		}

		// 直接发送请求
		req, err = http.NewRequest(http.MethodPost, finalURL, bytes.NewBuffer(payloadBytes))
		if err != nil {
			return fmt.Errorf("failed to create gotify request: %v", err)
		}

		// 设置请求头
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		req.Header.Set("User-Agent", "NewAPI-Gotify-Notify/1.0")

		// 发送请求
		client := GetSSRFProtectedHTTPClient()
		resp, err = client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to send gotify request: %v", err)
		}
		defer resp.Body.Close()

		// 检查响应状态
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("gotify request failed with status code: %d", resp.StatusCode)
		}
	}

	return nil
}

// renderNotify renders a notification's title/content for delivery. When the
// notify carries TemplateData, {{.field}} placeholders are expanded with
// text/template; otherwise the legacy sequential {{value}} replacement is kept
// for compatibility with existing callers.
func renderNotify(data dto.Notify) (string, string) {
	title := data.Title
	content := data.Content
	if data.TemplateData == nil {
		for _, value := range data.Values {
			content = strings.Replace(content, dto.ContentValueParam, fmt.Sprintf("%v", value), 1)
		}
		return title, content
	}
	return renderNotifyTemplate(title, data.TemplateData), renderNotifyTemplate(content, data.TemplateData)
}

// renderNotifyHTML preserves trusted markup in a notification template while
// applying html/template's context-aware escaping to every dynamic value.
func renderNotifyHTML(data dto.Notify) htmltemplate.HTML {
	content := data.Content
	if data.TemplateData == nil {
		if len(data.Values) == 0 {
			return htmltemplate.HTML(content)
		}
		values := make(map[string]any, len(data.Values))
		for i, value := range data.Values {
			field := fmt.Sprintf("Value%d", i)
			values[field] = value
			content = strings.Replace(content, dto.ContentValueParam, "[[."+field+"]]", 1)
		}
		return renderNotifyHTMLTemplateWithDelims(content, values, "[[", "]]")
	}
	return renderNotifyHTMLTemplate(content, data.TemplateData)
}

func renderNotifyHTMLTemplate(src string, data map[string]any) htmltemplate.HTML {
	return renderNotifyHTMLTemplateWithDelims(src, data, "{{", "}}")
}

func renderNotifyHTMLTemplateWithDelims(src string, data map[string]any, leftDelim string, rightDelim string) htmltemplate.HTML {
	tpl, err := htmltemplate.New("notify").
		Delims(leftDelim, rightDelim).
		Option("missingkey=default").
		Parse(src)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to parse HTML notify template: %s", err.Error()))
		return htmltemplate.HTML(htmltemplate.HTMLEscapeString(src))
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		common.SysError(fmt.Sprintf("failed to render HTML notify template: %s", err.Error()))
		return htmltemplate.HTML(htmltemplate.HTMLEscapeString(src))
	}
	return htmltemplate.HTML(buf.String())
}

func renderNotifyTemplate(src string, data map[string]any) string {
	tpl, err := texttemplate.New("notify").Option("missingkey=default").Parse(src)
	if err != nil {
		common.SysError(fmt.Sprintf("failed to parse notify template: %s", err.Error()))
		return src
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		common.SysError(fmt.Sprintf("failed to render notify template: %s", err.Error()))
		return src
	}
	return buf.String()
}
