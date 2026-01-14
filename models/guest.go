package models

import (
	"time"

	"gorm.io/gorm"
)

type Guest struct {
    ID uint `gorm:"primaryKey;autoIncrement" json:"id"`

    CreatedAt time.Time `json:"createdAt"`
    UpdatedAt time.Time `json:"updatedAt"`
    DeletedAt gorm.DeletedAt `gorm:"index" json:"deletedAt"`

    BookingID *uint `gorm:"index;column:booking_id" json:"booking_id"`

    // 🔹 โหลด Booking พร้อม Room ได้
    Booking Booking `gorm:"foreignKey:BookingID" json:"-"`

    // 🔹 ใช้ส่งค่าไป frontend (ไม่บันทึก DB)
    RoomNumber string `gorm:"-" json:"roomNumber"`

    FullName string `json:"fullName"`

    IsMainGuest bool       `json:"isMainGuest"`
    DateOfBirth *time.Time `json:"dateOfBirth"`

    Gender         string `json:"gender"`
    Nationality    string `json:"nationality"`
    CurrentAddress string `json:"currentAddress"`

    IDType          string `json:"idType"`
    IDNumber        string `json:"idNumber"`
    IDIssuedCountry string `json:"idIssuedCountry"`

    FaceImagePath     string `json:"faceImagePath"`
    DocumentImagePath string `json:"documentImagePath"`

    // เพิ่มฟิลด์นี้เพื่อเก็บอีเมล
    Email string `json:"email"`
}
