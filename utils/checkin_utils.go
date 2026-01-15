package utils

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//
// ===========================================================
//  TYPES
// ===========================================================
//

// RoomInfo represents a room's number + type for emails / display
type RoomInfo struct {
	Number string // e.g. "101"
	Type   string // e.g. "Deluxe King"
}

//
// ===========================================================
//  ENV UTILITIES
// ===========================================================
//

// EnvOrDefault returns ENV value or fallback default.
func EnvOrDefault(key, def string) string {
	v := os.Getenv(key)
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

//
// ===========================================================
//  TOKEN & CODE GENERATORS
// ===========================================================
//

const checkinCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// GenerateSecureToken สร้าง token แบบ hex (length = bytes)
func GenerateSecureToken(length int) (string, error) {
	if length <= 0 {
		return "", errors.New("invalid token length")
	}
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GenerateCheckinCode (A-Z0-9) เช่น "AB4D93KF"
// เวอร์ชันนี้ใช้ crypto/rand + rand.Int (math/big) เพื่อลด modulo bias
func GenerateCheckinCode(n int) (string, error) {
	if n <= 0 {
		return "", errors.New("invalid length")
	}
	var sb strings.Builder
	alphaLen := big.NewInt(int64(len(checkinCharset)))
	for i := 0; i < n; i++ {
		num, err := rand.Int(rand.Reader, alphaLen)
		if err != nil {
			return "", err
		}
		sb.WriteByte(checkinCharset[num.Int64()])
	}
	return sb.String(), nil
}

// GenerateFormattedCheckinCode → "XXXX-XXXX"
func GenerateFormattedCheckinCode(raw string) (string, error) {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	raw = strings.ReplaceAll(raw, "-", "")
	if len(raw) != 8 {
		return "", errors.New("raw must be length 8")
	}
	return raw[:4] + "-" + raw[4:], nil
}

// PtrTime returns pointer to time.Time
func PtrTime(t time.Time) *time.Time { return &t }

//
// ===========================================================
//  CHECKIN CODE HELPERS
// ===========================================================
//

// NormalizeCheckinCode → remove hyphens/non-alnum
func NormalizeCheckinCode(code string) string {
	s := strings.ToUpper(strings.TrimSpace(code))
	re := regexp.MustCompile(`[^A-Z0-9]`)
	return re.ReplaceAllString(s, "")
}

// Validate format: "ABCDEFGH" or "ABCD-EFGH"
func IsValidCheckinCodeFormat(code string) bool {
	if code == "" {
		return false
	}
	c := strings.TrimSpace(code)
	match1, _ := regexp.MatchString(`^[A-Za-z0-9]{8}$`, c)
	match2, _ := regexp.MatchString(`^[A-Za-z0-9]{4}-[A-Za-z0-9]{4}$`, c)
	return match1 || match2
}

// Build frontend link by token
func BuildCheckinLink(frontendURL, token string, useQuery bool) string {
	if frontendURL == "" {
		frontendURL = "http://localhost:3000"
	}
	frontendURL = strings.TrimRight(frontendURL, "/")
	if useQuery {
		return fmt.Sprintf("%s/checkin?token=%s", frontendURL, token)
	}
	return fmt.Sprintf("%s/checkin/%s", frontendURL, token)
}

// AppendBookingIDParam appends bookingId query param to a check-in link.
func AppendBookingIDParam(link string, bookingID uint) string {
	if bookingID == 0 {
		return link
	}
	sep := "?"
	if strings.Contains(link, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%sbookingId=%d", link, sep, bookingID)
}

//
// ===========================================================
//  EMAIL MASKING
// ===========================================================
//

// MaskEmail returns masked email for safe display
func MaskEmail(email string) string {
	email = strings.TrimSpace(email)
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return email
	}
	local := parts[0]
	domain := parts[1]

	maskedLocal := local
	if len(local) > 2 {
		maskedLocal = local[:1] + strings.Repeat("*", len(local)-2) + local[len(local)-1:]
	} else if len(local) == 2 {
		maskedLocal = local[:1] + "*"
	}

	domainParts := strings.Split(domain, ".")
	if len(domainParts) >= 2 {
		if len(domainParts[0]) > 1 {
			domainParts[0] = domainParts[0][:1] + strings.Repeat("*", len(domainParts[0])-1)
		}
	}

	return maskedLocal + "@" + strings.Join(domainParts, ".")
}

//
// ===========================================================
//  EMAIL SENDER (CHECK-IN LINK + CONFIRM CODE)
// ===========================================================
//

// SendCheckInLinkEmail — send HTML + plain text email including confirmation code
// NOTE: changed to accept rooms []RoomInfo so email can include number+type for every room.
func SendCheckInLinkEmail(
	recipientEmail,
	bookingRef,
	checkinLink,
	guestName string,
	rooms []RoomInfo,
	checkInDate,
	checkOutDate,
	confirmationCode string,
) error {

	fromName := strings.TrimSpace(os.Getenv("SMTP_FROM_NAME"))
	if fromName == "" {
		fromName = strings.TrimSpace(os.Getenv("RESEND_FROM_NAME"))
	}
	if fromName == "" {
		fromName = "Hotel"
	}

	// sanitize strings
	safe := func(s string) string {
		return strings.ReplaceAll(strings.TrimSpace(s), "\r\n", " ")
	}

	guestName = safe(guestName)
	bookingRef = safe(bookingRef)
	checkInDate = safe(checkInDate)
	checkOutDate = safe(checkOutDate)
	checkinLink = safe(checkinLink)
	confirmationCode = safe(confirmationCode)

	// Ensure scheme
	if !(strings.HasPrefix(checkinLink, "http://") || strings.HasPrefix(checkinLink, "https://")) {
		checkinLink = "https://" + strings.TrimLeft(checkinLink, "/")
	}

	// Build rooms textual & HTML representation
	roomsText := roomsListText(rooms)    // plain text list
	roomsHTML := roomsListHTML(rooms)    // html list

	subject := fmt.Sprintf("Booking Confirmation and Pre-Check-in — %s", bookingRef)

	//
	// PLAIN TEXT
	//
	plainBody := fmt.Sprintf(
		"Dear %s,\n\n"+ 
			"Thank you for booking with us! Here are your booking details:\n\n"+
			"Booking Reference: %s\n"+
			"Confirmation Code: %s\n"+
			"Rooms:\n%s\n"+
			"Check-In: %s\n"+
			"Check-Out: %s\n\n"+
			"Complete your pre-check-in here: %s\n\n"+
			"If you have any questions, feel free to contact us.\n\n"+
			"Best regards,\n%s",
		guestName,
		bookingRef,
		confirmationCode,
		roomsText,
		checkInDate,
		checkOutDate,
		checkinLink,
		fromName,
	)

	//
	// HTML EMAIL BODY
	//
	htmlBody := fmt.Sprintf(`<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>Pre Check-in</title>
<style>
body { background:#f5f7fb; font-family:Arial, Helvetica, sans-serif; color:#222; }
.container { max-width:700px; margin:20px auto; }
.card { background:#fff; border:1px solid #e6eef6; padding:24px; border-radius:8px; }
.label { font-weight:700; width:160px; display:inline-block; vertical-align:top; }
.btn { display:inline-block; padding:12px 20px; background:#0b74ff; color:#fff;
       text-decoration:none; border-radius:6px; margin-top:18px; }
.room-list { margin:12px 0 18px 0; padding-left:18px; }
.room-item { margin:6px 0; }
</style>
</head>
<body>
<div class="container">
  <div class="card">
    <h2>Booking Confirmation & Pre-Check-in</h2>
    <p>Dear %s,</p>
    <p>Thank you for choosing our hotel. Below are your booking details:</p>

    <p><span class="label">Booking Reference:</span> %s</p>
    <p><span class="label">Confirmation Code:</span> %s</p>
    <p><span class="label">Rooms:</span> %s</p>
    <p><span class="label">Check-In:</span> %s</p>
    <p><span class="label">Check-Out:</span> %s</p>

    <a class="btn" href="%s" target="_blank">Complete Pre-Check-in</a>
    <p>If you have any questions, feel free to contact us.</p>
    <p>Best regards,<br>%s</p>
  </div>
</div>
</body>
</html>`,
		guestName,
		bookingRef,
		confirmationCode,
		roomsHTML,
		checkInDate,
		checkOutDate,
		checkinLink,
		fromName,
	)

	if err := sendResendEmail([]string{recipientEmail}, subject, htmlBody, plainBody, "", fromName); err != nil {
		log.Printf("❌ Failed to send email to %s: %v", recipientEmail, err)
		return err
	}

	log.Printf("📨 Email sent to %s (Confirmation Code: %s)", recipientEmail, confirmationCode)
	return nil
}

// helper: produce plain text list for rooms
func roomsListText(rooms []RoomInfo) string {
	if len(rooms) == 0 {
		return "N/A"
	}
	var b strings.Builder
	for _, r := range rooms {
		num := strings.TrimSpace(r.Number)
		typ := strings.TrimSpace(r.Type)
		if typ != "" {
			b.WriteString(fmt.Sprintf(" - %s (%s)\n", num, typ))
		} else {
			b.WriteString(fmt.Sprintf(" - %s\n", num))
		}
	}
	return b.String()
}

// helper: produce HTML list for rooms
func roomsListHTML(rooms []RoomInfo) string {
	if len(rooms) == 0 {
		return "<em>N/A</em>"
	}
	var b strings.Builder
	b.WriteString("<ul class=\"room-list\">")
	for _, r := range rooms {
		num := strings.TrimSpace(r.Number)
		typ := strings.TrimSpace(r.Type)
		if typ != "" {
			b.WriteString(fmt.Sprintf("<li class=\"room-item\">%s (%s)</li>", htmlEscape(num), htmlEscape(typ)))
		} else {
			b.WriteString(fmt.Sprintf("<li class=\"room-item\">%s</li>", htmlEscape(num)))
		}
	}
	b.WriteString("</ul>")
	return b.String()
}

// minimal html escaper for the small strings we use
func htmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(s)
}

//
// ===========================================================
//  MISC HELPERS
// ===========================================================
//

func emptyIfNil(s string) string {
	if strings.TrimSpace(s) == "<nil>" || strings.TrimSpace(s) == "nil" {
		return ""
	}
	return s
}

// PopulateStayData populates stay details for the booking
func PopulateStayData(bookingId int) (map[string]interface{}, error) {
	// Example logic to fetch stay data
	// Replace this with actual logic to retrieve stay data from your database or service
	stay := map[string]interface{}{
		"checkInDate":  "2023-12-01",
		"checkOutDate": "2023-12-05",
		"nights":       4,
	}
	return stay, nil
}

// PopulateRoomData populates room details for the booking
func PopulateRoomData(bookingId int) (map[string]interface{}, error) {
	// Example logic to fetch room data
	// Replace this with actual logic to retrieve room data from your database or service
	room := map[string]interface{}{
		"roomNumber": "101",
		"type":       "Deluxe",
		"bedType":    "King",
	}
	return room, nil
}

// UpdateBookingState updates the booking state with stay and room details
func UpdateBookingState(bookingId int, mainGuest, email, status string, raw map[string]interface{}) (map[string]interface{}, error) {
	// Fetch stay and room data
	stay, err := PopulateStayData(bookingId)
	if err != nil {
		return nil, fmt.Errorf("failed to populate stay data: %v", err)
	}

	room, err := PopulateRoomData(bookingId)
	if err != nil {
		return nil, fmt.Errorf("failed to populate room data: %v", err)
	}

	// Construct the booking state
	bookingState := map[string]interface{}{
		"id":        bookingId,
		"dbId":      bookingId,
		"mainGuest": mainGuest,
		"email":     email,
		"status":    status,
		"stay":      stay,
		"room":      room,
		"_raw":      raw,
	}

	return bookingState, nil
}

//
// ===========================================================
//  IMAGE (BASE64) HELPERS
// ===========================================================
//

// SaveBase64Image decodes base64 image string and writes to destDir.
// Returns the saved filepath (absolute) or error.
// base64Str may be either raw base64 payload or a data URI like "data:image/png;base64,...."
func SaveBase64Image(base64Str string, destDir string) (string, error) {
	base64Str = strings.TrimSpace(base64Str)
	if base64Str == "" {
		return "", fmt.Errorf("empty base64 string")
	}

	// If data URI, try to detect extension and strip prefix
	ext := ""
	if strings.HasPrefix(base64Str, "data:") {
		// format: data:<mime>;base64,<payload>
		parts := strings.SplitN(base64Str, ";base64,", 2)
		if len(parts) == 2 {
			meta := parts[0] // e.g. data:image/png
			base64Str = parts[1]
			if strings.HasPrefix(meta, "data:") {
				mime := strings.TrimPrefix(meta, "data:")
				switch mime {
				case "image/png":
					ext = ".png"
				case "image/jpeg", "image/jpg":
					ext = ".jpg"
				case "image/gif":
					ext = ".gif"
				default:
					// unknown mime -> leave ext empty
				}
			}
		} else {
			// if not the expected separator, try to remove "data:" prefix only
			if idx := strings.Index(base64Str, ","); idx != -1 {
				base64Str = base64Str[idx+1:]
			}
		}
	}

	// decode (try StdEncoding then URL encoding)
	data, err := base64.StdEncoding.DecodeString(base64Str)
	if err != nil {
		data, err = base64.URLEncoding.DecodeString(base64Str)
		if err != nil {
			return "", fmt.Errorf("base64 decode failed: %v", err)
		}
	}

	// ensure dest dir exists
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir failed: %v", err)
	}

	// create filename with timestamp + random hex
	randBytes := make([]byte, 6)
	if _, err := rand.Read(randBytes); err != nil {
		// fallback to timestamp only
		randBytes = []byte(fmt.Sprintf("%d", time.Now().UnixNano()))
	}
	name := fmt.Sprintf("face_%d_%s%s", time.Now().UnixNano(), hexEncode(randBytes), ext)
	fullpath := filepath.Join(destDir, name)

	if err := ioutil.WriteFile(fullpath, data, 0644); err != nil {
		return "", fmt.Errorf("write file failed: %v", err)
	}

	return fullpath, nil
}

// helper to hex-encode bytes (small wrapper to avoid importing encoding/hex in many places)
func hexEncode(b []byte) string {
	return fmt.Sprintf("%x", b)
}
