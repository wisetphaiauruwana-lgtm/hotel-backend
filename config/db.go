package config

import (
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"hotel-backend/models"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"golang.org/x/crypto/bcrypt"
)

var DB *gorm.DB

func mustParseTime(layout, value string) time.Time {
	t, err := time.Parse(layout, value)
	if err != nil {
		log.Fatalf("Error parsing time for seeding (%s): %v", value, err)
		panic(err)
	}
	return t
}

// SeedDatabase unchanged...
func SeedDatabase() {
	// ---------------- Admins ----------------
	var adminCount int64
	DB.Model(&models.Admin{}).Count(&adminCount)
	if adminCount == 0 {
		hash, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.DefaultCost)
		if err != nil {
			log.Printf("warning: failed to hash default admin password: %v", err)
		} else {
			admin := models.Admin{
				FullName: "Admin User",
				Username: "wisetphaiauruwana@gmail.com",
				Password: string(hash),
			}
			if err := DB.Create(&admin).Error; err != nil {
				log.Printf("warning: failed to create default admin: %v", err)
			} else {
				log.Println("Default admin seeded")
			}
		}
	}

	// ---------------- RoomTypes ----------------
	var rtCount int64
	DB.Model(&models.RoomType{}).Count(&rtCount)

	if rtCount == 0 {
		roomTypes := []models.RoomType{
			{TypeName: "Standard", Description: "Standard Room", MaxGuests: 2},
			{TypeName: "Superior", Description: "Superior Room", MaxGuests: 3},
			{TypeName: "Deluxe", Description: "Deluxe Room", MaxGuests: 4},
			{TypeName: "Connecting", Description: "Connecting Room", MaxGuests: 5},
		}
		DB.Create(&roomTypes)
		log.Println("RoomTypes seeded")
	}

	// ---------------- Consents ----------------
	var count int64
	DB.Model(&models.Consent{}).Count(&count)

	if count > 0 {
		log.Println("Consents already seeded")
	} else {
		now := time.Now()

		consents := []models.Consent{
			{
				Slug:          "checkin-general",
				Title:         "General Check-in Consent",
				Description:   "Consent for hotel check-in process",
				EffectiveFrom: &now,
				Version:       "1.0",
			},
		}

		if err := DB.Create(&consents).Error; err != nil {
			log.Fatalf("Failed to seed Consents: %v", err)
		}

		log.Println("Consents seeded successfully")
	}

	// ---------------- Roles ----------------
	desiredRoles := []models.Role{
		{Name: "owner", Description: "System owner with full access"},
		{Name: "Manager", Description: "Manager with elevated access"},
		{Name: "Receptionist", Description: "Front desk operations"},
		{Name: "Cleaner", Description: "Housekeeping access"},
	}

	allPerms := []string{
		"bookingManagement.view",
		"bookingManagement.create",
		"bookingManagement.edit",
		"bookingManagement.delete",
		"roomManagement.view",
		"roomManagement.create",
		"roomManagement.edit",
		"roomManagement.delete",
		"roomManagement.editStatus",
		"customerList.view",
		"customerList.create",
		"customerList.edit",
		"customerList.delete",
		"customerList.export",
		"tm30Verification.view",
		"tm30Verification.submit",
		"tm30Verification.verify",
		"rolesAndPermissions.view",
		"rolesAndPermissions.create",
		"rolesAndPermissions.edit",
		"rolesAndPermissions.delete",
	}

	rolesByKey := map[string]models.Role{}
	for i := range desiredRoles {
		role := desiredRoles[i]
		key := strings.ToLower(role.Name)

		var existing models.Role
		err := DB.Where("LOWER(name) = ?", key).First(&existing).Error
		if err == nil && existing.ID != 0 {
			rolesByKey[key] = existing
			if existing.Name != role.Name || existing.Description != role.Description {
				if err := DB.Model(&existing).Updates(models.Role{
					Name:        role.Name,
					Description: role.Description,
				}).Error; err != nil {
					log.Printf("warning: failed to update role %s: %v", role.Name, err)
				}
			}
			continue
		}

		if err := DB.Create(&role).Error; err != nil {
			log.Printf("warning: failed to create role %s: %v", role.Name, err)
			continue
		}
		rolesByKey[key] = role
	}

	ownerRole, ok := rolesByKey["owner"]
	if ok && ownerRole.ID != 0 {
		var permCount int64
		DB.Model(&models.RolePermission{}).Where("role_id = ?", ownerRole.ID).Count(&permCount)
		if permCount == 0 {
			perms := make([]models.RolePermission, 0, len(allPerms))
			for _, p := range allPerms {
				perms = append(perms, models.RolePermission{RoleID: ownerRole.ID, Permission: p})
			}
			if len(perms) > 0 {
				if err := DB.Create(&perms).Error; err != nil {
					log.Printf("warning: failed to create owner permissions: %v", err)
				}
			}
		}

		var memberCount int64
		DB.Model(&models.RoleMember{}).Where("role_id = ?", ownerRole.ID).Count(&memberCount)
		if memberCount == 0 {
			var admins []models.Admin
			DB.Find(&admins)
			if len(admins) > 0 {
				members := make([]models.RoleMember, 0, len(admins))
				for _, admin := range admins {
					members = append(members, models.RoleMember{RoleID: ownerRole.ID, AdminID: admin.ID})
				}
				if err := DB.Create(&members).Error; err != nil {
					log.Printf("warning: failed to assign admins to owner role: %v", err)
				}
			}
		}
	}

	// Ensure default admin stays in owner role
	if ownerRole, ok := rolesByKey["owner"]; ok && ownerRole.ID != 0 {
		var admin models.Admin
		if err := DB.Unscoped().Where("username = ?", "wisetphaiauruwana@gmail.com").First(&admin).Error; err == nil && admin.ID != 0 {
			if err := DB.Where("admin_id = ?", admin.ID).Delete(&models.RoleMember{}).Error; err != nil {
				log.Printf("warning: failed to clear roles for wisetphaiauruwana@gmail.com: %v", err)
			}
			if err := DB.Create(&models.RoleMember{RoleID: ownerRole.ID, AdminID: admin.ID}).Error; err != nil {
				log.Printf("warning: failed to assign owner role to wisetphaiauruwana@gmail.com: %v", err)
			}
		}
	}

	log.Println("Roles ensured")
}

func envOrDefault(key, def string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return def
	}
	return value
}

func mysqlDSNFromURL(raw string) (string, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}

	user := u.User.Username()
	pass, _ := u.User.Password()
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "3306"
	}

	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		return "", "", fmt.Errorf("mysql url missing database name")
	}

	q := u.Query()
	if q.Get("charset") == "" {
		q.Set("charset", "utf8mb4")
	}
	if q.Get("parseTime") == "" {
		q.Set("parseTime", "True")
	}
	if q.Get("loc") == "" {
		q.Set("loc", "Local")
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?%s", user, pass, host, port, dbName, q.Encode())
	return dsn, dbName, nil
}

func resolveMySQLDSN() (string, string, error) {
	raw := strings.TrimSpace(os.Getenv("MYSQL_URL"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}

	if raw != "" {
		if strings.HasPrefix(raw, "mysql://") {
			return mysqlDSNFromURL(raw)
		}
		return raw, strings.TrimSpace(os.Getenv("DB_NAME")), nil
	}

	user := envOrDefault("DB_USER", "root")
	pass := envOrDefault("DB_PASS", "Wisetphaiauruwana")
	host := envOrDefault("DB_HOST", "127.0.0.1")
	port := envOrDefault("DB_PORT", "3306")
	dbName := envOrDefault("DB_NAME", "hotel_db")

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		user, pass, host, port, dbName,
	)
	return dsn, dbName, nil
}

// helper: drop wrong FK constraints that reference consent_logs FROM consents table
func dropWrongConsentFKIfAny(sqlDB *sql.DB, dbName string) {
	rows, err := sqlDB.Query(`
SELECT CONSTRAINT_NAME, TABLE_NAME, REFERENCED_TABLE_NAME
FROM information_schema.KEY_COLUMN_USAGE
WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'consents' AND REFERENCED_TABLE_NAME = 'consent_logs';
`, dbName)
	if err != nil {
		// not fatal: maybe information_schema access not allowed
		log.Printf("info: error querying information_schema for wrong FK: %v", err)
		return
	}
	defer rows.Close()

	var constraintName, tableName, refTable string
	for rows.Next() {
		if err := rows.Scan(&constraintName, &tableName, &refTable); err != nil {
			log.Printf("info: scan error: %v", err)
			continue
		}
		// drop it
		stmt := fmt.Sprintf("ALTER TABLE `%s` DROP FOREIGN KEY `%s`;", tableName, constraintName)
		if _, err := sqlDB.Exec(stmt); err != nil {
			log.Printf("warning: failed to drop FK %s on table %s: %v", constraintName, tableName, err)
		} else {
			log.Printf("info: dropped wrong FK %s on %s", constraintName, tableName)
		}
	}
}

func ConnectDatabase() error {
	dsn, dbName, err := resolveMySQLDSN()
	if err != nil {
		return err
	}

	newLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold: time.Second,
			LogLevel:      logger.Info,
			Colorful:      true,
		},
	)

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: newLogger})
	if err != nil {
		return err
	}

	// If possible, get sql.DB to run raw queries for schema fixes
	sqlDB, err := db.DB()
	if err == nil {
		if dbName != "" {
			// attempt to drop wrong FK constraints that point consents -> consent_logs
			dropWrongConsentFKIfAny(sqlDB, dbName)
		}
	} else {
		log.Printf("info: cannot get raw sql.DB: %v", err)
	}

	DB = db

	// AutoMigrate in correct parent->child order
	if err := DB.AutoMigrate(
		&models.Admin{},
		&models.HotelSetting{},
		&models.Role{},
		&models.RolePermission{},
		&models.RoleMember{},
		&models.RoomType{},
		&models.Customer{},
		&models.Room{},
		&models.Booking{},
		&models.BookingInfo{},
		&models.Guest{},
		&models.Consent{},    // parent (consents)
		&models.ConsentLog{}, // child (consent_logs)
		&models.BookingRoom{},
	); err != nil {
		return err
	}

	SeedDatabase()
	return nil
}
