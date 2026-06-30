package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"propagate/backend/internal/api"
	"propagate/backend/internal/dotenv"
	"propagate/backend/internal/storage"

	_ "github.com/lib/pq"
)

func main() {
	if err := dotenv.LoadDefault(); err != nil {
		log.Fatalf("load .env: %v", err)
	}

	config := api.ConfigFromEnv()
	var store storage.Store = storage.NewMemoryStore()
	if databaseURL := strings.TrimSpace(os.Getenv("PROPAGATE_DATABASE_URL")); databaseURL != "" {
		db, err := sql.Open("postgres", databaseURL)
		if err != nil {
			log.Fatalf("open database: %v", err)
		}
		defer db.Close()
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(5)
		db.SetConnMaxLifetime(30 * time.Minute)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			log.Fatalf("connect database: %v", err)
		}
		store = storage.NewSQLStore(db)
	}
	handler := api.NewServer(store, config)

	addr := os.Getenv("PORT")
	if addr == "" {
		addr = "8080"
	}
	if !strings.HasPrefix(addr, ":") {
		addr = ":" + addr
	}

	log.Printf("Starting propagate API on %s", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}
