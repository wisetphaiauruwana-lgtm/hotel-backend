package utils

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const resendAPIURL = "https://api.resend.com/emails"

type resendEmailPayload struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Html    string   `json:"html,omitempty"`
	Text    string   `json:"text,omitempty"`
}

func resendFromName() string {
	name := strings.TrimSpace(os.Getenv("RESEND_FROM_NAME"))
	if name == "" {
		name = strings.TrimSpace(os.Getenv("SMTP_FROM_NAME"))
	}
	if name == "" {
		name = "Hotel"
	}
	return name
}

func resendDefaultFrom() string {
	value := strings.TrimSpace(os.Getenv("RESEND_FROM_EMAIL"))
	if value == "" {
		value = strings.TrimSpace(os.Getenv("SMTP_USERNAME"))
	}
	return value
}

func parseAllowedFromEmails() map[string]struct{} {
	raw := strings.TrimSpace(os.Getenv("RESEND_FROM_EMAILS"))
	allowed := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		email := strings.ToLower(strings.TrimSpace(part))
		if email != "" {
			allowed[email] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		def := strings.ToLower(strings.TrimSpace(resendDefaultFrom()))
		if def != "" {
			allowed[def] = struct{}{}
		}
	}
	return allowed
}

func resolveFromEmail(requested string) (string, error) {
	value := strings.TrimSpace(requested)
	if value == "" {
		value = resendDefaultFrom()
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
	apiKey := strings.TrimSpace(os.Getenv("RESEND_API_KEY"))
	if apiKey == "" {
		log.Printf("[MOCK EMAIL] to:%v subject:%s", to, subject)
		return nil
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
		fromName = resendFromName()
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
