package utils

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/smtp"
	"os"
	"strings"
	"time"
)

const resendAPIURL = "https://api.resend.com/emails"
const sendGridAPIURL = "https://api.sendgrid.com/v3/mail/send"

type resendEmailPayload struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Html    string   `json:"html,omitempty"`
	Text    string   `json:"text,omitempty"`
}

type sendGridEmailPayload struct {
	Personalizations []sendGridPersonalization `json:"personalizations"`
	From             sendGridAddress           `json:"from"`
	Subject          string                    `json:"subject"`
	Content          []sendGridContent         `json:"content"`
}

type sendGridPersonalization struct {
	To []sendGridAddress `json:"to"`
}

type sendGridAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type sendGridContent struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

func emailFromName() string {
	name := strings.TrimSpace(os.Getenv("SENDGRID_FROM_NAME"))
	if name == "" {
		name = strings.TrimSpace(os.Getenv("RESEND_FROM_NAME"))
	}
	if name == "" {
		name = strings.TrimSpace(os.Getenv("SMTP_FROM_NAME"))
	}
	if name == "" {
		name = "Hotel"
	}
	return name
}

func defaultFromEmail() string {
	value := strings.TrimSpace(os.Getenv("SENDGRID_FROM_EMAIL"))
	if value == "" {
		value = strings.TrimSpace(os.Getenv("RESEND_FROM_EMAIL"))
	}
	if value == "" {
		value = strings.TrimSpace(os.Getenv("SMTP_USERNAME"))
	}
	return value
}

func parseAllowedFromEmails() map[string]struct{} {
	raw := strings.TrimSpace(os.Getenv("SENDGRID_FROM_EMAILS"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("RESEND_FROM_EMAILS"))
	}
	allowed := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		email := strings.ToLower(strings.TrimSpace(part))
		if email != "" {
			allowed[email] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		def := strings.ToLower(strings.TrimSpace(defaultFromEmail()))
		if def != "" {
			allowed[def] = struct{}{}
		}
	}
	return allowed
}

func resolveFromEmail(requested string) (string, error) {
	value := strings.TrimSpace(requested)
	if value == "" {
		value = defaultFromEmail()
	}
	if value == "" {
		return "", errors.New("sender email is not configured")
	}
	allowed := parseAllowedFromEmails()
	if len(allowed) > 0 {
		if _, ok := allowed[strings.ToLower(value)]; !ok {
			return "", fmt.Errorf("sender email %q is not allowed", value)
		}
	}
	return value, nil
}

func sendResendEmail(to []string, subject, htmlBody, textBody, fromEmail, fromName string) error {
	sendgridKey := strings.TrimSpace(os.Getenv("SENDGRID_API_KEY"))
	if sendgridKey != "" {
		return sendSendGridEmail(to, subject, htmlBody, textBody, fromEmail, fromName, sendgridKey)
	}

	apiKey := strings.TrimSpace(os.Getenv("RESEND_API_KEY"))
	if apiKey == "" {
		return sendSMTPEmail(to, subject, htmlBody, textBody, fromEmail, fromName)
	}
	if len(to) == 0 {
		return errors.New("recipient list is empty")
	}

	fromEmail, err := resolveFromEmail(fromEmail)
	if err != nil {
		return err
	}

	fromName = strings.TrimSpace(fromName)
	if fromName == "" {
		fromName = emailFromName()
	}

	from := fromEmail
	if fromName != "" {
		from = fmt.Sprintf("%s <%s>", fromName, fromEmail)
	}

	payload := resendEmailPayload{
		From:    from,
		To:      to,
		Subject: subject,
		Html:    htmlBody,
		Text:    textBody,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to serialize email payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, resendAPIURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to build resend request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("resend request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("resend error: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return nil
}

func sendSMTPEmail(to []string, subject, htmlBody, textBody, fromEmail, fromName string) error {
	smtpHost := strings.TrimSpace(os.Getenv("SMTP_HOST"))
	smtpPort := strings.TrimSpace(os.Getenv("SMTP_PORT"))
	smtpUser := strings.TrimSpace(os.Getenv("SMTP_USERNAME"))
	smtpPass := strings.TrimSpace(os.Getenv("SMTP_PASSWORD"))

	if smtpHost == "" || smtpPort == "" || smtpUser == "" || smtpPass == "" {
		log.Printf("[MOCK EMAIL] to:%v subject:%s", to, subject)
		return nil
	}
	if len(to) == 0 {
		return errors.New("recipient list is empty")
	}

	fromEmail = strings.TrimSpace(fromEmail)
	if fromEmail == "" {
		fromEmail = smtpUser
	}
	if strings.ToLower(fromEmail) != strings.ToLower(smtpUser) {
		return fmt.Errorf("sender email %q does not match SMTP_USERNAME", fromEmail)
	}

	fromName = strings.TrimSpace(fromName)
	if fromName == "" {
		fromName = emailFromName()
	}

	fromHeader := fromEmail
	if fromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", fromName, fromEmail)
	}

	boundary := "----=_EMAIL_BOUNDARY"
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("From: %s\r\n", fromHeader))
	sb.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(to, ",")))
	sb.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", boundary))

	if textBody != "" {
		sb.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		sb.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		sb.WriteString(textBody + "\r\n")
	}

	if htmlBody != "" {
		sb.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		sb.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
		sb.WriteString(htmlBody + "\r\n")
	}

	sb.WriteString(fmt.Sprintf("--%s--\r\n", boundary))

	auth := smtp.PlainAuth("", smtpUser, smtpPass, smtpHost)
	addr := fmt.Sprintf("%s:%s", smtpHost, smtpPort)
	if err := smtp.SendMail(addr, auth, smtpUser, to, []byte(sb.String())); err != nil {
		return err
	}

	return nil
}

func sendSendGridEmail(to []string, subject, htmlBody, textBody, fromEmail, fromName, apiKey string) error {
	if len(to) == 0 {
		return errors.New("recipient list is empty")
	}

	fromEmail, err := resolveFromEmail(fromEmail)
	if err != nil {
		return err
	}

	fromName = strings.TrimSpace(fromName)
	if fromName == "" {
		fromName = emailFromName()
	}

	toList := make([]sendGridAddress, 0, len(to))
	for _, addr := range to {
		email := strings.TrimSpace(addr)
		if email != "" {
			toList = append(toList, sendGridAddress{Email: email})
		}
	}
	if len(toList) == 0 {
		return errors.New("recipient list is empty")
	}

	content := make([]sendGridContent, 0, 2)
	if textBody != "" {
		content = append(content, sendGridContent{Type: "text/plain", Value: textBody})
	}
	if htmlBody != "" {
		content = append(content, sendGridContent{Type: "text/html", Value: htmlBody})
	}
	if len(content) == 0 {
		return errors.New("email body is empty")
	}

	payload := sendGridEmailPayload{
		Personalizations: []sendGridPersonalization{{To: toList}},
		From: sendGridAddress{
			Email: fromEmail,
			Name:  fromName,
		},
		Subject: subject,
		Content: content,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to serialize sendgrid payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, sendGridAPIURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to build sendgrid request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sendgrid request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sendgrid error: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return nil
}
