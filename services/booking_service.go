// services/booking_service.go
package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"hotel-backend/models"
	"hotel-backend/utils"

	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BookingService เป็น wrapper รอบ *gorm.DB เพื่อแยก logic ของ booking
type BookingService struct {
	DB *gorm.DB
}

func NewBookingService(db *gorm.DB) *BookingService {
	return &BookingService{DB: db}
}

// ✅ helper ดึง string จาก map (รองรับหลายชื่อ key)
func getStringFromMap(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			if s, ok2 := v.(string); ok2 {
				return strings.TrimSpace(s)
			}
			return strings.TrimSpace(fmt.Sprintf("%v", v))
		}
	}
	return ""
}

// ✅ helper normalize guest list -> keep only safe fields (optional)
func normalizeGuestList(guestList []map[string]interface{}) []map[string]interface{} {
	if len(guestList) == 0 {
		return []map[string]interface{}{}
	}
	out := make([]map[string]interface{}, 0, len(guestList))
	for _, g := range guestList {
		name := getStringFromMap(g, "name", "fullName", "full_name")
		typ := getStringFromMap(g, "type", "guestType", "guest_type")

		if name == "" {
			continue
		}
		if typ == "" {
			typ = "Adult"
		}

		out = append(out, map[string]interface{}{
			"fullName": name,
			"type":     typ,
		})
	}
	return out
}

// InitiateCheckInProcess: สร้าง BookingInfo (token + checkin code) และส่งอีเมลเชิญเช็คอิน
func (s *BookingService) InitiateCheckInProcess(bookingID uint) (models.BookingInfo, error) {
	var booking models.Booking
	if err := s.DB.Preload("Rooms.Room.RoomType").Preload("Customer").First(&booking, bookingID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.BookingInfo{}, errors.New("booking_not_found")
		}
		return models.BookingInfo{}, fmt.Errorf("failed to find booking: %w", err)
	}

	// validations
	if booking.Customer.ID == 0 {
		return models.BookingInfo{}, errors.New("missing customer")
	}
	if len(booking.Rooms) == 0 && booking.Room.ID == 0 {
		return models.BookingInfo{}, errors.New("missing room")
	}
	if strings.TrimSpace(booking.Customer.Email) == "" {
		return models.BookingInfo{}, errors.New("customer_email_missing")
	}
	if strings.EqualFold(booking.Status, "Checked-In") ||
		strings.EqualFold(booking.Status, "Checkedin") ||
		strings.EqualFold(booking.Status, "Checked in") {
		return models.BookingInfo{}, errors.New("already_checked_in")
	}
	if booking.CheckedInAt != nil {
		return models.BookingInfo{}, errors.New("already_checked_in")
	}
	if strings.EqualFold(strings.TrimSpace(booking.Status), "Checked-Out") {
		return models.BookingInfo{}, errors.New("booking_checked_out")
	}

	// check existing non-expired booking_info
	var existing models.BookingInfo
	now := time.Now().UTC()
	err := s.DB.
		Where("(expires_at IS NULL OR expires_at > ?) AND booking_id = ? AND deleted_at IS NULL", now, bookingID).
		Order("id DESC").
		First(&existing).Error
	if err == nil {
		return models.BookingInfo{}, errors.New("checkin_already_initiated")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return models.BookingInfo{}, fmt.Errorf("failed to check existing booking info: %w", err)
	}

	// create booking_info with retries on unique collision
	var bookingInfo models.BookingInfo
	maxRetries := 5
	var createErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		token, gErr := utils.GenerateSecureToken(32)
		if gErr != nil {
			return models.BookingInfo{}, fmt.Errorf("failed to generate token: %w", gErr)
		}
		rawCode, cErr := utils.GenerateCheckinCode(8)
		if cErr != nil {
			return models.BookingInfo{}, fmt.Errorf("failed to generate checkin code: %w", cErr)
		}
		formatted, fErr := utils.GenerateFormattedCheckinCode(rawCode)
		if fErr != nil {
			return models.BookingInfo{}, fmt.Errorf("failed to format checkin code: %w", fErr)
		}

		expiresAt := time.Now().UTC().Add(24 * time.Hour)
		var codeExp *time.Time
		if strings.ToLower(utils.EnvOrDefault("CHECKIN_CODE_NEVER_EXPIRE", "false")) == "true" {
			codeExp = nil
		} else {
			t := time.Now().UTC().Add(7 * 24 * time.Hour)
			codeExp = &t
		}

		bookingInfo = models.BookingInfo{
			BookingID:     bookingID,
			Token:         token,
			CheckinCode:   formatted,
			Status:        "INITIATED",
			EmailStatus:   "PENDING",
			ExpiresAt:     &expiresAt,
			CodeExpiresAt: codeExp,
			GuestEmail:    booking.Customer.Email,
			GuestLastName: booking.Customer.FullName,
		}

		createErr = s.DB.Create(&bookingInfo).Error
		if createErr == nil {
			break
		}

		lc := strings.ToLower(createErr.Error())
		if strings.Contains(lc, "duplicate") || strings.Contains(lc, "unique") || strings.Contains(lc, "constraint") {
			log.Printf("create booking_info collision (attempt %d) - retrying", attempt+1)
			continue
		}
		return models.BookingInfo{}, fmt.Errorf("failed to create booking info: %w", createErr)
	}
	if createErr != nil {
		return models.BookingInfo{}, fmt.Errorf("failed to create booking info after retries: %w", createErr)
	}

	// build rooms list for email (best-effort)
	roomsForEmail := []utils.RoomInfo{}
	if len(booking.Rooms) > 0 {
		for _, br := range booking.Rooms {
			num := ""
			typ := ""
			if br.Room.ID != 0 {
				if strings.TrimSpace(br.Room.RoomCode) != "" {
					num = strings.TrimSpace(br.Room.RoomCode)
				} else {
					num = strings.TrimSpace(br.Room.RoomNumber)
				}
				if strings.TrimSpace(br.Room.Type) != "" {
					typ = strings.TrimSpace(br.Room.Type)
				} else if br.Room.RoomType.ID != 0 {
					typ = strings.TrimSpace(br.Room.RoomType.TypeName)
				}
			}
			roomsForEmail = append(roomsForEmail, utils.RoomInfo{Number: num, Type: typ})
		}
	} else if booking.Room.ID != 0 {
		num := strings.TrimSpace(booking.Room.RoomCode)
		if num == "" {
			num = strings.TrimSpace(booking.Room.RoomNumber)
		}
		typ := strings.TrimSpace(booking.Room.Type)
		if typ == "" && booking.Room.RoomType.ID != 0 {
			typ = strings.TrimSpace(booking.Room.RoomType.TypeName)
		}
		roomsForEmail = append(roomsForEmail, utils.RoomInfo{Number: num, Type: typ})
	}

	frontend := utils.EnvOrDefault("FRONTEND_URL", "http://localhost:3000")
	checkinLink := fmt.Sprintf("%s/checkin?token=%s", strings.TrimRight(frontend, "/"), bookingInfo.Token)

	// send email (best-effort) and update email status
	if mailErr := utils.SendCheckInLinkEmail(
		booking.Customer.Email,
		booking.ReferenceCode,
		checkinLink,
		booking.Customer.FullName,
		roomsForEmail,
		func() string {
			if booking.CheckIn != nil {
				return booking.CheckIn.Format("2006-01-02")
			}
			return "N/A"
		}(),
		func() string {
			if booking.CheckOut != nil {
				return booking.CheckOut.Format("2006-01-02")
			}
			return "N/A"
		}(),
		bookingInfo.CheckinCode,
	); mailErr != nil {
		_ = s.DB.Model(&bookingInfo).Where("id = ?", bookingInfo.ID).
			Updates(map[string]interface{}{"email_status": "FAILED", "email_error": mailErr.Error()}).Error
		return bookingInfo, fmt.Errorf("email_send_failed: %w", mailErr)
	}

	_ = s.DB.Model(&bookingInfo).Where("id = ?", bookingInfo.ID).
		Updates(map[string]interface{}{"email_status": "SENT"}).Error

	return bookingInfo, nil
}

// ValidateCheckinCodeByBooking: ตรวจสอบ code + query (name or reference code) และยังไม่หมดอายุ
func (s *BookingService) ValidateCheckinCodeByBooking(code string, query string) (models.BookingInfo, error) {
	var bi models.BookingInfo
	now := time.Now().UTC()

	norm := utils.NormalizeCheckinCode(code)
	if len(norm) != 8 {
		return bi, errors.New("invalid_or_expired_code")
	}
	formatted := strings.ToUpper(norm[:4] + "-" + norm[4:])

	qLower := strings.ToLower(strings.TrimSpace(query))
	if qLower == "" {
		return bi, errors.New("invalid_query")
	}

	err := s.DB.
		Table("booking_infos").
		Select("booking_infos.*").
		Joins("JOIN bookings ON bookings.id = booking_infos.booking_id").
		Joins("JOIN customers ON customers.id = bookings.customer_id").
		Where("(booking_infos.checkin_code = ? OR booking_infos.checkin_code = ?)", formatted, strings.ToUpper(code)).
		Where("( (LOWER(customers.full_name) LIKE ?) OR (LOWER(bookings.reference_code) = ?) )", "%"+qLower+"%", qLower).
		Where("(booking_infos.code_expires_at IS NULL OR booking_infos.code_expires_at > ?)", now).
		First(&bi).Error

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var biAny models.BookingInfo
			err2 := s.DB.
				Table("booking_infos").
				Select("booking_infos.*").
				Where("booking_infos.checkin_code = ? OR booking_infos.checkin_code = ?", formatted, strings.ToUpper(code)).
				First(&biAny).Error
			if err2 == nil {
				log.Printf("ValidateCheckinCodeByBooking: code exists but expired (booking_info_id=%d)", biAny.ID)
			}
			return bi, errors.New("invalid_or_expired_code")
		}
		return bi, fmt.Errorf("failed to validate code: %w", err)
	}

	return bi, nil
}

// FinalizeCheckInTransaction: ทำงานใน transaction — อัพเดต booking, insert guests, save consent logs, finalize booking_info
func (s *BookingService) FinalizeCheckInTransaction(
	token string,
	guests []models.Guest,
	consents []models.Consent,
) error {

	now := time.Now().UTC()

	return s.DB.Transaction(func(tx *gorm.DB) error {

		var bookingInfo models.BookingInfo
		if err := tx.
			Where("token = ? AND (expires_at IS NULL OR expires_at > ?)", token, now).
			First(&bookingInfo).Error; err != nil {

			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("invalid_or_expired_token")
			}
			return err
		}

		// idempotent
		if bookingInfo.Status == "COMPLETED" {
			return nil
		}

		bookingID := bookingInfo.BookingID

		var booking models.Booking
		if err := tx.
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&booking, bookingID).Error; err != nil {
			return err
		}

		if booking.CheckedInAt != nil || booking.CheckinCompleted {
			return nil
		}

		// ✅ update booking + number_of_guests ตามของจริง
		if err := tx.Model(&booking).Updates(map[string]interface{}{
			"status":            "Checked-In",
			"check_in":          now,
			"checked_in_at":     now,
			"checkin_completed": true,
			"number_of_guests":  len(guests),
		}).Error; err != nil {
			return err
		}

		// insert guests (ลูกค้ากรอกจริง)
		insertedGuestIDs := make([]uint, 0, len(guests))
		for i := range guests {
			guests[i].BookingID = &bookingID
			if err := tx.Create(&guests[i]).Error; err != nil {
				return err
			}
			insertedGuestIDs = append(insertedGuestIDs, guests[i].ID)
		}

		// save consent logs
		for _, gid := range insertedGuestIDs {
			for _, c := range consents {
				gidLocal := gid
				logEntry := models.ConsentLog{
					BookingID:  &bookingID,
					ConsentID:  c.ID,
					GuestID:    &gidLocal,
					AcceptedAt: now,
					Status:     "accepted",
				}
				if err := tx.Create(&logEntry).Error; err != nil {
					return err
				}
			}
		}

		// finalize booking_info
		if err := tx.Model(&bookingInfo).
			Updates(map[string]interface{}{
				"status": "COMPLETED",
			}).Error; err != nil {
			return err
		}

		return nil
	})
}

// CreateBooking: สร้าง booking แบบ single-room helper
func (s *BookingService) CreateBooking(customerID int, checkIn string, checkOut string, roomID uint) (*models.Booking, error) {
	ci, err := time.Parse("2006-01-02", checkIn)
	if err != nil {
		return nil, fmt.Errorf("invalid check_in format: %w", err)
	}
	co, err := time.Parse("2006-01-02", checkOut)
	if err != nil {
		return nil, fmt.Errorf("invalid check_out format: %w", err)
	}

	var room models.Room
	if err := s.DB.First(&room, roomID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("room_id %d not found", roomID)
		}
		return nil, fmt.Errorf("db error checking room %d: %w", roomID, err)
	}

	bk := &models.Booking{
		CustomerID: uint(customerID),
		CheckIn:    &ci,
		CheckOut:   &co,
	}

	if err := s.DB.Create(bk).Error; err != nil {
		return nil, fmt.Errorf("failed to create booking: %w", err)
	}
	return bk, nil
}

// GetBookingDetails
func (s *BookingService) GetBookingDetails(bookingID uint) (*models.Booking, error) {
	var bk models.Booking
	if err := s.DB.Preload("Rooms.Room.RoomType").Preload("Customer").First(&bk, bookingID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("booking_not_found")
		}
		return nil, fmt.Errorf("failed to retrieve booking details: %w", err)
	}
	return &bk, nil
}

// GetAllWithRelations
func (s *BookingService) GetAllWithRelations() ([]models.Booking, error) {
	var list []models.Booking

	if err := s.DB.
		Preload("Customer").
		Preload("Rooms").
		Preload("Rooms.Room").
		Preload("Rooms.Room.RoomType").
		Order("created_at DESC").
		Find(&list).Error; err != nil {

		return nil, fmt.Errorf("failed to retrieve bookings: %w", err)
	}

	for i := range list {
		if list[i].Rooms == nil {
			list[i].Rooms = []models.BookingRoom{}
		}
	}

	return list, nil
}

// DeleteByStringID
func (s *BookingService) DeleteByStringID(referenceCode string) error {
	ref := strings.TrimSpace(referenceCode)
	if ref == "" {
		return gorm.ErrRecordNotFound
	}

	// Try numeric id first if possible
	if id, err := strconv.ParseUint(ref, 10, 64); err == nil && id != 0 {
		res := s.DB.Delete(&models.Booking{}, id)
		if res.Error != nil {
			return fmt.Errorf("failed to delete booking: %w", res.Error)
		}
		if res.RowsAffected > 0 {
			return nil
		}
	}

	// Fallback to reference_code
	res := s.DB.Where("reference_code = ?", ref).Delete(&models.Booking{})
	if res.Error != nil {
		return fmt.Errorf("failed to delete booking: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ✅ CreateBookingMultiple:
// - เก็บ adults/children/summary ลง bookings
// - เก็บ accompanying guests (draft) ลง bookings เป็น JSON
// - ❌ ไม่ insert ลงตาราง guests (เพราะลูกค้าจะกรอกใหม่ตอน ConfirmCheckIn)
func (s *BookingService) CreateBookingMultiple(
	customerID int,
	checkIn, checkOut string,
	roomIDs []uint,
	adults int,
	children int,
	guestList []map[string]interface{},
	sendEmail bool,
) (models.Booking, error) {

	var resultBooking models.Booking

	if len(roomIDs) == 0 {
		return resultBooking, fmt.Errorf("validation: no room ids provided")
	}

	if adults <= 0 {
		adults = 1
	}
	if children < 0 {
		children = 0
	}

	// ✅ normalize + marshal guestList เก็บเป็น draft ใน booking
	normalizedGuests := normalizeGuestList(guestList)
	accompanyingJSON, _ := json.Marshal(normalizedGuests) // best-effort

	// ensure customer exists
	var cust models.Customer
	if err := s.DB.First(&cust, customerID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return resultBooking, fmt.Errorf("validation: customer not found")
		}
		return resultBooking, fmt.Errorf("db error checking customer: %w", err)
	}

	// validate rooms exist
	for _, rid := range roomIDs {
		if rid == 0 {
			return resultBooking, fmt.Errorf("validation: invalid room id 0 in roomIDs")
		}
		var rm models.Room
		if err := s.DB.First(&rm, rid).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return resultBooking, fmt.Errorf("validation: room %d not found", rid)
			}
			return resultBooking, fmt.Errorf("db error checking room %d: %w", rid, err)
		}
	}

	// parse dates (best-effort)
	var checkInDate *time.Time
	var checkOutDate *time.Time
	if checkIn != "" {
		if t, err := time.Parse("2006-01-02", checkIn); err == nil {
			checkInDate = &t
		} else if t2, err2 := time.Parse(time.RFC3339, checkIn); err2 == nil {
			checkInDate = &t2
		} else {
			return resultBooking, fmt.Errorf("validation: invalid check_in format: %v", err)
		}
	}
	if checkOut != "" {
		if t, err := time.Parse("2006-01-02", checkOut); err == nil {
			checkOutDate = &t
		} else if t2, err2 := time.Parse(time.RFC3339, checkOut); err2 == nil {
			checkOutDate = &t2
		} else {
			return resultBooking, fmt.Errorf("validation: invalid check_out format: %v", err)
		}
	}

	var bookingID uint
	var bookingRef string

	// transaction create booking + booking_room + update room status
	txErr := s.DB.Transaction(func(tx *gorm.DB) error {
		var ciDate *time.Time
		var coDate *time.Time

		if checkInDate != nil {
			t := time.Date(checkInDate.Year(), checkInDate.Month(), checkInDate.Day(), 0, 0, 0, 0, checkInDate.Location())
			ciDate = &t
		}

		if checkOutDate != nil {
			t := time.Date(checkOutDate.Year(), checkOutDate.Month(), checkOutDate.Day(), 0, 0, 0, 0, checkOutDate.Location())
			coDate = &t
		}

		booking := models.Booking{
			CustomerID:   uint(customerID),
			CheckIn:      checkInDate,
			CheckOut:     checkOutDate,
			CheckInDate:  ciDate,
			CheckOutDate: coDate,
			Status:       "Confirmed",

			Adults:         adults,
			Children:       children,
			NumberOfGuests: adults + children,

			// ✅ ต้องมี field นี้ใน models.Booking ด้วย
			AccompanyingGuests: datatypes.JSON(accompanyingJSON),
		}

		if err := tx.Create(&booking).Error; err != nil {
			return fmt.Errorf("failed to create booking: %w", err)
		}

		bookingID = booking.ID
		bookingRef = booking.ReferenceCode

		nights := 0
		if checkInDate != nil && checkOutDate != nil && checkOutDate.After(*checkInDate) {
			n := int(checkOutDate.Sub(*checkInDate).Hours() / 24)
			if n <= 0 {
				n = 1
			}
			nights = n
		}

		for _, rid := range roomIDs {
			br := models.BookingRoom{
				BookingID: booking.ID,
				RoomID:    rid,
				Nights:    nights,
				Status:    "Reserved",
			}
			if err := tx.Create(&br).Error; err != nil {
				return fmt.Errorf("failed to create booking_room for room %d: %w", rid, err)
			}

			if err := tx.Model(&models.Room{}).
				Where("id = ?", rid).
				Updates(map[string]interface{}{"status": "Reserved"}).Error; err != nil {
				return fmt.Errorf("failed to update room %d status: %w", rid, err)
			}
		}

		// ❌ ไม่สร้าง records ใน guests ที่นี่แล้ว

		return nil
	})

	if txErr != nil {
		return resultBooking, txErr
	}

	// optional: send checkin link email for newly created booking
	if sendEmail {
		token, genErr := utils.GenerateSecureToken(32)
		if genErr != nil {
			return resultBooking, fmt.Errorf("email_send_failed: failed to generate token: %w", genErr)
		}
		raw, gErr := utils.GenerateCheckinCode(8)
		if gErr != nil {
			return resultBooking, fmt.Errorf("email_send_failed: failed to generate code: %w", gErr)
		}
		formatted, fErr := utils.GenerateFormattedCheckinCode(raw)
		if fErr != nil {
			return resultBooking, fmt.Errorf("email_send_failed: failed to format code: %w", fErr)
		}

		expiresAt := time.Now().UTC().Add(24 * time.Hour)
		codeExpires := time.Now().UTC().Add(7 * 24 * time.Hour)

		bookingInfo := models.BookingInfo{
			BookingID:     bookingID,
			Token:         token,
			CheckinCode:   formatted,
			Status:        "INITIATED",
			EmailStatus:   "PENDING",
			ExpiresAt:     &expiresAt,
			CodeExpiresAt: &codeExpires,
			GuestEmail:    cust.Email,
			GuestLastName: cust.FullName,
		}

		if err := s.DB.Create(&bookingInfo).Error; err != nil {
			return resultBooking, fmt.Errorf("email_send_failed: failed to create booking_info: %w", err)
		}

		frontend := utils.EnvOrDefault("FRONTEND_URL", "http://localhost:3000")
		checkinLink := fmt.Sprintf("%s/checkin?token=%s", strings.TrimRight(frontend, "/"), bookingInfo.Token)

		// load room models for email content
		var roomModels []models.Room
		if err := s.DB.Where("id IN ?", roomIDs).Find(&roomModels).Error; err != nil {
			log.Printf("warning: failed to load room models for email: %v", err)
		}
		roomsForEmail := make([]utils.RoomInfo, 0, len(roomModels))
		for _, rm := range roomModels {
			num := strings.TrimSpace(rm.RoomCode)
			if num == "" {
				num = strings.TrimSpace(rm.RoomNumber)
			}
			roomsForEmail = append(roomsForEmail, utils.RoomInfo{Number: num, Type: strings.TrimSpace(rm.Type)})
		}

		if mailErr := utils.SendCheckInLinkEmail(
			cust.Email,
			bookingRef,
			// ถ้าต้องการให้ไม่ว่าง ต้องแน่ใจว่า booking มี reference_code ถูก generate แล้ว
			checkinLink,
			cust.FullName,
			roomsForEmail,
			checkIn,
			checkOut,
			bookingInfo.CheckinCode,
		); mailErr != nil {
			_ = s.DB.Model(&bookingInfo).Where("id = ?", bookingInfo.ID).
				Updates(map[string]interface{}{"email_status": "FAILED", "email_error": mailErr.Error()}).Error
			return resultBooking, fmt.Errorf("email_send_failed: %w", mailErr)
		}
		_ = s.DB.Model(&bookingInfo).Where("id = ?", bookingInfo.ID).
			Updates(map[string]interface{}{"email_status": "SENT"}).Error
	}

	// reload booking with relations (สำคัญมาก)
	if err := s.DB.
		Preload("Customer").
		Preload("Rooms").
		Preload("Rooms.Room").
		Preload("Rooms.Room.RoomType").
		First(&resultBooking, bookingID).Error; err != nil {

		return resultBooking, err
	}
	if resultBooking.Rooms == nil {
		resultBooking.Rooms = []models.BookingRoom{}
	}

	return resultBooking, nil
}

// ✅ CheckoutBooking: แก้ให้เป็น Checked-Out (ของเดิมผิด)
func (s *BookingService) CheckoutBooking(bookingID uint) error {
	return s.DB.Transaction(func(tx *gorm.DB) error {

		var booking models.Booking
		if err := tx.Preload("Rooms").First(&booking, bookingID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("booking_not_found")
			}
			return err
		}

		if booking.Status != "Checked-In" {
			return fmt.Errorf("not_checked_in")
		}

		now := time.Now().UTC()

		if err := tx.Model(&booking).Updates(map[string]interface{}{
			"status":    "Checked-Out",
			"check_out": now,
		}).Error; err != nil {
			return err
		}
		// ✅ IMPORTANT: checkout แล้วต้องทำให้ token ใช้ไม่ได้ทันที
		// วิธีที่ปลอดภัย: set expires_at = now (ทำให้ VerifyToken ไม่ผ่านเงื่อนไข expires_at > now)
		if err := tx.Model(&models.BookingInfo{}).
			Where("booking_id = ? AND deleted_at IS NULL", bookingID).
			Updates(map[string]interface{}{
				"expires_at": now,
				"status":     "EXPIRED", // ถ้าไม่มี field status ใน BookingInfo ให้ลบบรรทัดนี้ทิ้ง
			}).Error; err != nil {
			return err
		}

		for _, br := range booking.Rooms {
			if err := tx.Model(&models.Room{}).
				Where("id = ?", br.RoomID).
				Updates(map[string]interface{}{"status": "Cleaning"}).Error; err != nil {
				return err
			}
		}

		return nil
	})
}
