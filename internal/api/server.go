package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/metazla/meta-core/internal/config"
	"github.com/metazla/meta-core/internal/discovery"
	"github.com/metazla/meta-core/internal/leader"
	"github.com/metazla/meta-core/internal/storage"
)

// Server is the HTTP API server for meta-core
type Server struct {
	config    *config.Config
	election  *leader.Election
	discovery *discovery.Service
	storage   *storage.Client
	router    *mux.Router
	server    *http.Server
}

// NewServer creates a new API server
func NewServer(
	cfg *config.Config,
	election *leader.Election,
	disc *discovery.Service,
	stor *storage.Client,
) *Server {
	s := &Server{
		config:    cfg,
		election:  election,
		discovery: disc,
		storage:   stor,
		router:    mux.NewRouter(),
	}

	s.setupRoutes()
	return s
}

// setupRoutes configures all API routes
func (s *Server) setupRoutes() {
	// Health and status
	s.router.HandleFunc("/health", s.handleHealth).Methods("GET")
	s.router.HandleFunc("/status", s.handleStatus).Methods("GET")
	s.router.HandleFunc("/leader", s.handleLeader).Methods("GET")
	s.router.HandleFunc("/role", s.handleRole).Methods("GET")

	// Metadata operations - base endpoints
	s.router.HandleFunc("/meta/{hash}", s.handleGetMeta).Methods("GET")
	s.router.HandleFunc("/meta/{hash}", s.handlePutMeta).Methods("PUT")
	s.router.HandleFunc("/meta/{hash}", s.handlePatchMeta).Methods("PATCH")
	s.router.HandleFunc("/meta/{hash}", s.handleDeleteMeta).Methods("DELETE")
	s.router.HandleFunc("/meta", s.handleListMeta).Methods("GET")

	// Metadata operations - set operations (must be before property routes)
	s.router.HandleFunc("/meta/{hash}/_add/{key:.*}", s.handleAddToSet).Methods("POST")

	// Metadata operations - property-level (key can contain slashes)
	s.router.HandleFunc("/meta/{hash}/{key:.*}", s.handleGetProperty).Methods("GET")
	s.router.HandleFunc("/meta/{hash}/{key:.*}", s.handlePutProperty).Methods("PUT")
	s.router.HandleFunc("/meta/{hash}/{key:.*}", s.handleDeleteProperty).Methods("DELETE")

	// Data operations
	s.router.HandleFunc("/data/{hash}/path", s.handleGetDataPath).Methods("GET")
	s.router.HandleFunc("/data/{hash}", s.handleHeadData).Methods("HEAD")

	// File operations (by CID)
	s.router.HandleFunc("/file/{cid}", s.handleGetFileByCID).Methods("GET")
	s.router.HandleFunc("/file/cid", s.handleComputeFileCID).Methods("POST")

	// Service discovery
	s.router.HandleFunc("/services", s.handleListServices).Methods("GET")
	s.router.HandleFunc("/services/{name}", s.handleGetService).Methods("GET")

	// Add middleware
	s.router.Use(loggingMiddleware)
	s.router.Use(corsMiddleware)
}

// Start starts the HTTP server
func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.config.HTTPHost, s.config.HTTPPort)

	s.server = &http.Server{
		Addr:         addr,
		Handler:      s.router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("[API] Starting HTTP server on %s", addr)

	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[API] HTTP server error: %v", err)
		}
	}()

	return nil
}

// Stop gracefully stops the HTTP server
func (s *Server) Stop() error {
	if s.server == nil {
		return nil
	}

	log.Println("[API] Stopping HTTP server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return s.server.Shutdown(ctx)
}

// loggingMiddleware logs all requests
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("[API] %s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

// corsMiddleware adds CORS headers
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
