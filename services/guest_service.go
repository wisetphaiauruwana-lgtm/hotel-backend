package services

import (
	"log"

	"hotel-backend/models"
	"gorm.io/gorm"
)

type GuestService struct {
	DB *gorm.DB
}

func NewGuestService(db *gorm.DB) *GuestService {
	return &GuestService{DB: db}
}

// ----------------------------------------------------
// CREATE — ต้องรับ pointer เพื่อให้ ID ถูกเติมกลับมาที่ตัวแปรจริง
// ----------------------------------------------------
func (s *GuestService) Create(guest *models.Guest) error {
	log.Printf("➡️ GuestService.Create incoming: %+v", guest)

	// ตรวจสอบหรือตั้งค่าอีเมล
	if guest.Email == "" {
		// ถ้าไม่มีอีเมลใน Guest ให้ตั้งค่าสมมติหรือล็อกแจ้งเตือน
		log.Println("⚠️ Guest does not have an email.")
	}

	err := s.DB.Create(guest).Error

	log.Printf("⬅️ GuestService.Create result: %+v (err: %v)", guest, err)
	return err
}

// ----------------------------------------------------
// ✅ GetAll (Admin view)
// - preload booking/room
// - เติม RoomNumber
// ----------------------------------------------------
func (s *GuestService) GetAll() ([]models.Guest, error) {
	log.Println("➡️ GuestService.GetAll")

	var guests []models.Guest

	// NOTE: ใส่ Order ให้ consistent กับ /guests/all ที่คุณอยากให้เรียงล่าสุดก่อน
	err := s.DB.
		Preload("Booking.Room").
		Preload("Booking.Rooms.Room").
		Where("guests.deleted_at IS NULL").
		Order("guests.id DESC").
		Find(&guests).Error

	if err != nil {
		log.Printf("⬅️ GuestService.GetAll error: %v", err)
		return nil, err
	}

	// เติม roomNumber ให้ guest (ใช้เฉพาะ admin view)
	for i := range guests {

		// booking.rooms (หลายห้อง)
		if len(guests[i].Booking.Rooms) > 0 {
			r := guests[i].Booking.Rooms[0].Room
			if r.RoomCode != "" {
				guests[i].RoomNumber = r.RoomCode
			} else {
				guests[i].RoomNumber = r.RoomNumber
			}
			continue
		}

		// booking.room (ห้องเดียว)
		if guests[i].Booking.Room.ID != 0 {
			r := guests[i].Booking.Room
			if r.RoomCode != "" {
				guests[i].RoomNumber = r.RoomCode
			} else {
				guests[i].RoomNumber = r.RoomNumber
			}
		}
	}

	log.Printf("⬅️ GuestService.GetAll ok: %d guests", len(guests))
	return guests, nil
}

// ----------------------------------------------------
// ✅ GetAllRaw (Admin view - แบบเบา/กัน preload พัง)
// ใช้กรณีคุณไม่ต้องการ preload ความสัมพันธ์
// ----------------------------------------------------
func (s *GuestService) GetAllRaw() ([]models.Guest, error) {
	log.Println("➡️ GuestService.GetAllRaw")

	var guests []models.Guest
	err := s.DB.
		Where("deleted_at IS NULL").
		Order("id DESC").
		Find(&guests).Error

	if err != nil {
		log.Printf("⬅️ GuestService.GetAllRaw error: %v", err)
		return nil, err
	}

	log.Printf("⬅️ GuestService.GetAllRaw ok: %d guests", len(guests))
	return guests, nil
}

// ----------------------------------------------------
// GET BY ID
// ----------------------------------------------------
func (s *GuestService) GetByID(id uint) (*models.Guest, error) {
	log.Printf("➡️ GuestService.GetByID id=%d", id)

	var guest models.Guest
	if err := s.DB.Where("deleted_at IS NULL").First(&guest, id).Error; err != nil {
		log.Printf("⬅️ GuestService.GetByID error: %v", err)
		return nil, err
	}

	log.Printf("⬅️ GuestService.GetByID ok: guest_id=%d", guest.ID)
	return &guest, nil
}

// ----------------------------------------------------
// UPDATE
// ----------------------------------------------------
func (s *GuestService) Update(guest *models.Guest) error {
	log.Printf("➡️ GuestService.Update id=%d", guest.ID)

	// ตรวจสอบหรือตั้งค่าอีเมล
	if guest.Email == "" {
		// ถ้าไม่มีอีเมลใน Guest ให้ตั้งค่าสมมติหรือล็อกแจ้งเตือน
		log.Println("⚠️ Guest does not have an email.")
	}

	err := s.DB.Model(&models.Guest{}).
		Where("id = ?", guest.ID).
		Updates(guest).Error

	log.Printf("⬅️ GuestService.Update err=%v", err)
	return err
}
// ----------------------------------------------------
// 🚫 DELETE — ไม่อนุญาตให้ลบ Guest
// ----------------------------------------------------
func (s *GuestService) Delete(id uint) error {
	log.Printf("⚠️ GuestService.Delete blocked id=%d", id)
	return nil
}

// ----------------------------------------------------
// ✅ IMPORTANT: GET GUESTS BY BOOKING ID (ตัวที่ต้องใช้จริง)
// ใช้ใน flow:
// EnterCode → Checkin → PostCheckinDetails → viewGuests
// ----------------------------------------------------
func (s *GuestService) GetByBookingID(bookingID uint) ([]models.Guest, error) {
	log.Printf("➡️ GuestService.GetByBookingID bookingID=%d", bookingID)

	var guests []models.Guest

	err := s.DB.
		Where("booking_id = ? AND deleted_at IS NULL", bookingID).
		Order("is_main_guest DESC, id ASC").
		Find(&guests).Error

	if err != nil {
		log.Printf("⬅️ GuestService.GetByBookingID error: %v", err)
		return nil, err
	}

	log.Printf("⬅️ GuestService.GetByBookingID ok: %d guests", len(guests))
	return guests, nil
}

// ----------------------------------------------------
// ✅ GetByBookingIDRaw (แบบเบา เผื่ออยากแยกจาก preload logic)
// ----------------------------------------------------
func (s *GuestService) GetByBookingIDRaw(bookingID uint) ([]models.Guest, error) {
	log.Printf("➡️ GuestService.GetByBookingIDRaw bookingID=%d", bookingID)

	var guests []models.Guest
	err := s.DB.
		Where("booking_id = ? AND deleted_at IS NULL", bookingID).
		Order("is_main_guest DESC, id ASC").
		Find(&guests).Error

	if err != nil {
		log.Printf("⬅️ GuestService.GetByBookingIDRaw error: %v", err)
		return nil, err
	}

	log.Printf("⬅️ GuestService.GetByBookingIDRaw ok: %d guests", len(guests))
	return guests, nil
}
