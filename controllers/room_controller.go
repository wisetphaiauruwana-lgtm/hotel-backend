package controllers

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"

	"hotel-backend/config"
	"hotel-backend/models"

	"github.com/gin-gonic/gin"
)

func generateUniqueAccessCode() (string, error) {
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


// ----------------------------------------------------
// 1. Get Rooms (GET /api/rooms)
// ----------------------------------------------------

func GetRooms(c *gin.Context) {
	var rooms []models.Room
	config.DB.Preload("RoomType").Find(&rooms)

	c.JSON(http.StatusOK, rooms)
}


// ----------------------------------------------------
// 2. Create Room (POST /api/rooms)
// ----------------------------------------------------

func CreateRoom(c *gin.Context) {
    var room models.Room

    if err := c.ShouldBindJSON(&room); err != nil {
        log.Printf("❌ JSON BINDING ERROR (400): %v", err)
        c.JSON(http.StatusBadRequest, gin.H{
            "status":  "error",
            "message": "Invalid request payload",
            "details": err.Error(),
        })
        return
    }

    // Normalize / trim room number (frontend sends roomNumber)
    room.RoomNumber = strings.TrimSpace(room.RoomNumber)
    if room.RoomNumber == "" {
        log.Println("❌ RoomNumber cannot be empty.")
        c.JSON(http.StatusBadRequest, gin.H{
            "status":  "error",
            "message": "Room Number is required.",
        })
        return
    }

    // If RoomTypeID pointer exists but is zero or invalid -> set to nil to avoid FK=0 insertion
   if room.RoomTypeID != nil {
    var rt models.RoomType
    err := config.DB.
        Where("id = ?", *room.RoomTypeID).
        First(&rt).Error

    if err != nil {
        log.Printf("❌ Invalid RoomTypeID provided: %v", *room.RoomTypeID)
        c.JSON(http.StatusBadRequest, gin.H{
            "status": "error",
            "message": "Invalid roomTypeID provided.",
        })
        return
    }
}

	if strings.TrimSpace(room.AccessCode) == "" {
		code, err := generateUniqueAccessCode()
		if err != nil {
			log.Printf("❌ Access code generation error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"status":  "error",
				"message": "Failed to generate access code",
			})
			return
		}
		room.AccessCode = code
	}

    // Save
    if result := config.DB.Create(&room); result.Error != nil {
        // Check duplicate room_number (unique index)
        if strings.Contains(result.Error.Error(), "Duplicate entry") || strings.Contains(result.Error.Error(), "UNIQUE constraint failed") {
            log.Printf("❌ Duplicate Room Number: %s", room.RoomNumber)
            c.JSON(http.StatusConflict, gin.H{
                "status":  "error",
                "message": fmt.Sprintf("Room Number '%s' already exists.", room.RoomNumber),
            })
            return
        }

        log.Printf("❌ DB ERROR: %v", result.Error)
        c.JSON(http.StatusInternalServerError, gin.H{
            "status":  "error",
            "message": "Database error",
            "details": result.Error.Error(),
        })
        return
    }

    c.JSON(http.StatusCreated, room)
}


// ----------------------------------------------------
// 3. Update Room (PATCH /api/rooms/:id)
// ----------------------------------------------------

func UpdateRoom(c *gin.Context) {
	id := c.Param("id")
	var updateData map[string]interface{}

	// Bind JSON
	if err := c.ShouldBindJSON(&updateData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "Invalid request payload",
			"details": err.Error(),
		})
		return
	}

	// ป้องกันแก้ไขฟิลด์สำคัญ
	delete(updateData, "id")
	delete(updateData, "created_at")
	delete(updateData, "updated_at")
	delete(updateData, "deleted_at")

	if v, ok := updateData["accessCode"]; ok {
		updateData["access_code"] = v
		delete(updateData, "accessCode")
	}

	var existing models.Room
	if err := config.DB.First(&existing, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"status":  "error",
			"message": "Room not found",
		})
		return
	}

	if statusRaw, ok := updateData["status"]; ok {
		statusStr := fmt.Sprintf("%v", statusRaw)
		if strings.EqualFold(existing.Status, "Cleaning") && strings.EqualFold(statusStr, "Available") {
			code, err := generateUniqueAccessCode()
			if err != nil {
				log.Printf("❌ Access code generation error: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{
					"status":  "error",
					"message": "Failed to generate access code",
				})
				return
			}
			updateData["access_code"] = code
		}
	}

	// Update DB
	if err := config.DB.Model(&models.Room{}).Where("id = ?", id).Updates(updateData).Error; err != nil {
		log.Printf("❌ Update Error for Room %s: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": "Update failed",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Room updated successfully",
	})
}


// ----------------------------------------------------
// 4. Delete Room (DELETE /api/rooms/:id)
// ----------------------------------------------------

func DeleteRoom(c *gin.Context) {
	id := c.Param("id")

	log.Printf("DEBUG DELETE: Room ID to delete: '%s'", id)

	result := config.DB.Where("id = ?", id).Delete(&models.Room{})

	if result.Error != nil {
		log.Printf("❌ DB Error during deletion (ID: %s): %v", id, result.Error)
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": "Failed to delete room.",
		})
		return
	}

	if result.RowsAffected == 0 {
		log.Printf("⚠️ No room found with ID: %s", id)
		c.JSON(http.StatusNotFound, gin.H{
			"status":  "error",
			"message": fmt.Sprintf("Room with ID %s not found.", id),
		})
		return
	}

	log.Printf("✅ Room ID %s deleted.", id)

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Room deleted successfully",
	})
}
