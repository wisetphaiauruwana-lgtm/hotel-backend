
package controllers

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hotel-backend/config"
	"hotel-backend/models"
	"hotel-backend/services"
	"hotel-backend/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// BookingInfoController ตัวเดิมของคุณ (constructor อยู่ที่ไฟล์เดิม)
type BookingInfoController struct {
	InfoSvc *services.BookingInfoService
}

func NewBookingInfoController(svc *services.BookingInfoService) *BookingInfoController {
	return &BookingInfoController{InfoSvc: svc}
}

// --- Existing CRUD methods (SaveBookingInfo, GetBookingInfoByID, DeleteBookingInfo) ---

// SaveBookingInfo creates or updates a BookingInfo entry
func (ctrl *BookingInfoController) SaveBookingInfo(c *gin.Context) {
	var bi models.BookingInfo
	if err := c.ShouldBindJSON(&bi); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	if err := ctrl.InfoSvc.SaveBookingInfo(bi); err != nil {
		log.Printf("SaveBookingInfo DB error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save booking info"})
		return
	}
	c.JSON(http.StatusCreated, bi)
}

// GetBookingInfoByID returns a BookingInfo by ID
func (ctrl *BookingInfoController) GetBookingInfoByID(c *gin.Context) {
	idStr := c.Param("id")
	if idStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing id"})
		return
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	bi, err := ctrl.InfoSvc.GetByID(uint(id))
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		log.Printf("GetBookingInfoByID DB error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve booking info"})
		return
	}
	c.JSON(http.StatusOK, bi)
}

// DeleteBookingInfo deletes a BookingInfo by ID
func (ctrl *BookingInfoController) DeleteBookingInfo(c *gin.Context) {
	c.JSON(http.StatusForbidden, gin.H{
		"error": "booking_info deletion is disabled",
	})
}

// ------------------------------
// Checkin: Validate / Resend
// ------------------------------

// ValidateCheckinCode (POST /api/checkin/validate)
// Body: { "checkinCode": "AWLI-TEJN", "query": "lastnameOrRef" }
func (ctrl *BookingInfoController) ValidateCheckinCode(c *gin.Context) {
	var req struct {
		CheckinCode string `json:"checkinCode"`
		Query       string `json:"query"` // optional
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	codeRaw := strings.TrimSpace(req.CheckinCode)
	if codeRaw == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "checkinCode required"})
		return
	}

	norm := utils.NormalizeCheckinCode(codeRaw)
	if len(norm) != 8 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid checkin code format"})
		return
	}
	formatted := strings.ToUpper(norm[:4] + "-" + norm[4:])
	noDash := strings.ToUpper(norm)

	bi, expired, err := ctrl.InfoSvc.FindByCodeWithExpiry(formatted, noDash)
	if err != nil {
		// not found or DB error
		// If it's a DB error different from not found, log it
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("ValidateCheckinCode error: %v", err)
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "confirmation code not found"})
		return
	}

	if expired {
		var expiresAt interface{}
		if bi.CodeExpiresAt != nil {
			expiresAt = bi.CodeExpiresAt.UTC().Format(time.RFC3339)
		} else {
			expiresAt = nil
		}
		c.JSON(http.StatusGone, gin.H{
			"error":         "confirmation code expired",
			"bookingInfoId": bi.ID,
			"expiresAt":     expiresAt,
		})
		return
	}

	// Block if booking already Checked-Out
	var booking models.Booking
	if err := config.DB.Select("status", "check_out").Where("id = ?", bi.BookingID).First(&booking).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
			return
		}
		log.Printf("ValidateCheckinCode booking fetch error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load booking"})
		return
	}
	if strings.EqualFold(strings.TrimSpace(booking.Status), "Checked-Out") || booking.CheckOut != nil {
		c.JSON(http.StatusGone, gin.H{
			"error":   "booking checked out",
			"message": "การจองนี้เช็คเอาท์แล้ว ไม่สามารถใช้รหัสนี้ได้",
		})
		return
	}

	// Optionally validate query (last name or booking reference) if provided
	// Current implementation returns success if code is valid and not expired.
	// If you want to enforce query matching, implement additional checks here.

	c.JSON(http.StatusOK, gin.H{
		"status":        "ok",
		"bookingInfoId": bi.ID,
		"bookingId":     bi.BookingID,
		"checkinCode":   bi.CheckinCode,
		"token":         bi.Token,
	})
}

// ResendCheckinCode (POST /api/checkin/resend)
// Body: { "bookingInfoId": 10 } OR { "checkinCode": "AWLI-TEJN" }
func (ctrl *BookingInfoController) ResendCheckinCode(c *gin.Context) {
	var req struct {
		BookingInfoId uint   `json:"bookingInfoId"`
		CheckinCode   string `json:"checkinCode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	var bi models.BookingInfo
	var err error

	if req.BookingInfoId > 0 {
		bi, err = ctrl.InfoSvc.GetByID(req.BookingInfoId)
	} else if strings.TrimSpace(req.CheckinCode) != "" {
		norm := utils.NormalizeCheckinCode(req.CheckinCode)
		if len(norm) != 8 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid checkin code format"})
			return
		}
		formatted := strings.ToUpper(norm[:4] + "-" + norm[4:])
		noDash := strings.ToUpper(norm)
		bi, _, err = ctrl.InfoSvc.FindByCodeWithExpiry(formatted, noDash)
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing bookingInfoId or checkinCode"})
		return
	}

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "booking info not found"})
		return
	}

	newExpiry, err := ctrl.InfoSvc.ExtendExpiry(bi.ID, 15) // extend by 15 minutes
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to extend expiry"})
		return
	}

	// async resend email (best-effort)
	go func(b models.BookingInfo) {
		// build checkin link
		link := utils.BuildCheckinLink(utils.EnvOrDefault("FRONTEND_URL", "http://localhost:3000"), b.Token, true)
		link = utils.AppendBookingIDParam(link, b.BookingID)

		// Use helper to gather rooms & send email (silently ignore error)
		if err := ctrl.sendCheckInEmail(
			b.GuestEmail,
			"", // bookingRef optional - helper will load booking and use reference if present
			link,
			b.GuestLastName,
			b.BookingID,
			"", // checkInDate (unknown here)
			"", // checkOutDate
			b.CheckinCode,
		); err != nil {
			log.Printf("ResendCheckinCode: send email failed for bookingInfo %d: %v", b.ID, err)
		}
	}(bi)

	c.JSON(http.StatusOK, gin.H{
		"message":       "code resent",
		"bookingInfoId": bi.ID,
		"expiresAt":     newExpiry.UTC().Format(time.RFC3339),
	})
}

// InitiateCheckIn (POST /api/checkin/initiate)
// Body: { "bookingId": 123 }
func (ctrl *BookingInfoController) InitiateCheckIn(c *gin.Context) {
	var req struct {
		BookingID uint `json:"bookingId" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	// Call service to initiate check-in
	bookingInfo, err := ctrl.InfoSvc.InitiateCheckIn(req.BookingID)
	if err != nil {
		log.Printf("InitiateCheckIn error: %v", err)
		switch {
		case strings.Contains(err.Error(), "booking_not_found"):
			c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
		case strings.Contains(err.Error(), "already_checked_in"):
			c.JSON(http.StatusConflict, gin.H{"error": "already checked in"})
		case strings.Contains(err.Error(), "email_send_failed"):
			c.JSON(http.StatusPartialContent, gin.H{
				"status": "warning",
				"data": gin.H{
					"id":           bookingInfo.ID,
					"token":        bookingInfo.Token,
					"checkin_code": bookingInfo.CheckinCode,
				},
				"error": "email send failed",
			})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data": gin.H{
			"id":           bookingInfo.ID,
			"token":        bookingInfo.Token,
			"checkin_code": bookingInfo.CheckinCode,
		},
	})
}

// -----------------------------
// Helper: load rooms for booking and send email
// -----------------------------
// -----------------------------
// Helper: load rooms for booking and send email
// -----------------------------
func (ctrl *BookingInfoController) sendCheckInEmail(
	recipientEmail string,
	bookingRef string,
	checkinLink string,
	guestName string,
	bookingID uint,
	checkInDate string,
	checkOutDate string,
	confirmationCode string,
) error {
	// Load booking (we only need reference and maybe legacy Room)
	var booking models.Booking
	if err := config.DB.
		Preload("Room"). // legacy single-room relation if present
		Where("id = ?", bookingID).
		First(&booking).Error; err != nil {

		// If booking not found, still attempt to send email with empty rooms slice
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("sendCheckInEmail: booking %d not found: %v", bookingID, err)
			return utils.SendCheckInLinkEmail(
				recipientEmail,
				bookingRef,
				checkinLink,
				guestName,
				[]utils.RoomInfo{},
				checkInDate,
				checkOutDate,
				confirmationCode,
			)
		}
		return fmt.Errorf("failed to load booking %d: %w", bookingID, err)
	}

	// If bookingRef not provided, use booking.ReferenceCode if available
	if bookingRef == "" {
		bookingRef = strings.TrimSpace(booking.ReferenceCode)
	}

	// Query booking_rooms directly to get all rooms for the booking.
	// This avoids assuming a specific field name on the Booking struct.
	var bookingRooms []models.BookingRoom
	if err := config.DB.Preload("Room").Where("booking_id = ?", bookingID).Find(&bookingRooms).Error; err != nil {
		// non-fatal: log and continue with fallback
		log.Printf("sendCheckInEmail: failed to query booking_rooms for booking %d: %v", bookingID, err)
	}

	// Build rooms slice from bookingRooms (if any)
	rooms := []utils.RoomInfo{}
	for _, br := range bookingRooms {
		// Ensure Room was loaded
		if br.Room.ID == 0 {
			continue
		}
		num := strings.TrimSpace(br.Room.RoomCode)
		if num == "" {
			num = strings.TrimSpace(br.Room.RoomNumber)
		}
		rooms = append(rooms, utils.RoomInfo{
			Number: num,
			Type:   strings.TrimSpace(br.Room.Type),
		})
	}

	// Fallback: if no booking_rooms rows, try booking.Room (single-room legacy)
	if len(rooms) == 0 && booking.Room.ID != 0 {
		num := strings.TrimSpace(booking.Room.RoomCode)
		if num == "" {
			num = strings.TrimSpace(booking.Room.RoomNumber)
		}
		rooms = append(rooms, utils.RoomInfo{
			Number: num,
			Type:   strings.TrimSpace(booking.Room.Type),
		})
	}

	// Finally call the utils email sender
	if err := utils.SendCheckInLinkEmail(
		recipientEmail,
		bookingRef,
		checkinLink,
		guestName,
		rooms,
		checkInDate,
		checkOutDate,
		confirmationCode,
	); err != nil {
		return fmt.Errorf("failed to send checkin email: %w", err)
	}
	return nil
}
