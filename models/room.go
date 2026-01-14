package models

import (
	"gorm.io/gorm"
)

type Room struct {
	gorm.Model

	// Make RoomTypeID nullable so when frontend doesn't provide a valid FK, DB won't try to insert 0.
	// Use json tag matching frontend (camelCase) but keep gorm column mapping to existing DB column.
	RoomTypeID *uint `json:"RoomTypeID,omitempty" gorm:"column:room_type_id"`
	RoomNumber string `json:"roomNumber" gorm:"column:room_number;uniqueIndex;type:varchar(50)"`
	RoomCode   string `json:"roomCode"   gorm:"column:room_code;type:varchar(50)"`
	AccessCode string `json:"accessCode" gorm:"column:access_code;type:varchar(6)"`

	Type         string  `json:"type"`
	Status       string  `json:"status"`
	Floor        string  `json:"floor" gorm:"type:varchar(10)"`
	Price        float64 `json:"price"`
	MaxOccupancy int     `json:"maxOccupancy" gorm:"column:max_occupancy"`
	Description  string  `json:"description" gorm:"type:text"`

	RoomType RoomType `gorm:"foreignKey:RoomTypeID"`
}
