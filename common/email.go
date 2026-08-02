package common

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net/smtp"
	"slices"
	"strings"
	"time"
)

func generateMessageID() (string, error) {
	split := strings.Split(SMTPFrom, "@")
	if len(split) < 2 {
		return "", fmt.Errorf("invalid SMTP account")
	}
	domain := strings.Split(SMTPFrom, "@")[1]
	return fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), GetRandomString(12), domain), nil
}

func shouldUseSMTPLoginAuth() bool {
	if SMTPForceAuthLogin {
		return true
	}
	return isOutlookServer(SMTPAccount) || slices.Contains(EmailLoginAuthServerList, SMTPServer)
}

func getSMTPAuth() smtp.Auth {
	return AutoSMTPAuth(SMTPAccount, SMTPToken)
}

func shouldAuthenticateSMTP() bool {
	return SMTPAccount != "" && SMTPToken != ""
}

func smtpTLSConfig() *tls.Config {
	return &tls.Config{
		ServerName:         SMTPServer,
		InsecureSkipVerify: SMTPInsecureSkipVerify, // #nosec G402 -- admin-controlled SMTP compatibility option.
	}
}

func newSMTPClient(addr string) (*smtp.Client, error) {
	if SMTPSSLEnabled || (SMTPPort == 465 && !SMTPStartTLSEnabled) {
		conn, err := tls.Dial("tcp", addr, smtpTLSConfig())
		if err != nil {
			return nil, err
		}
		client, err := smtp.NewClient(conn, SMTPServer)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return client, nil
	}

	client, err := smtp.Dial(addr)
	if err != nil {
		return nil, err
	}

	if SMTPStartTLSEnabled {
		startTLSSupported, _ := client.Extension("STARTTLS")
		if !startTLSSupported {
			_ = client.Close()
			return nil, fmt.Errorf("SMTP server does not support STARTTLS")
		}
		if err := client.StartTLS(smtpTLSConfig()); err != nil {
			_ = client.Close()
			return nil, err
		}
	}

	return client, nil
}

// EmailAttachment is an in-memory MIME attachment. Callers should provide a
// safe filename and a matching content type; the message builder handles MIME
// encoding and line wrapping.
type EmailAttachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

func SendEmail(subject string, receiver string, content string) error {
	return SendEmailWithAttachments(subject, receiver, content, nil)
}

func SendEmailWithAttachments(subject string, receiver string, content string, attachments []EmailAttachment) error {
	if SMTPFrom == "" { // for compatibility
		SMTPFrom = SMTPAccount
	}
	id, err2 := generateMessageID()
	if err2 != nil {
		return err2
	}
	if SMTPServer == "" && SMTPAccount == "" {
		return fmt.Errorf("SMTP 服务器未配置")
	}
	encodedSubject := fmt.Sprintf("=?UTF-8?B?%s?=", base64.StdEncoding.EncodeToString([]byte(subject)))
	var message string
	if len(attachments) == 0 {
		message = fmt.Sprintf("To: %s\r\n"+
			"From: %s <%s>\r\n"+
			"Subject: %s\r\n"+
			"Date: %s\r\n"+
			"Message-ID: %s\r\n"+ // 添加 Message-ID 头
			"Content-Type: text/html; charset=UTF-8\r\n\r\n%s\r\n",
			receiver, SystemName, SMTPFrom, encodedSubject, time.Now().Format(time.RFC1123Z), id, content)
	} else {
		boundary := fmt.Sprintf("=_new_api_%s", strings.Trim(id, "<>"))
		var body strings.Builder
		body.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		body.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
		body.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		body.WriteString(content)
		body.WriteString("\r\n")
		for _, attachment := range attachments {
			if len(attachment.Data) == 0 {
				continue
			}
			filename := strings.ReplaceAll(attachment.Filename, `"`, "")
			if filename == "" {
				filename = "attachment.bin"
			}
			contentType := attachment.ContentType
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			body.WriteString(fmt.Sprintf("--%s\r\n", boundary))
			body.WriteString(fmt.Sprintf("Content-Type: %s; name=\"%s\"\r\n", contentType, filename))
			body.WriteString(fmt.Sprintf("Content-Disposition: attachment; filename=\"%s\"\r\n", filename))
			body.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
			body.WriteString(wrapBase64(attachment.Data))
			body.WriteString("\r\n")
		}
		body.WriteString(fmt.Sprintf("--%s--\r\n", boundary))
		message = fmt.Sprintf("To: %s\r\n"+
			"From: %s <%s>\r\n"+
			"Subject: %s\r\n"+
			"Date: %s\r\n"+
			"Message-ID: %s\r\n"+
			"MIME-Version: 1.0\r\n"+
			"Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n%s",
			receiver, SystemName, SMTPFrom, encodedSubject, time.Now().Format(time.RFC1123Z), id, boundary, body.String())
	}
	mail := []byte(message)
	auth := getSMTPAuth()
	addr := fmt.Sprintf("%s:%d", SMTPServer, SMTPPort)
	to := strings.Split(receiver, ";")
	var err error
	client, err := newSMTPClient(addr)
	if err != nil {
		return err
	}
	defer client.Close()
	if shouldAuthenticateSMTP() {
		if err = client.Auth(auth); err != nil {
			return err
		}
	}
	if err = client.Mail(SMTPFrom); err != nil {
		return err
	}
	for _, receiver := range to {
		if err = client.Rcpt(receiver); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	_, err = w.Write(mail)
	if err != nil {
		return err
	}
	err = w.Close()
	if err != nil {
		return err
	}
	err = client.Quit()
	if err != nil {
		// Log the failure without echoing the recipient address (privacy).
		SysError(fmt.Sprintf("failed to send email after quit: %v", err))
	}
	return err
}

func wrapBase64(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	var wrapped strings.Builder
	for len(encoded) > 0 {
		lineLength := 76
		if len(encoded) < lineLength {
			lineLength = len(encoded)
		}
		wrapped.WriteString(encoded[:lineLength])
		wrapped.WriteString("\r\n")
		encoded = encoded[lineLength:]
	}
	return wrapped.String()
}
