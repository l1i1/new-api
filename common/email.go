package common

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
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
	return newSMTPClientContext(context.Background(), addr)
}

func newSMTPClientContext(ctx context.Context, addr string) (*smtp.Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dialer := &net.Dialer{}
	if deadline, ok := ctx.Deadline(); ok {
		dialer.Deadline = deadline
	}
	var conn net.Conn
	var err error
	if SMTPSSLEnabled || (SMTPPort == 465 && !SMTPStartTLSEnabled) {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: smtpTLSConfig()}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stopGreetingCancellation := context.AfterFunc(ctx, func() {
		_ = conn.Close()
	})
	client, err := smtp.NewClient(conn, SMTPServer)
	stopGreetingCancellation()
	if err != nil {
		_ = conn.Close()
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

// EmailDeliveryError records whether another delivery attempt is known to be
// safe. Errors after the SMTP DATA terminator are ambiguous: the server may
// have accepted the message even if the client did not receive confirmation.
type EmailDeliveryError struct {
	Err       error
	RetrySafe bool
}

func (e *EmailDeliveryError) Error() string {
	return e.Err.Error()
}

func (e *EmailDeliveryError) Unwrap() error {
	return e.Err
}

func IsEmailDeliveryRetrySafe(err error) bool {
	var deliveryErr *EmailDeliveryError
	return errors.As(err, &deliveryErr) && deliveryErr.RetrySafe
}

func emailDeliveryError(err error, retrySafe bool) error {
	if err == nil {
		return nil
	}
	return &EmailDeliveryError{Err: err, RetrySafe: retrySafe}
}

func SendEmail(subject string, receiver string, content string) error {
	return SendEmailWithAttachments(subject, receiver, content, nil)
}

func SendEmailWithAttachments(subject string, receiver string, content string, attachments []EmailAttachment) error {
	return SendEmailWithAttachmentsContext(context.Background(), subject, receiver, content, attachments)
}

// SendEmailWithAttachmentsContext bounds SMTP connection and command I/O to
// the supplied context while preserving the legacy background-context API.
func SendEmailWithAttachmentsContext(ctx context.Context, subject string, receiver string, content string, attachments []EmailAttachment) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if SMTPFrom == "" { // for compatibility
		SMTPFrom = SMTPAccount
	}
	id, err2 := generateMessageID()
	if err2 != nil {
		return emailDeliveryError(err2, true)
	}
	if SMTPServer == "" && SMTPAccount == "" {
		return emailDeliveryError(fmt.Errorf("SMTP 服务器未配置"), true)
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
	client, err := newSMTPClientContext(ctx, addr)
	if err != nil {
		return emailDeliveryError(err, true)
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = client.Close()
	})
	defer stopCancellation()
	defer client.Close()
	if shouldAuthenticateSMTP() {
		if err = client.Auth(auth); err != nil {
			return emailDeliveryError(err, true)
		}
	}
	if err = client.Mail(SMTPFrom); err != nil {
		return emailDeliveryError(err, true)
	}
	for _, receiver := range to {
		if err = client.Rcpt(receiver); err != nil {
			return emailDeliveryError(err, true)
		}
	}
	w, err := client.Data()
	if err != nil {
		return emailDeliveryError(err, true)
	}
	_, err = w.Write(mail)
	if err != nil {
		return emailDeliveryError(err, true)
	}
	err = w.Close()
	if err != nil {
		return emailDeliveryError(err, false)
	}
	err = client.Quit()
	if err != nil {
		// Log the failure without echoing the recipient address (privacy).
		SysError(fmt.Sprintf("failed to send email after quit: %v", err))
	}
	return nil
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
