package controllers

import (
	"net/http"
	"strconv"
	"strings"

	"hotel-backend/config"
	"hotel-backend/models"

	"github.com/gin-gonic/gin"

	"gorm.io/gorm"
)

type rolePermissionsPayload struct {
	Permissions []string `json:"permissions"`
}

type roleMemberResponse struct {
	ID    uint   `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}


var defaultActionsByModule = map[string][]string{
	"bookingManagement":   {"view", "create", "edit", "delete"},
	"roomManagement":      {"view", "create", "edit", "delete", "editStatus"},
	"customerList":        {"view", "create", "edit", "delete", "export"},
	"tm30Verification":    {"view", "submit", "verify", "export"},
	"rolesAndPermissions": {"view", "create", "edit", "delete"},
}

func buildDefaultPermissions() map[string]map[string]bool {
	permMap := map[string]map[string]bool{}
	for module, actions := range defaultActionsByModule {
		permMap[module] = map[string]bool{}
		for _, action := range actions {
			permMap[module][action] = false
		}
	}
	return permMap
}

type roleResponse struct {
	ID          uint                          `json:"id"`
	Name        string                        `json:"name"`
	Description string                        `json:"description"`
	Permissions map[string]map[string]bool    `json:"permissions"`
	Members     []roleMemberResponse          `json:"members"`
}

func GetRoles(c *gin.Context) {
	var roles []models.Role
	if err := config.DB.Preload("Permissions").Preload("Members").Find(&roles).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	responses := make([]roleResponse, 0, len(roles))
	for _, role := range roles {
		permMap := buildDefaultPermissions()
		for _, perm := range role.Permissions {
			parts := strings.Split(perm.Permission, ".")
			if len(parts) != 2 {
				continue
			}
			module := parts[0]
			action := parts[1]
			if _, ok := permMap[module]; !ok {
				permMap[module] = map[string]bool{}
			}
			permMap[module][action] = true
		}

		members := make([]roleMemberResponse, 0, len(role.Members))
		for _, admin := range role.Members {
			members = append(members, roleMemberResponse{
				ID:    admin.ID,
				Name:  admin.FullName,
				Email: admin.Username,
			})
		}

		responses = append(responses, roleResponse{
			ID:          role.ID,
			Name:        role.Name,
			Description: role.Description,
			Permissions: permMap,
			Members:     members,
		})
	}

	c.JSON(http.StatusOK, responses)
}

func UpdateRolePermissions(c *gin.Context) {
	var payload rolePermissionsPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var role models.Role
	idStr := strings.TrimSpace(c.Param("id"))
	roleID, err := strconv.ParseUint(idStr, 10, 64)
	if err == nil && roleID > 0 {
		if err := config.DB.First(&role, roleID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "role not found"})
			return
		}
	} else {
		if err := config.DB.Where("name = ?", idStr).First(&role).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "role not found"})
			return
		}
		roleID = uint64(role.ID)
	}

	if roleID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid role id"})
		return
	}

	if role.ID == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "role not found"})
		return
	}


	if err := config.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("role_id = ?", roleID).Delete(&models.RolePermission{}).Error; err != nil {
			return err
		}

		if len(payload.Permissions) > 0 {
			perms := make([]models.RolePermission, 0, len(payload.Permissions))
			for _, p := range payload.Permissions {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				perms = append(perms, models.RolePermission{
					RoleID:     uint(roleID),
					Permission: p,
				})
			}
			if len(perms) > 0 {
				if err := tx.Create(&perms).Error; err != nil {
					return err
				}
			}
		}

		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "permissions updated"})
}
