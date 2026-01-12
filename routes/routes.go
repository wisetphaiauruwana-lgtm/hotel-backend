package routes

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"hotel-backend/controllers"
)

func parseCorsOrigins() []string {
	raw := strings.TrimSpace(os.Getenv("CORS_ORIGINS"))
	if raw == "" {
		return []string{"*"}
	}

	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		if origin != "" {
			origins = append(origins, origin)
		}
	}
	if len(origins) == 0 {
		return []string{"*"}
	}
	return origins
}

// SetupRouter รับ Controller Instances เข้ามาเพื่อกำหนด Route
func SetupRouter(
	gc *controllers.GuestController,
	bc *controllers.BookingController,
	bic *controllers.BookingInfoController,
	ctc *controllers.CustomerController,
	apiKey string,
) *gin.Engine {
	r := gin.Default()
	r.Static("/uploads", "./uploads")

	origins := parseCorsOrigins()
	allowCredentials := true
	for _, origin := range origins {
		if origin == "*" {
			allowCredentials = false
			break
		}
	}

	r.Use(cors.New(cors.Config{
		AllowOrigins:     origins,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Requested-With"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: allowCredentials,
		MaxAge:           12 * time.Hour,
	}))

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	api := r.Group("/api")
	{
		guests := api.Group("/guests")
		{
			guests.GET("", gc.GetGuests)

			// ? ต้องอยู่ก่อน /:id
			guests.GET("/all", gc.GetAllGuests)

			// ? รับเฉพาะตัวเลข ป้องกัน all/xyz ไปชน handler นี้
			guests.GET("/:id", gc.GetGuestByID)
			guests.POST("", gc.CreateGuest)
			guests.PUT("/:id", gc.UpdateGuest)
			guests.DELETE("/:id", gc.DeleteGuest)
		}

		// Customers
		customersRoutes := api.Group("/customers")
		{
			customersRoutes.POST("", ctc.CreateCustomer)
		}

		// Bookings
		// Bookings
		bookings := api.Group("/bookings")
		{
			bookings.GET("", bc.GetBookings)
			bookings.POST("", bc.CreateBooking)

			// ? เพิ่มบรรทัดนี้ (ต้องมี)
			bookings.GET("/:id", bc.GetBookingDetails)

			bookings.DELETE("/:id", bc.DeleteBooking)
			bookings.POST("/:id/checkout", bc.CheckoutBooking)
			bookings.GET("/:id/guests", gc.GetGuestsByBookingID)
		}

		infoRoutes := api.Group("/booking-info")
		{
			infoRoutes.POST("", bic.SaveBookingInfo)
			infoRoutes.GET("/:id", bic.GetBookingInfoByID)
			infoRoutes.DELETE("/:id", bic.DeleteBookingInfo)
		}
		consents := api.Group("/consents")
		{
			consents.GET("", controllers.GetConsents)
			consents.POST("", controllers.CreateConsent)
			consents.POST("/accept", controllers.AcceptConsent) //  อันใหม่
			consents.DELETE("/:id", controllers.DeleteConsent)
		}
		consentLogs := api.Group("/consent-logs")
		{
			consentLogs.GET("", controllers.GetConsentLogs)
			consentLogs.POST("", controllers.CreateConsentLog)
			consentLogs.DELETE("/:id", controllers.DeleteConsentLog)
			consentLogs.PATCH("/attach-booking", controllers.AttachBookingToPending)
		}

		roles := api.Group("/roles")
		{
			roles.GET("", controllers.GetRoles)
			roles.PUT("/:id/permissions", controllers.UpdateRolePermissions)
		}

		settings := api.Group("/settings")
		{
			settings.GET("/hotel", controllers.GetHotelSettings)
			settings.PUT("/hotel", controllers.UpdateHotelSettings)
		}

		auth := api.Group("/auth")
		{
			auth.POST("/login", controllers.Login)
			auth.POST("/forgot", controllers.ForgotPassword)
		}

		admins := api.Group("/admins")
		{
			admins.GET("", controllers.GetAdmins)
			admins.POST("", controllers.CreateAdmin)
			admins.POST("/invite", controllers.InviteAdmin)
			admins.POST("/activate", controllers.ActivateAdmin)
			admins.DELETE("/:id", controllers.DeleteAdmin)
		}
		rooms := api.Group("/rooms")
		{
			rooms.GET("", controllers.GetRooms)
			rooms.POST("", controllers.CreateRoom)
			rooms.PATCH("/:id", controllers.UpdateRoom)
			rooms.PUT("/:id", controllers.UpdateRoom)
			rooms.DELETE("/:id", controllers.DeleteRoom)
		}
		roomTypes := api.Group("/room-types")
		{
			roomTypes.GET("", controllers.GetRoomTypes)
			roomTypes.POST("", controllers.CreateRoomType)
			roomTypes.DELETE("/:id", controllers.DeleteRoomType)
		}

		checkin := api.Group("/checkin")
		{
			checkin.POST("/initiate", bc.InitiateCheckIn)
			checkin.POST("", bc.ConfirmCheckIn)
			checkin.GET("/verify", bc.VerifyToken)
			checkin.POST("/validate", bic.ValidateCheckinCode)
			checkin.POST("/resend", bic.ResendCheckinCode)
		}

		api.POST("/verify/idcard", func(c *gin.Context) {
			gc.HandleIDCardVerification(c, apiKey)
		})
		api.POST("/verify/passport", func(c *gin.Context) {
			gc.HandlePassportVerification(c, apiKey)
		})

	}

	return r
}

