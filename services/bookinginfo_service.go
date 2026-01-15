package services

import (
	"errors"
	"strings"
	"time"

	"hotel-backend/models"
	"hotel-backend/utils"

	"gorm.io/gorm"
)

// BookingInfoService handles BookingInfo persistence and logic.
type BookingInfoService struct {
	DB *gorm.DB
}

// NewBookingInfoService constructor
func NewBookingInfoService(db *gorm.DB) *BookingInfoService {
	return &BookingInfoService{DB: db}
}

// SaveBookingInfo saves or updates a BookingInfo
func (s *BookingInfoService) SaveBookingInfo(info models.BookingInfo) error {
	return s.DB.Save(&info).Error
}

// GetByID returns a BookingInfo by id
func (s *BookingInfoService) GetByID(id uint) (models.BookingInfo, error) {
	var info models.BookingInfo
	err := s.DB.First(&info, id).Error
	return info, err
}

// GetInfoByToken returns BookingInfo by token
func (s *BookingInfoService) GetInfoByToken(token string) (models.BookingInfo, error) {
	var info models.BookingInfo
	err := s.DB.Where("token = ?", token).First(&info).Error
	return info, err
}

func (s *BookingInfoService) Delete(id uint) error {
	// ❌ ห้ามลบ booking_info
	return nil
}

// FindByCodeWithExpiry:
// - Try to find a non-expired booking_info first.
// - If not found, try to find any (including expired) so caller can tell "found but expired".
func (s *BookingInfoService) FindByCodeWithExpiry(code string, codeNoDash string) (models.BookingInfo, bool, error) {
	var bi models.BookingInfo
	now := time.Now().UTC()

	c := strings.ToUpper(strings.TrimSpace(code))
	cNo := strings.ToUpper(strings.TrimSpace(codeNoDash))

	// 1) non-expired match
	err := s.DB.
		Table("booking_infos").
		Select("booking_infos.*").
		Joins("JOIN bookings ON bookings.id = booking_infos.booking_id").
		Joins("JOIN customers ON customers.id = bookings.customer_id").
		Where("(booking_infos.checkin_code = ? OR booking_infos.checkin_code = ?)", c, cNo).
		Where("(booking_infos.code_expires_at IS NULL OR booking_infos.code_expires_at > ?)", now).
		Where("booking_infos.deleted_at IS NULL").
		Order("booking_infos.id").
		First(&bi).Error
	if err == nil {
		return bi, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return models.BookingInfo{}, false, err
	}

	// 2) find any (maybe expired)
	var any models.BookingInfo
	err2 := s.DB.
		Table("booking_infos").
		Select("booking_infos.*").
		Joins("JOIN bookings ON bookings.id = booking_infos.booking_id").
		Joins("JOIN customers ON customers.id = bookings.customer_id").
		Where("(booking_infos.checkin_code = ? OR booking_infos.checkin_code = ?)", c, cNo).
		Where("booking_infos.deleted_at IS NULL").
		Order("booking_infos.id").
		First(&any).Error
	if err2 == nil {
		return any, true, nil
	}
	if errors.Is(err2, gorm.ErrRecordNotFound) {
		return models.BookingInfo{}, false, gorm.ErrRecordNotFound
	}
	return models.BookingInfo{}, false, err2
}

// ExtendExpiry extends CodeExpiresAt by minutes and returns new expiry time.
func (s *BookingInfoService) ExtendExpiry(bookingInfoId uint, minutes int) (*time.Time, error) {
	var bi models.BookingInfo
	if err := s.DB.First(&bi, bookingInfoId).Error; err != nil {
		return nil, err
	}
	newExpiry := time.Now().UTC().Add(time.Duration(minutes) * time.Minute)
	bi.CodeExpiresAt = &newExpiry
	if err := s.DB.Save(&bi).Error; err != nil {
		return nil, err
	}
	return &newExpiry, nil
}

// InitiateCheckIn creates a BookingInfo record and sends a check-in email.
// Enhanced: if an active BookingInfo already exists, return it with error "checkin_already_initiated".
func (s *BookingInfoService) InitiateCheckIn(bookingID uint) (models.BookingInfo, error) {
	var booking models.Booking
	if err := s.DB.Preload("Customer").First(&booking, bookingID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.BookingInfo{}, errors.New("booking_not_found")
		}
		return models.BookingInfo{}, err
	}

	// If booking already checked in
	if strings.EqualFold(booking.Status, "Checked-In") || strings.EqualFold(booking.Status, "CHECKED-IN") {
		return models.BookingInfo{}, errors.New("already_checked_in")
	}

	// Look for existing active BookingInfo (not deleted and not expired)
	var existing models.BookingInfo
	now := time.Now().UTC()
	err := s.DB.
		Where("booking_id = ? AND deleted_at IS NULL", bookingID).
		Where("(code_expires_at IS NULL OR code_expires_at > ?) OR (expires_at IS NULL OR expires_at > ?)", now, now).
		Order("id desc").
		First(&existing).Error
	if err == nil {
		// found existing active session
		return existing, errors.New("checkin_already_initiated")
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return models.BookingInfo{}, err
	}

	// create new BookingInfo
	token, err := utils.GenerateSecureToken(32)
	if err != nil {
		return models.BookingInfo{}, err
	}
	checkinCode, err := utils.GenerateCheckinCode(8)
	if err != nil {
		return models.BookingInfo{}, err
	}

	bookingInfo := models.BookingInfo{
		BookingID:     bookingID,
		Token:         token,
		CheckinCode:   checkinCode,
		Status:        "INITIATED",
		GuestEmail:    booking.Customer.Email,
		GuestLastName: booking.Customer.FullName,
	}

	if err := s.DB.Create(&bookingInfo).Error; err != nil {
		return models.BookingInfo{}, err
	}

	// Build checkin link and send email (best-effort)
	frontendURL := utils.EnvOrDefault("FRONTEND_URL", "http://localhost:3000")
	bookingRef := booking.ReferenceCode
	guestEmail := booking.Customer.Email
	guestLastName := booking.Customer.FullName
	checkInStr := ""
	checkOutStr := ""
	checkinLink := utils.BuildCheckinLink(frontendURL, token, true)
	checkinLink = utils.AppendBookingIDParam(checkinLink, bookingID)

	err = utils.SendCheckInLinkEmail(
		guestEmail,
		bookingRef,
		checkinLink,
		guestLastName,
		[]utils.RoomInfo{},
		checkInStr,
		checkOutStr,
		bookingInfo.CheckinCode,
	)
	if err != nil {
		_ = s.DB.Model(&bookingInfo).Update("email_status", "FAILED").Error
		// return partial success with bookingInfo and sentinel error
		return bookingInfo, errors.New("email_send_failed")
	}

	_ = s.DB.Model(&bookingInfo).Update("email_status", "SENT").Error
	return bookingInfo, nil
}
