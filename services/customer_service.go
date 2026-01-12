package services

import (
	"gorm.io/gorm"
	"hotel-backend/models"
)

type CustomerService struct { 
    DB *gorm.DB
}

// NewCustomerService Constructor สำหรับ Dependency Injection
func NewCustomerService(db *gorm.DB) *CustomerService {
    return &CustomerService{DB: db}
}

// Create Customer Record (T0.1)
// รับ Pointer เพื่อให้ GORM อัปเดต Customer.ID กลับมา
func (s *CustomerService) Create(customer *models.Customer) error {
    return s.DB.Create(customer).Error 
}

// 💡 คุณสามารถเพิ่มเมธอดอื่น ๆ เช่น GetByID หรือ Update ได้ที่นี่