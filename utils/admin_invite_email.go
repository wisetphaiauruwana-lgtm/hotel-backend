package utils

import (
	"fmt"
	"log"
	"os"
	"strings"
)

// SendAdminInviteEmail sends an account setup invite email for admins.
func SendAdminInviteEmail(recipientEmail, inviteLink, name, role, fromEmail string) error {
	fromName := os.Getenv("SMTP_FROM_NAME")

	safe := func(s string) string {
		return strings.ReplaceAll(strings.TrimSpace(s), "\r\n", " ")
	}

	name = safe(name)
	role = safe(role)
	inviteLink = safe(inviteLink)

	if !(strings.HasPrefix(inviteLink, "http://") || strings.HasPrefix(inviteLink, "https://")) {
		inviteLink = "https://" + strings.TrimLeft(inviteLink, "/")
	}

	subject := "You're invited to Horizon Hotel System"

	plainBody := fmt.Sprintf(
		"Hi %s,\n\n"+
		"You have been invited to join Horizon Hotel as a %s.\n"+
		"Please set your password using the link below:\n%s\n\n"+
		"If you did not expect this invitation, you can ignore this email.\n",
		name, role, inviteLink,
	)

	htmlBody := fmt.Sprintf(`<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>Invitation</title>
<style>
body { background:#f5f7fb; font-family:Arial, Helvetica, sans-serif; color:#222; }
.container { max-width:640px; margin:20px auto; }
.card { background:#fff; border:1px solid #e6eef6; padding:24px; border-radius:8px; }
.btn { display:inline-block; padding:12px 20px; background:#0b74ff; color:#fff; text-decoration:none; border-radius:6px; margin-top:16px; }
</style>
</head>
<body>
<div class="container">
  <div class="card">
    <h2>You're invited</h2>
    <p>Hi %s,</p>
    <p>You have been invited to join Horizon Hotel as a <strong>%s</strong>.</p>
    <p>Click the button below to set your password.</p>
    <a class="btn" href="%s" target="_blank">Set up my account</a>
    <p>If you did not expect this invitation, you can ignore this email.</p>
  </div>
</div>
</body>
</html>`,
		name, role, inviteLink,
	)

	if err := sendResendEmail([]string{recipientEmail}, subject, htmlBody, plainBody, fromEmail, fromName); err != nil {
		log.Printf("Failed to send invite email to %s: %v", recipientEmail, err)
		return err
	}

	log.Printf("Invite email sent to %s", recipientEmail)
	return nil
}
