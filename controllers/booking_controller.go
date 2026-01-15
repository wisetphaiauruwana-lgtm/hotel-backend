// controllers/booking_controller.go
package controllers

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	mysql "github.com/go-sql-driver/mysql"
	"hotel-backend/config"
	"hotel-backend/models"
	"hotel-backend/services"
	"io/ioutil"
	"log"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ---------------------------
// Payload / DTOs
// ---------------------------

type InitiateCheckInPayload struct {
	BookingID uint `json:"bookingId" binding:"required"`
}

type ValidateCodePayload struct {
	CheckinCode string `json:"checkinCode" binding:"required"`
	Query       string `json:"query" binding:"required"` // lastName หรือ bookingRef
}

type ConfirmCheckInPayload struct {
	Token    string           `json:"token" binding:"required"`
	Guests   []models.Guest   `json:"guests"`
	Consents []models.Consent `json:"consents"`
}

// RoomItem รองรับ per-room details ที่ frontend อาจส่งมา
type RoomItem struct {
	RoomID uint     `json:"room_id" binding:"required"`
	Price  *float64 `json:"price,omitempty"`
	Nights *int     `json:"nights,omitempty"`
	Hours  *int     `json:"hours,omitempty"`
}

// CreateBookingRequest รองรับหลายรูปแบบ:
type CreateBookingRequest struct {
	CustomerID int                      `json:"customer_id" binding:"required"`
	CheckIn    string                   `json:"check_in" binding:"required"`
	CheckOut   string                   `json:"check_out" binding:"required"`
	RoomID     uint                     `json:"room_id"`
	RoomIDs    []uint                   `json:"room_ids"`
	Rooms      []RoomItem               `json:"rooms"`
	GuestList  []map[string]interface{} `json:"guest_list,omitempty"`
	SendEmail  bool                     `json:"send_email,omitempty"`

	// ✅ รองรับจำนวนแขก
	Adults   int `json:"adults"`
	Children int `json:"children"`
}

// ---------------------------
// Controller
// ---------------------------

type BookingController struct {
	BookingSvc *services.BookingService
}

func NewBookingController(svc *services.BookingService) *BookingController {
	return &BookingController{BookingSvc: svc}
}

func generateUniqueRoomAccessCode() (string, error) {
	for i := 0; i < 5; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(1000000))
		if err != nil {
			return "", err
		}
		code := fmt.Sprintf("%06d", n.Int64())
		var count int64
		if err := config.DB.Model(&models.Room{}).Where("access_code = ?", code).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return code, nil
		}
	}
	return "", fmt.Errorf("failed to generate unique access code")
}

// ---------------------------
// Helper: ดึง bookingId (string) จาก context/param/query
// คืนค่า id string และ bool ว่าพบหรือไม่
// ---------------------------
func getBookingIDString(c *gin.Context) (string, bool) {
	// 1) ถ้ามีใน context (เช่น middleware ตั้งไว้)
	if v, ok := c.Get("bookingId"); ok {
		if s, ok2 := v.(string); ok2 && s != "" {
			return s, true
		}
	}

	// 2) param :id
	if id := c.Param("id"); id != "" {
		return id, true
	}

	// 3) query ?bookingId=
	if q := c.Query("bookingId"); q != "" {
		return q, true
	}

	return "", false
}

// ---------------------------
// Helper: คืน structured error
// ---------------------------
func respondErrorMissingBookingID(c *gin.Context) {
	c.JSON(http.StatusBadRequest, gin.H{
		"error": gin.H{
			"code":    "error.missingBookingId",
			"message": "ไม่พบหมายเลขการจอง (bookingId) กรุณาตรวจสอบและลองใหม่",
		},
	})
}

// ---------------------------
// ✅ Helper: parse accompanying guests from Booking.AccompanyingGuests (datatypes.JSON)
// รองรับทั้ง JSON array/object หรือ string (best-effort)
// ---------------------------
func parseAccompanyingGuests(raw []byte) any {
	// default = []
	out := any([]any{})

	if len(raw) == 0 {
		return out
	}

	// try parse JSON
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if parsed == nil {
			return out
		}
		return parsed
	}

	// fallback: send raw string so frontend can still show/debug
	return string(raw)
}

// ---------------------------
// 1) Initiate Check-in
// ---------------------------

func (ctrl *BookingController) InitiateCheckIn(c *gin.Context) {
	var payload InitiateCheckInPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "error.invalidPayload",
				"message": "payload ไม่ถูกต้อง: ต้องมี bookingId",
				"details": err.Error(),
			},
		})
		return
	}

	bookingInfo, err := ctrl.BookingSvc.InitiateCheckInProcess(payload.BookingID)
	if err != nil {
		log.Printf("InitiateCheckIn error for booking %d: %v", payload.BookingID, err)

		switch {
		case strings.Contains(err.Error(), "booking_not_found"):
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "error.bookingNotFound", "message": "ไม่พบการจอง (Booking) ที่ระบุ"}})
			return

		case strings.Contains(err.Error(), "already_checked_in"):
			c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "error.alreadyCheckedIn", "message": "การจองนี้ถูกเช็คอินแล้ว"}})
			return

		case strings.Contains(err.Error(), "checkin_already_initiated"):
			c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "error.checkinAlreadyInitiated", "message": "มี session การเช็คอินที่กำลังใช้งานอยู่แล้ว"}})
			return

		case strings.Contains(err.Error(), "booking_checked_out"):
			c.JSON(http.StatusGone, gin.H{
				"error": gin.H{
					"code":    "error.bookingCheckedOut",
					"message": "การจองนี้เช็คเอาท์แล้ว ไม่สามารถเริ่มเช็คอินได้",
				},
			})
			return

		case strings.Contains(err.Error(), "email_send_failed"):
			// bookingInfo created but email sending failed -> return 206 with token & checkin_code
			c.JSON(http.StatusPartialContent, gin.H{
				"status": "warning",
				"data": gin.H{
					"id":           bookingInfo.ID,
					"token":        bookingInfo.Token,
					"checkin_code": bookingInfo.CheckinCode,
				},
				"error": gin.H{
					"code":    "error.emailSendFailed",
					"message": "สร้าง session การเช็คอินสำเร็จ แต่ส่งอีเมลไม่สำเร็จ",
					"details": err.Error(),
				},
			})
			return

		default:
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": gin.H{
					"code":    "error.internal",
					"message": "เกิดข้อผิดพลาดภายในระบบ",
					"details": err.Error(),
				},
			})
			return
		}
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

// ---------------------------
// 2) Manual Code Entry (/validate)
// ---------------------------

func (ctrl *BookingController) ValidateCheckinCode(c *gin.Context) {
	var p ValidateCodePayload
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "error.invalidPayload", "message": "ต้องระบุ checkinCode และ query (lastName หรือ bookingRef)"}})
		return
	}

	bi, err := ctrl.BookingSvc.ValidateCheckinCodeByBooking(p.CheckinCode, p.Query)
	if err != nil {

		// ✅ เพิ่มตรงนี้
		if strings.Contains(err.Error(), "booking_checked_out") {
			c.JSON(http.StatusGone, gin.H{
				"error": gin.H{
					"code":    "error.bookingCheckedOut",
					"message": "การจองนี้เช็คเอาท์แล้ว ไม่สามารถใช้รหัสนี้ได้",
				},
			})
			return
		}

		if strings.Contains(err.Error(), "invalid_or_expired_code") || strings.Contains(err.Error(), "invalid_or_expired") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "error.invalidOrExpiredCode", "message": "รหัสยืนยันไม่ถูกต้องหรือหมดอายุ"}})
			return
		}

		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "error.internal", "message": "เกิดข้อผิดพลาดขณะตรวจสอบรหัส", "details": err.Error()}})
		return
	}

	// success -> return token so frontend can redirect to /checkin?token=...
	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data": gin.H{
			"token":         bi.Token,
			"bookingInfoId": bi.ID,
		},
	})
}

// ---------------------------
// 3) Verify Token (click-link flow)
// ---------------------------

func (ctrl *BookingController) VerifyToken(c *gin.Context) {
	// --------------------
	// 1) Read token
	// --------------------
	token := c.Query("token")
	if token == "" {
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				token = parts[1]
			}
		}
	}

	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "error.missingToken",
				"message": "ไม่พบ token กรุณาตรวจสอบลิงก์",
			},
		})
		return
	}

	token = strings.TrimSpace(token)

	// --------------------
	// 2) Find BookingInfo
	// --------------------
	var bi models.BookingInfo
	now := time.Now().UTC()

	if err := ctrl.BookingSvc.DB.
		Where("token = ? AND (expires_at IS NULL OR expires_at > ?)", token, now).
		First(&bi).Error; err != nil {

		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"code":    "error.invalidOrExpiredToken",
					"message": "ลิงก์ยืนยันไม่ถูกต้องหรือหมดอายุ",
				},
			})
			return
		}

		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "error.internal",
				"message": "เกิดข้อผิดพลาดภายในระบบ",
			},
		})
		return
	}

	// --------------------
	// 3) Load Booking
	// --------------------
	var booking models.Booking
	if err := ctrl.BookingSvc.DB.
		Where("id = ?", bi.BookingID).
		Preload("Customer").
		Preload("Rooms.Room").
		Preload("Rooms.Room.RoomType").
		First(&booking).Error; err != nil {

		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"code":    "error.bookingNotFound",
					"message": "ไม่พบข้อมูลการจองสำหรับลิงก์นี้",
				},
			})
			return
		}

		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "error.bookingFetchFailed",
				"message": "ไม่สามารถโหลดข้อมูลการจองได้",
			},
		})
		return
	}

	// ✅ Block if booking already Checked-Out
	if strings.EqualFold(strings.TrimSpace(booking.Status), "Checked-Out") {
		c.JSON(http.StatusGone, gin.H{
			"error": gin.H{
				"code":    "error.bookingCheckedOut",
				"message": "การจองนี้เช็คเอาท์แล้ว ลิงก์นี้ใช้ไม่ได้",
			},
		})
		return
	}

	// --------------------
	// 4) Build rooms list
	// --------------------
	rooms := make([]map[string]interface{}, 0, len(booking.Rooms))
	for _, br := range booking.Rooms {
		roomNumber := ""
		roomType := ""
		accessCode := ""

		if br.Room.ID != 0 {
			if strings.TrimSpace(br.Room.RoomCode) != "" {
				roomNumber = strings.TrimSpace(br.Room.RoomCode)
			} else {
				roomNumber = strings.TrimSpace(br.Room.RoomNumber)
			}

			roomType = strings.TrimSpace(br.Room.Type)
			if roomType == "" && br.Room.RoomType.ID != 0 {
				roomType = strings.TrimSpace(br.Room.RoomType.TypeName)
			}
			accessCode = strings.TrimSpace(br.Room.AccessCode)
			if accessCode == "" {
				if code, err := generateUniqueRoomAccessCode(); err == nil {
					if err := config.DB.Model(&models.Room{}).Where("id = ?", br.Room.ID).Update("access_code", code).Error; err != nil {
						log.Printf("verify token: failed to persist access code for room %d: %v", br.Room.ID, err)
					} else {
						accessCode = code
					}
				} else {
					log.Printf("verify token: failed to generate access code for room %d: %v", br.Room.ID, err)
				}
			}
		}

		rooms = append(rooms, map[string]interface{}{
			"bookingInfoId": br.ID,
			"roomNumber":    roomNumber,
			"roomType":      roomType,
			"accessCode":    accessCode,
		})
	}

	// ✅ Parse accompanying guests
	accompanyingGuests := parseAccompanyingGuests([]byte(booking.AccompanyingGuests))

	// --------------------
	// 6) Already checked-in
	// --------------------
	if booking.CheckinCompleted || booking.CheckedInAt != nil {
		c.JSON(http.StatusOK, gin.H{
			"status": "already_checked_in",
			"data": gin.H{
				"bookingId":   booking.ID,
				"checkedInAt": booking.CheckedInAt,
				"stay": gin.H{
					"from":   booking.CheckInDate,
					"to":     booking.CheckOutDate,
					"nights": booking.Nights,
				},
				"customer": gin.H{
					"name":  booking.Customer.FullName,
					"email": booking.Customer.Email,
				},
				"rooms": rooms,

				"adults":             booking.Adults,
				"children":           booking.Children,
				"accompanyingGuests": accompanyingGuests,
			},
		})
		return
	}

	// --------------------
	// 7) Normal success (not yet checked-in)
	// --------------------
	numberOfNights := 0
	if booking.CheckIn != nil && booking.CheckOut != nil {
		numberOfNights = calculateNights(booking.CheckIn, booking.CheckOut)
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data": gin.H{
			"bookingInfoId":  bi.ID,
			"bookingId":      bi.BookingID,
			"guestEmail":     bi.GuestEmail,
			"guestLastName":  bi.GuestLastName,
			"tokenExpires":   bi.ExpiresAt,
			"checkInDate":    booking.CheckIn,
			"checkOutDate":   booking.CheckOut,
			"numberOfNights": numberOfNights,
			"rooms":          rooms,

			"adults":             booking.Adults,
			"children":           booking.Children,
			"accompanyingGuests": accompanyingGuests,
		},
	})
}

// calculateNights: ฟังก์ชันคำนวณคืนจาก *time.Time
func calculateNights(checkIn, checkOut *time.Time) int {
	if checkIn == nil || checkOut == nil {
		return 0
	}
	if checkOut.Before(*checkIn) {
		return 0
	}
	diff := checkOut.Sub(*checkIn).Hours() / 24
	nights := int(diff)
	if nights <= 0 {
		nights = 1
	}
	return nights
}

// ฟังก์ชันจัดรูปแบบช่วงเวลาการเข้าพัก
func formatStayDuration(checkIn, checkOut *time.Time) string {
	if checkIn == nil || checkOut == nil {
		return "N/A"
	}
	return checkIn.Format("2006-01-02") + " - " + checkOut.Format("2006-01-02")
}

// ---------------------------
// 4) Finalize / Confirm Check-in (T4)
// ---------------------------

func (ctrl *BookingController) ConfirmCheckIn(c *gin.Context) {
	var payload ConfirmCheckInPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		log.Printf("ConfirmCheckIn bind error: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "error.invalidPayload", "message": "payload ไม่ถูกต้องหรือขาดฟิลด์ที่จำเป็น", "details": err.Error()}})
		return
	}

	var guestModels []models.Guest
	for _, g := range payload.Guests {
		guestModels = append(guestModels, g)
	}

	if err := ctrl.BookingSvc.FinalizeCheckInTransaction(payload.Token, guestModels, payload.Consents); err != nil {
		log.Printf("FinalizeCheckInTransaction error (token=%s): %v", payload.Token, err)
		if strings.Contains(err.Error(), "invalid_or_expired_token") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "error.invalidOrExpiredToken", "message": "ลิงก์การเช็คอินไม่ถูกต้องหรือหมดอายุ"}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "error.finalizeFailed", "message": "ไม่สามารถยืนยันการเช็คอินได้", "details": err.Error()}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "เช็คอินเสร็จสิ้นและบันทึกข้อมูลแล้ว"})
}

// ---------------------------
// CRUD: Bookings
// ---------------------------

func (ctrl *BookingController) GetBookings(c *gin.Context) {
	bookings, err := ctrl.BookingSvc.GetAllWithRelations()
	if err != nil {
		log.Printf("GetBookings error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "error.fetchBookings", "message": "ไม่สามารถดึงรายการการจองได้"}})
		return
	}
	c.JSON(http.StatusOK, bookings)
}

func (ctrl *BookingController) CreateBooking(c *gin.Context) {
	var payload CreateBookingRequest

	bodyBytes, _ := ioutil.ReadAll(c.Request.Body)
	if len(bodyBytes) > 0 {
		c.Request.Body = ioutil.NopCloser(bytes.NewBuffer(bodyBytes))
		log.Printf("Incoming CreateBooking body: %s", string(bodyBytes))
	} else {
		log.Printf("Incoming request: %#v", c.Request)
	}

	if err := c.ShouldBindJSON(&payload); err != nil {
		log.Printf("Validation error: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body", "details": err.Error()})
		return
	}

	var roomIDs []uint
	if payload.RoomID != 0 {
		roomIDs = append(roomIDs, payload.RoomID)
	}
	if len(payload.RoomIDs) > 0 {
		for _, id := range payload.RoomIDs {
			if id != 0 {
				roomIDs = append(roomIDs, id)
			}
		}
	}
	if len(payload.Rooms) > 0 {
		for _, r := range payload.Rooms {
			if r.RoomID != 0 {
				roomIDs = append(roomIDs, r.RoomID)
			}
		}
	}

	unique := map[uint]struct{}{}
	var deduped []uint
	for _, id := range roomIDs {
		if _, ok := unique[id]; !ok {
			unique[id] = struct{}{}
			deduped = append(deduped, id)
		}
	}
	roomIDs = deduped

	if len(roomIDs) == 0 {
		log.Printf("CreateBooking: no room id provided in payload")
		c.JSON(http.StatusBadRequest, gin.H{"error": "room_id, room_ids or rooms is required"})
		return
	}

	for _, rid := range roomIDs {
		var r models.Room
		if err := config.DB.First(&r, rid).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				log.Printf("CreateBooking: room_id not found: %d", rid)
				c.JSON(http.StatusBadRequest, gin.H{
					"error":   "room_id not found",
					"details": gin.H{"room_id": rid},
				})
				return
			}
			log.Printf("CreateBooking: DB error checking room %d: %v", rid, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "internal error",
				"details": err.Error(),
			})
			return
		}
	}

	booking, err := ctrl.BookingSvc.CreateBookingMultiple(
		payload.CustomerID,
		payload.CheckIn,
		payload.CheckOut,
		roomIDs,
		payload.Adults,
		payload.Children,
		payload.GuestList,
		payload.SendEmail,
	)

	if err != nil {
		log.Printf("Service error creating booking: %v", err)
		if strings.Contains(err.Error(), "validation") || strings.Contains(err.Error(), "invalid") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to create booking", "details": err.Error()})
			return
		}
		if isForeignKeyError(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "foreign key constraint", "details": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create booking", "details": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Booking created successfully", "data": booking})
}

func (ctrl *BookingController) DeleteBooking(c *gin.Context) {
	idStr, ok := getBookingIDString(c)
	if !ok {
		respondErrorMissingBookingID(c)
		return
	}

	if err := ctrl.BookingSvc.DeleteByStringID(idStr); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "error.bookingNotFound", "message": "ไม่พบการจองที่ต้องการลบ"}})
			return
		}
		log.Printf("DeleteBooking error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "error.deleteBookingFailed", "message": "ไม่สามารถลบการจองได้"}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "ลบการจองเรียบร้อยแล้ว"})
}

// ---------------------------
// 5) Booking Details
// ---------------------------

func (ctrl *BookingController) GetBookingDetails(c *gin.Context) {
	idStr, ok := getBookingIDString(c)
	if !ok {
		respondErrorMissingBookingID(c)
		return
	}

	var bookingID uint64
	if parsed, err := strconv.ParseUint(idStr, 10, 64); err == nil {
		bookingID = parsed
	} else {
		log.Printf("GetBookingDetails: id is not numeric: %s", idStr)
	}

	var booking models.Booking
	var queryErr error
	if bookingID != 0 {
		queryErr = config.DB.Preload("Rooms.Room").Preload("Customer").First(&booking, bookingID).Error
	} else {
		queryErr = config.DB.Preload("Rooms.Room").Preload("Customer").Where("id = ?", idStr).First(&booking).Error
	}

	if queryErr != nil {
		if errors.Is(queryErr, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "error.bookingNotFound", "message": "ไม่พบการจอง"}})
			return
		}
		log.Printf("GetBookingDetails DB error: %v", queryErr)
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "error.fetchBookingFailed", "message": "ไม่สามารถดึงข้อมูลการจองได้"}})
		return
	}

	rooms := make([]map[string]interface{}, 0, len(booking.Rooms))
	for _, br := range booking.Rooms {
		num := ""
		rtype := ""
		desc := ""
		accessCode := ""

		if br.Room.ID != 0 {
			if strings.TrimSpace(br.Room.RoomCode) != "" {
				num = strings.TrimSpace(br.Room.RoomCode)
			} else {
				num = strings.TrimSpace(br.Room.RoomNumber)
			}
			rtype = strings.TrimSpace(br.Room.Type)
			desc = strings.TrimSpace(br.Room.Description)
			accessCode = strings.TrimSpace(br.Room.AccessCode)
			if accessCode == "" {
				if code, err := generateUniqueRoomAccessCode(); err == nil {
					if err := config.DB.Model(&models.Room{}).Where("id = ?", br.Room.ID).Update("access_code", code).Error; err != nil {
						log.Printf("booking details: failed to persist access code for room %d: %v", br.Room.ID, err)
					} else {
						accessCode = code
					}
				} else {
					log.Printf("booking details: failed to generate access code for room %d: %v", br.Room.ID, err)
				}
			}
		}

		rooms = append(rooms, map[string]interface{}{
			"bookingInfoId": br.ID,
			"roomNumber":    num,
			"roomType":      rtype,
			"accessCode":    accessCode,
			"roomDetails": map[string]interface{}{
				"description": desc,
			},
		})
	}

	nights := calculateNights(booking.CheckIn, booking.CheckOut)

	// ✅✅✅ เพิ่มตรงนี้
	accompanyingGuests := parseAccompanyingGuests([]byte(booking.AccompanyingGuests))

	// ✅✅✅ แก้ response ให้ส่ง fields เพิ่ม
	response := gin.H{
		"mainGuest":    booking.Customer.FullName,
		"email":        booking.Customer.Email,
		"stayDuration": formatStayDuration(booking.CheckIn, booking.CheckOut),
		"nights":       nights,
		"roomType":     "",
		"roomNumber":   "",
		"rooms":        rooms,

		"accompanyingGuests": accompanyingGuests,
		"adults":             booking.Adults,
		"children":           booking.Children,
	}

	c.JSON(http.StatusOK, response)
}

// ---------------------------
// Helper: detect MySQL FK error
// ---------------------------
func isForeignKeyError(err error) bool {
	if err == nil {
		return false
	}
	if merr, ok := err.(*mysql.MySQLError); ok {
		return merr.Number == 1452
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "foreign key") || strings.Contains(lower, "1452")
}

// ---------------------------
// 6) Checkout Booking
// ---------------------------
func (ctrl *BookingController) CheckoutBooking(c *gin.Context) {
	idStr := c.Param("id")
	if idStr == "" {
		respondErrorMissingBookingID(c)
		return
	}

	bookingID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "error.invalidBookingId",
				"message": "bookingId ไม่ถูกต้อง",
			},
		})
		return
	}

	if err := ctrl.BookingSvc.CheckoutBooking(uint(bookingID)); err != nil {
		log.Printf("CheckoutBooking error: %v", err)

		if strings.Contains(err.Error(), "not_checked_in") {
			c.JSON(http.StatusConflict, gin.H{
				"error": gin.H{
					"code":    "error.notCheckedIn",
					"message": "ไม่สามารถ checkout ได้ เนื่องจากยังไม่ check-in",
				},
			})
			return
		}

		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "error.checkoutFailed",
				"message": "Checkout ไม่สำเร็จ",
				"details": err.Error(),
			},
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Checkout สำเร็จ",
	})
}
