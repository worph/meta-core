package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/metazla/meta-core/internal/cache"
	"github.com/metazla/meta-core/internal/config"
	"github.com/metazla/meta-core/internal/discovery"
	"github.com/metazla/meta-core/internal/events"
	"github.com/metazla/meta-core/internal/leader"
	"github.com/metazla/meta-core/internal/mounts"
	"github.com/metazla/meta-core/internal/storage"
	"github.com/metazla/meta-core/internal/watcher"
	"github.com/metazla/meta-core/internal/watchers"
	"github.com/metazla/meta-core/internal/webdav"
)

// Server is the HTTP API server for meta-core
type Server struct {
	config            *config.Config
	leaderProvider    *leader.LeaderInfoProvider
	discovery         *discovery.Service
	cleaner           *discovery.Cleaner
	storage           *storage.Client
	mountsManager     *mounts.Manager
	mountsHandlers    *mounts.Handlers
	mountPoller       *mounts.Poller
	watcherDispatcher *watcher.Dispatcher
	fileWatcher       *watcher.Watcher
	watcherHandlers   *watcher.Handlers
	watchersManager   *watchers.Manager
	watchersPoller    *watchers.Poller
	watchersHandlers  *watchers.Handlers
	cacheManager      *cache.Manager
	cacheHandlers     *cache.Handlers
	cacheInvalidator  *cache.Invalidator
	metaPublisher     *events.MetaPublisher
	webdavHandler     *webdav.Handler
	router            *mux.Router
	server            *http.Server
}

// NewServer creates a new API server
func NewServer(
	cfg *config.Config,
	leaderProvider *leader.LeaderInfoProvider,
	disc *discovery.Service,
	cleaner *discovery.Cleaner,
	stor *storage.Client,
) *Server {
	s := &Server{
		config:         cfg,
		leaderProvider: leaderProvider,
		discovery:      disc,
		cleaner:        cleaner,
		storage:        stor,
		router:         mux.NewRouter(),
	}

	// Initialize file watcher/scanner and dispatcher
	var scanFunc mounts.ScanFunc
	if cfg.EnableFileWatcher {
		s.watcherDispatcher = watcher.NewDispatcher()
		// Wire storage client for Redis Stream publishing
		if stor != nil {
			s.watcherDispatcher.SetStorageClient(stor)
		}
		fileWatcher, err := watcher.NewWatcher(cfg, s.watcherDispatcher)
		if err != nil {
			log.Printf("[API] Warning: failed to initialize file watcher: %v", err)
		} else {
			s.fileWatcher = fileWatcher
			s.watcherHandlers = watcher.NewHandlers(fileWatcher, s.watcherDispatcher)
			// Create scan function from watcher for mounts
			scanFunc = fileWatcher.ScanMountPath
		}

		// Initialize watchers manager (polling-based file watching)
		watchersManager, err := watchers.NewManager(cfg)
		if err != nil {
			log.Printf("[API] Warning: failed to initialize watchers manager: %v", err)
		} else {
			s.watchersManager = watchersManager

			// Create watchers poller
			if s.fileWatcher != nil {
				s.watchersPoller = watchers.NewPoller(watchersManager, s.fileWatcher, s.watcherDispatcher)
				// Pass republish callback to handlers (handles nil metaPublisher case)
				s.watchersHandlers = watchers.NewHandlers(watchersManager, s.watchersPoller, s.RepublishMetadata)
			}
		}
	}

	// Initialize mounts manager
	mountsManager, err := mounts.NewManager(cfg)
	if err != nil {
		log.Printf("[API] Warning: failed to initialize mounts manager: %v", err)
	} else {
		s.mountsManager = mountsManager
		s.mountsHandlers = mounts.NewHandlers(mountsManager, scanFunc)

		// Create and configure mount poller
		s.mountPoller = mounts.NewPoller(mountsManager, scanFunc)
		mountsManager.SetPoller(s.mountPoller)
	}

	// Initialize cache manager
	cacheManager, err := cache.NewManager(cfg)
	if err != nil {
		log.Printf("[API] Warning: failed to initialize cache manager: %v", err)
	} else {
		s.cacheManager = cacheManager
		s.cacheHandlers = cache.NewHandlers(cacheManager)
		s.webdavHandler = webdav.NewHandler(cfg, cacheManager)

		// Cache invalidator will be initialized in Start() after Redis is connected
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
	s.router.HandleFunc("/urls", s.handleURLs).Methods("GET")

	// Metadata Editor API routes (must be before /meta/{hash} routes)
	s.router.HandleFunc("/api/metadata/hash-ids", s.handleGetHashIds).Methods("GET")
	s.router.HandleFunc("/api/metadata/list", s.handleListMetadata).Methods("GET")
	s.router.HandleFunc("/api/metadata/search", s.handleSearchMetadata).Methods("POST")
	s.router.HandleFunc("/api/metadata/batch", s.handleBatchUpdate).Methods("POST")
	s.router.HandleFunc("/api/metadata/clear", s.handleClearMetadata).Methods("POST")
	s.router.HandleFunc("/api/metadata/{hashId}/property", s.handleMetadataGetProperty).Methods("GET")
	s.router.HandleFunc("/api/metadata/{hashId}/property", s.handleMetadataUpdateProperty).Methods("PUT")
	s.router.HandleFunc("/api/metadata/{hashId}", s.handleGetMetadataByHashId).Methods("GET")
	s.router.HandleFunc("/api/metadata/{hashId}", s.handleUpdateMetadataByHashId).Methods("PUT")
	s.router.HandleFunc("/api/metadata/{hashId}", s.handleDeleteMetadataByHashId).Methods("DELETE")

	// KV Browser API routes
	s.router.HandleFunc("/api/kv/info", s.handleKVInfo).Methods("GET")
	s.router.HandleFunc("/api/kv/keys", s.handleKVKeys).Methods("GET")
	s.router.HandleFunc("/api/kv/key/{key:.*}", s.handleKVGetKey).Methods("GET")

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
	// Both /services and /api/services are supported for consistency with other services
	s.router.HandleFunc("/services", s.handleListServices).Methods("GET")
	s.router.HandleFunc("/api/services", s.handleListServices).Methods("GET")
	s.router.HandleFunc("/services/cleanup/stats", s.handleCleanupStats).Methods("GET")
	s.router.HandleFunc("/services/{name}", s.handleGetService).Methods("GET")

	// Mount management routes (if manager initialized)
	if s.mountsHandlers != nil {
		s.mountsHandlers.RegisterRoutes(s.router)
		log.Println("[API] Mount management routes registered at /api/mounts/*")
	}

	log.Println("[API] Metadata Editor routes registered at /api/metadata/*")
	log.Println("[API] KV Browser routes registered at /api/kv/*")

	// File watcher routes (if watcher initialized)
	if s.watcherHandlers != nil {
		s.watcherHandlers.RegisterRoutes(s.router)
		log.Println("[API] File watcher routes registered at /api/events/*, /api/scan/*")
	}

	// Watchers management routes (if watchers initialized)
	if s.watchersHandlers != nil {
		s.watchersHandlers.RegisterRoutes(s.router)
		log.Println("[API] Watchers management routes registered at /api/watchers/*")
	}

	// Cache management routes (if cache initialized)
	if s.cacheHandlers != nil {
		s.cacheHandlers.RegisterRoutes(s.router)
		log.Println("[API] Cache management routes registered at /api/cache/*")
	}

	// WebDAV handler (if cache initialized)
	if s.webdavHandler != nil {
		s.router.PathPrefix("/webdav/").Handler(s.webdavHandler)
		s.router.PathPrefix("/webdav").Handler(s.webdavHandler)
		log.Println("[API] WebDAV routes registered at /webdav/*")
	}

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

	// Start watchers poller (if initialized)
	if s.watchersPoller != nil {
		if err := s.watchersPoller.Start(); err != nil {
			log.Printf("[API] Warning: failed to start watchers poller: %v", err)
		}
	}

	// Start mount poller (if initialized)
	if s.mountPoller != nil {
		if err := s.mountPoller.Start(); err != nil {
			log.Printf("[API] Warning: failed to start mount poller: %v", err)
		}
	}

	// Start cache manager (if initialized)
	if s.cacheManager != nil {
		if err := s.cacheManager.Start(); err != nil {
			log.Printf("[API] Warning: failed to start cache manager: %v", err)
		}

		// Initialize cache invalidator now that Redis is connected
		if s.storage != nil && s.storage.IsConnected() {
			s.cacheInvalidator = cache.NewInvalidator(s.cacheManager, s.storage.GetRedisClient())
			if err := s.cacheInvalidator.Start(); err != nil {
				log.Printf("[API] Warning: failed to start cache invalidator: %v", err)
			}
		}
	}

	// Start meta publisher (if storage is connected)
	// Publishes keyspace notifications to meta:events stream
	if s.storage != nil && s.storage.IsConnected() {
		s.metaPublisher = events.NewMetaPublisher(s.storage.GetRedisClient())
		if err := s.metaPublisher.Start(); err != nil {
			log.Printf("[API] Warning: failed to start meta publisher: %v", err)
		} else {
			// Republish events for all existing metadata
			go func() {
				count, err := s.RepublishMetadata()
				if err != nil {
					log.Printf("[API] Warning: failed to republish metadata on startup: %v", err)
				} else {
					log.Printf("[API] Republished %d metadata events on startup", count)
				}
			}()
		}
	}

	return nil
}

// Stop gracefully stops the HTTP server
func (s *Server) Stop() error {
	// Stop watchers poller
	if s.watchersPoller != nil {
		if err := s.watchersPoller.Stop(); err != nil {
			log.Printf("[API] Warning: failed to stop watchers poller: %v", err)
		}
	}

	// Stop mount poller
	if s.mountPoller != nil {
		if err := s.mountPoller.Stop(); err != nil {
			log.Printf("[API] Warning: failed to stop mount poller: %v", err)
		}
	}

	// Stop meta publisher
	if s.metaPublisher != nil {
		if err := s.metaPublisher.Stop(); err != nil {
			log.Printf("[API] Warning: failed to stop meta publisher: %v", err)
		}
	}

	// Stop cache invalidator
	if s.cacheInvalidator != nil {
		if err := s.cacheInvalidator.Stop(); err != nil {
			log.Printf("[API] Warning: failed to stop cache invalidator: %v", err)
		}
	}

	// Stop cache manager
	if s.cacheManager != nil {
		if err := s.cacheManager.Stop(); err != nil {
			log.Printf("[API] Warning: failed to stop cache manager: %v", err)
		}
	}

	if s.server == nil {
		return nil
	}

	log.Println("[API] Stopping HTTP server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return s.server.Shutdown(ctx)
}

// RepublishMetadata republishes all existing metadata to the meta:events stream
// This is called on startup and after reset-all
func (s *Server) RepublishMetadata() (int, error) {
	if s.metaPublisher == nil {
		return 0, fmt.Errorf("meta publisher not initialized")
	}
	if s.storage == nil || !s.storage.IsConnected() {
		return 0, fmt.Errorf("storage not connected")
	}

	// Create the republish function that wraps storage methods
	republishFunc := func() ([]string, func(string) (map[string]string, error), error) {
		hashIDs, err := s.storage.GetAllHashIDs()
		if err != nil {
			return nil, nil, err
		}
		return hashIDs, s.storage.GetMetadataFlat, nil
	}

	return s.metaPublisher.RepublishAllMetadata(republishFunc)
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
