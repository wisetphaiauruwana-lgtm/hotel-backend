package controllers

import (
	"net/url"
	"fmt"
	"net/http"
	"strings"
	"time"
	"regexp"
	"strconv"

	"hotel-backend/config"
	"hotel-backend/models"
	"hotel-backend/utils"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type createAdminPayload struct {
	FullName string `json:"full_name"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type inviteAdminPayload struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Role  string `json:"role"`
	FromEmail string `json:"from_email"`
}

type activateAdminPayload struct {
	Email    string `json:"email"`
	Token    string `json:"token"`
	Password string `json:"password"`
}

type updateAdminRolePayload struct {
	Role string `json:"role"`
}

var inviteEmailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

func normalizeInviteRole(raw string) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(raw))
	switch name {
	case "owner":
		return "owner", true
	case "manager":
		return "Manager", true
	case "receptionist":
		return "Receptionist", true
	case "cleaner":
		return "Cleaner", true
	default:
		return "", false
	}
}

func GetAdmins(c *gin.Context) {
	var admins []models.Admin
	if err := config.DB.Find(&admins).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, admins)
}

func CreateAdmin(c *gin.Context) {
	var payload createAdminPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	admin := models.Admin{
		FullName: payload.FullName,
		Username: payload.Username,
		Password: payload.Password,
	}

	if admin.Password != "" && !(strings.HasPrefix(admin.Password, "$2a$") || strings.HasPrefix(admin.Password, "$2b$") || strings.HasPrefix(admin.Password, "$2y$")) {
		if hash, err := bcrypt.GenerateFromPassword([]byte(admin.Password), bcrypt.DefaultCost); err == nil {
			admin.Password = string(hash)
		}
	}

	if err := config.DB.Create(&admin).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, admin)
}

func InviteAdmin(c *gin.Context) {
	var payload inviteAdminPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	name := strings.TrimSpace(payload.Name)
	email := strings.TrimSpace(payload.Email)
	roleName, ok := normalizeInviteRole(payload.Role)
	if name == "" || email == "" || !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name, email, and role are required"})
		return
	}
	if !inviteEmailRegex.MatchString(strings.ToLower(email)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email"})
		return
	}

	token, err := utils.GenerateSecureToken(24)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}
	expiry := time.Now().Add(24 * time.Hour)

	var admin models.Admin
	exists := false
	if err := config.DB.Unscoped().Where("username = ?", email).First(&admin).Error; err == nil {
		exists = true
		if !admin.DeletedAt.Valid {
			c.JSON(http.StatusConflict, gin.H{"error": "email already exists"})
			return
		}
	} else if err != nil && err != gorm.ErrRecordNotFound {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if exists {
		if err := config.DB.Unscoped().Model(&admin).Updates(map[string]any{
			"full_name":           name,
			"reset_token":         token,
			"reset_token_expires": expiry,
			"deleted_at":          nil,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	} else {
		admin = models.Admin{
			FullName:          name,
			Username:          email,
			ResetToken:        &token,
			ResetTokenExpires: &expiry,
		}

		if err := config.DB.Create(&admin).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	var role models.Role
	if err := config.DB.Where("name = ?", roleName).First(&role).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			role = models.Role{
				Name: roleName,
				Description: "",
			}
			if err := config.DB.Create(&role).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	if err := config.DB.Unscoped().Where("admin_id = ?", admin.ID).Delete(&models.RoleMember{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := config.DB.Create(&models.RoleMember{RoleID: role.ID, AdminID: admin.ID}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	adminFrontendURL := utils.EnvOrDefault("FRONTEND_ADMIN_URL", "")
	if adminFrontendURL == "" {
		adminFrontendURL = utils.EnvOrDefault("FRONTEND_URL", "http://localhost:3000")
	}
	adminFrontendURL = strings.TrimRight(adminFrontendURL, "/")
	inviteLink := fmt.Sprintf("%s/#/setup-account?token=%s&email=%s", adminFrontendURL, token, url.QueryEscape(email))

	fromEmail := strings.TrimSpace(payload.FromEmail)
	if err := utils.SendAdminInviteEmail(email, inviteLink, name, roleName, fromEmail); err != nil {
		_ = config.DB.Unscoped().Where("admin_id = ?", admin.ID).Delete(&models.RoleMember{}).Error
		if exists {
			_ = config.DB.Unscoped().Model(&admin).Update("deleted_at", time.Now()).Error
		} else {
			_ = config.DB.Unscoped().Delete(&admin).Error
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "email send failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id": admin.ID,
		"name": admin.FullName,
		"email": admin.Username,
		"role": roleName,
		"status": "Pending Invite",
		"inviteSentAt": time.Now().UTC(),
	})
}

func ActivateAdmin(c *gin.Context) {
	var payload activateAdminPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	email := strings.TrimSpace(payload.Email)
	token := strings.TrimSpace(payload.Token)
	password := strings.TrimSpace(payload.Password)
	if email == "" || token == "" || password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email, token, and password are required"})
		return
	}
	if len(password) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "password must be at least 8 characters"})
		return
	}

	var admin models.Admin
	if err := config.DB.Unscoped().Where("username = ? AND reset_token = ?", email, token).First(&admin).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "invalid token"})
		return
	}

	if admin.ResetTokenExpires != nil && time.Now().After(*admin.ResetTokenExpires) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token expired"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	updates := map[string]any{
		"password":            string(hash),
		"reset_token":         nil,
		"reset_token_expires": nil,
	}

	if admin.DeletedAt.Valid {
		updates["deleted_at"] = nil
	}

	if err := config.DB.Unscoped().Model(&admin).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to activate account"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "account activated"})
}

func DeleteAdmin(c *gin.Context) {
	id := c.Param("id")
	config.DB.Delete(&models.Admin{}, id)
	c.JSON(http.StatusOK, gin.H{"message": "Admin deleted"})
}

// Update admin role (Owner can change staff role)
func UpdateAdminRole(c *gin.Context) {
	idStr := strings.TrimSpace(c.Param("id"))
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid admin id"})
		return
	}

	var payload updateAdminRolePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	roleName, ok := normalizeInviteRole(payload.Role)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid role"})
		return
	}

	var admin models.Admin
	if err := config.DB.First(&admin, uint(id)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "admin not found"})
		return
	}

	var role models.Role
	if err := config.DB.Where("name = ?", roleName).First(&role).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			role = models.Role{
				Name:        roleName,
				Description: "",
			}
			if err := config.DB.Create(&role).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	if err := config.DB.Unscoped().Where("admin_id = ?", admin.ID).Delete(&models.RoleMember{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := config.DB.Create(&models.RoleMember{RoleID: role.ID, AdminID: admin.ID}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":    admin.ID,
		"email": admin.Username,
		"role":  roleName,
	})
}
