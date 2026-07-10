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
	"github.com/metazla/meta-core/internal/events"
	"github.com/metazla/meta-core/internal/identity"
	"github.com/metazla/meta-core/internal/leader"
	"github.com/metazla/meta-core/internal/mounts"
	"github.com/metazla/meta-core/internal/schema"
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
	mountStatsPoller  *mounts.StatsPoller
	watcherDispatcher *watcher.Dispatcher
	fileWatcher       *watcher.Watcher
	watcherHandlers   *watcher.Handlers
	watchersManager   *watchers.Manager
	watchersPoller    *watchers.Poller
	watchersHandlers  *watchers.Handlers
	metaPublisher     *events.MetaPublisher
	schemaIndexer     *schema.Indexer
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
			// Wire storage so the watcher can persist (path,size,mtime,cid)
			// tuples to Redis as it computes midhashes.
			if stor != nil {
				fileWatcher.SetStorageClient(stor)
			}
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

				// Hydrate the in-memory state registry from Redis tuples so
				// the first scan tick after boot can skip ComputeMidHash256
				// for files whose (size, mtime) match.
				configs := watchersManager.List()
				roots := make([]watcher.WatcherRoot, 0, len(configs))
				for _, c := range configs {
					roots = append(roots, watcher.WatcherRoot{ID: c.ID, Root: c.Path})
				}
				if loaded, skipped, err := s.fileWatcher.HydrateStateFromStorage(roots); err != nil {
					log.Printf("[API] Warning: failed to hydrate watcher state: %v", err)
				} else {
					log.Printf("[API] Hydrated watcher state from Redis: %d files loaded, %d skipped (no matching watcher root)", loaded, skipped)
				}
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

		// IO stats poller — queries the local rclone RC API every few seconds
		// and exposes live throughput per mount via the same status JSON.
		// Stats are daemon-global (rclone limitation); see stats_poller.go.
		s.mountStatsPoller = mounts.NewStatsPoller(mountsManager)
		mountsManager.SetStatsPoller(s.mountStatsPoller)
	}

	// Initialize WebDAV handler (caching is handled by nginx proxy_cache)
	s.webdavHandler = webdav.NewHandler(cfg)

	// One-time migration: fold a pre-multi-account identity.json into the
	// per-account keystore so existing single-identity hosts keep their uid.
	if migrated, err := identity.MigrateLegacy(cfg.IdentityFilePath(), cfg.IdentityAccountsDir()); err != nil {
		log.Printf("[API] Warning: identity migration failed: %v", err)
	} else if migrated {
		log.Printf("[API] Migrated legacy identity.json into multi-account keystore")
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

	// User Data Layer signing identity (see internal/identity). Auth-gated
	// at the perimeter — do NOT add to nginx-hash-lock unauth bypass.
	s.router.HandleFunc("/api/identity", s.handleIdentityGet).Methods("GET")
	s.router.HandleFunc("/api/identity/accounts", s.handleIdentityAccounts).Methods("GET")
	s.router.HandleFunc("/api/identity/generate", s.handleIdentityGenerate).Methods("POST")
	s.router.HandleFunc("/api/identity/import", s.handleIdentityImport).Methods("POST")
	s.router.HandleFunc("/api/identity", s.handleIdentityDelete).Methods("DELETE")
	s.router.HandleFunc("/api/identity/sign", s.handleIdentitySign).Methods("POST")
	s.router.HandleFunc("/api/identity/aead-key", s.handleIdentityAEADKey).Methods("GET")

	// Snapshot (export / import / wipe)
	s.router.HandleFunc("/api/snapshot/export", s.handleSnapshotExport).Methods("GET")
	s.router.HandleFunc("/api/snapshot/import", s.handleSnapshotImport).Methods("POST")
	s.router.HandleFunc("/api/snapshot/wipe", s.handleSnapshotWipe).Methods("POST")
	s.router.HandleFunc("/api/metadata/{hashId}/property", s.handleMetadataGetProperty).Methods("GET")
	s.router.HandleFunc("/api/metadata/{hashId}/property", s.handleMetadataUpdateProperty).Methods("PUT")
	s.router.HandleFunc("/api/metadata/{hashId}", s.handleGetMetadataByHashId).Methods("GET")
	s.router.HandleFunc("/api/metadata/{hashId}", s.handleUpdateMetadataByHashId).Methods("PUT")
	s.router.HandleFunc("/api/metadata/{hashId}", s.handleDeleteMetadataByHashId).Methods("DELETE")

	// Schema inference
	s.router.HandleFunc("/api/schema", s.handleSchemaGet).Methods("GET")
	s.router.HandleFunc("/api/schema/rescan", s.handleSchemaRescan).Methods("POST")

	// KV Browser API routes
	s.router.HandleFunc("/api/kv/info", s.handleKVInfo).Methods("GET")
	s.router.HandleFunc("/api/kv/keys", s.handleKVKeys).Methods("GET")
	s.router.HandleFunc("/api/kv/tree", s.handleKVTree).Methods("GET")
	s.router.HandleFunc("/api/kv/search", s.handleKVSearch).Methods("GET")
	s.router.HandleFunc("/api/kv/find", s.handleKVFind).Methods("GET")
	s.router.HandleFunc("/api/kv/value", s.handleKVValueGet).Methods("GET")
	s.router.HandleFunc("/api/kv/value", s.handleKVValuePut).Methods("PUT")
	s.router.HandleFunc("/api/kv/value", s.handleKVValueDelete).Methods("DELETE")
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
	s.router.HandleFunc("/api/file/{cid}/info", s.handleGetFileByCIDInfo).Methods("GET")
	s.router.HandleFunc("/file/{cid}", s.handleGetFileByCID).Methods("GET")
	s.router.HandleFunc("/file/cid", s.handleComputeFileCID).Methods("POST")

	// Public metadata-by-CID resolver. Reverse-index lookup → full document.
	// Auth-bypassed alongside /api/file/{cid} so meta-share peers can query
	// without going through Authelia.
	s.router.HandleFunc("/api/meta/{cid}", s.handleGetMetaByCID).Methods("GET")

	// Admin: reunify stranded midhash-rooted entries into their UUID roots
	// (the dual-root pattern caused by historical writes that bypassed CID
	// resolution).
	s.router.HandleFunc("/api/admin/migrate-dual-roots", s.handleMigrateDualRoots).Methods("POST")

	// SSE event streams. Mediated mirror of the Redis Streams; consumers
	// reach them over HTTP instead of touching Redis directly. Auth-bypass
	// applies but exposure should be inside-only (do NOT proxy through
	// Caddy publicly). See docs/api-mediated-access.md "Auth".
	s.router.HandleFunc("/api/events/files", s.handleEventsFiles).Methods("GET")
	s.router.HandleFunc("/api/events/meta", s.handleEventsMeta).Methods("GET")

	// Service discovery
	// Both /services and /api/services are supported for consistency with other services
	s.router.HandleFunc("/services", s.handleListServices).Methods("GET")
	s.router.HandleFunc("/api/services", s.handleListServices).Methods("GET")
	s.router.HandleFunc("/services/cleanup/stats", s.handleCleanupStats).Methods("GET")
	s.router.HandleFunc("/services/{name}", s.handleGetService).Methods("GET")

	// Mount management routes (if manager initialized)
	if s.mountsHandlers != nil {
		s.mountsHandlers.RegisterRoutes(s.router)
	}

	// File watcher routes (if watcher initialized)
	if s.watcherHandlers != nil {
		s.watcherHandlers.RegisterRoutes(s.router)
	}

	// Watchers management routes (if watchers initialized)
	if s.watchersHandlers != nil {
		s.watchersHandlers.RegisterRoutes(s.router)
	}

	// WebDAV handler (caching is handled by nginx proxy_cache)
	if s.webdavHandler != nil {
		s.router.PathPrefix("/webdav/").Handler(s.webdavHandler)
		s.router.PathPrefix("/webdav").Handler(s.webdavHandler)
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

	// Start IO stats poller alongside the mount poller. Cheap (one /proc read
	// per CIFS/NFS mount per cycle, plus one RC call shared by all rclone
	// mounts), so always-on rather than gated by config.
	if s.mountStatsPoller != nil {
		s.mountStatsPoller.Start()
	}

	// Start meta publisher (if storage is connected)
	// Publishes keyspace notifications to meta:events stream
	if s.storage != nil && s.storage.IsConnected() {
		s.metaPublisher = events.NewMetaPublisher(s.storage.GetRedisClient())
		if err := s.metaPublisher.Start(); err != nil {
			log.Printf("[API] Warning: failed to start meta publisher: %v", err)
		} else {
			// Schema indexer consumes the same stream to derive a live
			// per-field schema (type/hint/breakdown). Start it before the
			// republish flood so it captures the bootstrap events.
			s.schemaIndexer = schema.NewIndexer(s.storage.GetRedisClient(), s.storage.GetPrefix())
			if err := s.schemaIndexer.Start(); err != nil {
				log.Printf("[API] Warning: failed to start schema indexer: %v", err)
				s.schemaIndexer = nil
			}

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

	// Stop IO stats poller
	if s.mountStatsPoller != nil {
		s.mountStatsPoller.Stop()
	}

	// Stop schema indexer before the publisher so the consumer drains cleanly
	if s.schemaIndexer != nil {
		if err := s.schemaIndexer.Stop(); err != nil {
			log.Printf("[API] Warning: failed to stop schema indexer: %v", err)
		}
	}

	// Stop meta publisher
	if s.metaPublisher != nil {
		if err := s.metaPublisher.Stop(); err != nil {
			log.Printf("[API] Warning: failed to stop meta publisher: %v", err)
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

// statusRecorder captures the response status so loggingMiddleware can stay
// silent on success and only report failures. It forwards Flush/Unwrap so SSE
// and other streaming handlers keep working through the wrapper.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.written {
		r.status = code
		r.written = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.written {
		r.status = http.StatusOK
		r.written = true
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// loggingMiddleware logs a request only when it fails (status >= 400), keeping
// the nominal case quiet. Successful requests are not logged.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if rec.status >= 400 {
			log.Printf("[API] %s %s -> %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start))
		}
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
