package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"hotel-backend/config"
	"hotel-backend/controllers"
	"hotel-backend/routes"
	"hotel-backend/services"
)

func main() {
	// Load .env (optional)
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️  .env not found or couldn't load it; continuing with environment variables")
	}

	// Required API key (keep behavior: fatal if missing)
	apiKey := os.Getenv("AIGEN_API_KEY")
	if apiKey == "" {
		log.Fatal("❌ ERROR: AIGEN_API_KEY environment variable is not set. Cannot initialize API Service.")
	}
	log.Println("✅ AIGEN_API_KEY detected.")

	// Connect database (config.ConnectDatabase should set config.DB)
	if err := config.ConnectDatabase(); err != nil {
		log.Fatalf("❌ Database connect failed: %v", err)
	}
	db := config.DB
	if db == nil {
		log.Fatal("❌ config.DB is nil after ConnectDatabase()")
	}
	log.Println("✅ Database connection established and migrations applied (if configured).")

	// Initialize services
	guestService := services.NewGuestService(db)
	customerService := services.NewCustomerService(db)
	bookingService := services.NewBookingService(db)
	bookingInfoService := services.NewBookingInfoService(db)

	// Initialize controllers
	guestController := controllers.NewGuestController(guestService)
	customerController := controllers.NewCustomerController(customerService)
	bookingController := controllers.NewBookingController(bookingService)
	bookingInfoController := controllers.NewBookingInfoController(bookingInfoService)

	// Build router
	router := routes.SetupRouter(guestController, bookingController, bookingInfoController, customerController, apiKey)

	// Port from env (prefer), fallback to 8080
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port

	autoCtx, autoCancel := context.WithCancel(context.Background())

	srv := &http.Server{
		Addr:    addr,
		Handler: router,
		// useful timeouts
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		log.Printf("🚀 Server starting on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ ListenAndServe(): %v", err)
		}
	}()

	go startAutoCheckout(autoCtx, bookingService)

	// Wait for interrupt signal to gracefully shutdown the server with timeout
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Println("⚠️  Shutdown signal received, shutting down server...")

	autoCancel()

	// Create context with timeout for shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("❌ Server forced to shutdown: %v", err)
	}

	log.Println("✅ Server stopped gracefully")
}

func startAutoCheckout(ctx context.Context, svc *services.BookingService) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	// run immediately
	if err := svc.AutoCheckoutDue(ctx, time.Now()); err != nil {
		log.Printf("auto checkout job failed: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("auto checkout job stopped")
			return
		case now := <-ticker.C:
			if err := svc.AutoCheckoutDue(ctx, now); err != nil {
				log.Printf("auto checkout job failed: %v", err)
			}
		}
	}
}
