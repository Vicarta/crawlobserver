package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/SEObserver/crawlobserver/internal/apikeys"
	"github.com/SEObserver/crawlobserver/internal/applog"
	"github.com/SEObserver/crawlobserver/internal/backup"
	"github.com/SEObserver/crawlobserver/internal/config"
	"github.com/SEObserver/crawlobserver/internal/server"
	"github.com/SEObserver/crawlobserver/internal/storage"
	"github.com/SEObserver/crawlobserver/internal/telemetry"
	"github.com/SEObserver/crawlobserver/internal/updater"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the web server",
	Long:  `Start the web server and open the browser UI for browsing crawl results.`,
	RunE:  runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().Int("port", 0, "Port for the web server")
}

func runServe(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if port, _ := cmd.Flags().GetInt("port"); port > 0 {
		cfg.Server.Port = port
	}

	// Windows without external ClickHouse → guided setup mode
	mode := cfg.ClickHouse.Mode
	if mode == "" {
		mode = detectMode(cfg)
	}
	if runtime.GOOS == "windows" && mode == "managed" {
		return runServeSetupMode(cfg)
	}

	store, cleanup, _, err := setupClickHouse(cfg, cfg.ClickHouse.Database)
	if err != nil {
		return err
	}
	defer store.Close()
	defer cleanup()

	keyStore, err := apikeys.NewStore(cfg.Server.SQLitePath)
	if err != nil {
		return fmt.Errorf("opening SQLite store: %w", err)
	}
	defer keyStore.Close()

	srv := server.New(cfg, store, keyStore)
	srv.UpdateStatus = updater.NewUpdateStatus()

	// Configure SQL backup for external ClickHouse
	backupDir := resolveBackupDir(cfg)
	sqlBackupOpts := &backup.SQLBackupOptions{
		CHURL:      fmt.Sprintf("http://%s:%d", cfg.ClickHouse.Host, cfg.ClickHouse.EffectiveHTTPPort()),
		Database:   cfg.ClickHouse.Database,
		Username:   cfg.ClickHouse.Username,
		Password:   cfg.ClickHouse.Password,
		SQLitePath: cfg.Server.SQLitePath,
		ConfigPath: viper.ConfigFileUsed(),
		BackupDir:  backupDir,
	}
	srv.SQLBackupOpts = sqlBackupOpts
	srv.ExportDir = filepath.Join(backupDir, "exports")
	srv.ExportRetain = cfg.Backup.Retain

	// Background update check
	go func() {
		time.Sleep(3 * time.Second)
		srv.UpdateStatus.Check()
		snap := srv.UpdateStatus.Snapshot()
		if snap.Available {
			applog.Infof("cli", "Update available: %s -> %s  (run 'crawlobserver update' to install)", snap.CurrentVersion, snap.LatestVersion)
		}
	}()

	defer telemetry.Close()

	// Shutdown context for background goroutines
	ctx, cancelCtx := context.WithCancel(context.Background())

	// Auto-backup scheduler
	if cfg.Backup.Enabled {
		go runBackupScheduler(ctx, cfg, sqlBackupOpts, store)
	}
	if cfg.Retention.SessionsPerProject > 0 {
		go runSessionRetentionScheduler(ctx, cfg, store)
	}

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		applog.Info("cli", "Shutting down web server...")
		cancelCtx()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		srv.Stop(shutdownCtx)
	}()

	return srv.Start()
}

// runBackupScheduler runs periodic SQL backups and critical table exports.
func runBackupScheduler(ctx context.Context, cfg *config.Config, opts *backup.SQLBackupOptions, store *storage.Store) {
	interval, err := time.ParseDuration(cfg.Backup.Interval)
	if err != nil || interval < 1*time.Hour {
		interval = 6 * time.Hour
	}

	retain := cfg.Backup.Retain
	if retain < 1 {
		retain = 5
	}

	exportDir := filepath.Join(opts.BackupDir, "exports")

	applog.Infof("cli", "Auto-backup enabled: every %s, retaining %d backups in %s", interval, retain, opts.BackupDir)

	// Run first backup shortly after startup
	select {
	case <-time.After(30 * time.Second):
	case <-ctx.Done():
		return
	}
	performBackup(ctx, opts, retain, store, exportDir)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			performBackup(ctx, opts, retain, store, exportDir)
		case <-ctx.Done():
			return
		}
	}
}

func performBackup(ctx context.Context, opts *backup.SQLBackupOptions, retain int, store *storage.Store, exportDir string) {
	if ctx.Err() != nil {
		return
	}
	applog.Info("cli", "Starting scheduled backup...")
	info, err := backup.CreateSQLBackup(ctx, *opts, updater.Version)
	if err != nil {
		applog.Errorf("cli", "Scheduled backup failed: %v", err)
	} else {
		applog.Infof("cli", "Backup created: %s (%.1f MB)", info.Filename, float64(info.Size)/(1024*1024))
		if pruned, _ := backup.PruneBackups(opts.BackupDir, retain); pruned > 0 {
			applog.Infof("cli", "Pruned %d old backup(s)", pruned)
		}
	}

	// Export critical non-regenerable tables
	applog.Info("cli", "Exporting critical tables...")
	if err := store.ExportCriticalTables(ctx, exportDir, retain); err != nil {
		applog.Errorf("cli", "Critical table export failed: %v", err)
	} else {
		applog.Info("cli", "Critical table export complete")
	}
}

func runSessionRetentionScheduler(ctx context.Context, cfg *config.Config, store *storage.Store) {
	keep := cfg.Retention.SessionsPerProject
	if keep < 1 {
		return
	}

	interval, err := time.ParseDuration(cfg.Retention.Interval)
	if err != nil || interval < time.Minute {
		interval = 15 * time.Minute
	}

	applog.Infof("cli", "Session retention enabled: keeping %d inactive session(s) per project every %s", keep, interval)

	select {
	case <-time.After(2 * time.Minute):
	case <-ctx.Done():
		return
	}
	pruneOldSessions(ctx, store, keep)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			pruneOldSessions(ctx, store, keep)
		case <-ctx.Done():
			return
		}
	}
}

func pruneOldSessions(ctx context.Context, store *storage.Store, keep int) {
	if ctx.Err() != nil || keep < 1 {
		return
	}
	sessions, err := store.ListSessions(ctx)
	if err != nil {
		applog.Errorf("cli", "Session retention failed to list sessions: %v", err)
		return
	}
	for _, sess := range sessionsToPrune(sessions, keep) {
		if err := store.DeleteSession(ctx, sess.ID); err != nil {
			applog.Errorf("cli", "Session retention failed to delete session %s: %v", sess.ID, err)
			continue
		}
		applog.Infof("cli", "Session retention deleted old session %s from project %s", sess.ID, retentionProjectKey(sess))
	}
}

func sessionsToPrune(sessions []storage.CrawlSession, keep int) []storage.CrawlSession {
	if keep < 1 {
		return nil
	}

	byProject := make(map[string][]storage.CrawlSession)
	for _, sess := range sessions {
		if isActiveSessionStatus(sess.Status) {
			continue
		}
		key := retentionProjectKey(sess)
		byProject[key] = append(byProject[key], sess)
	}

	var prune []storage.CrawlSession
	for _, projectSessions := range byProject {
		sort.SliceStable(projectSessions, func(i, j int) bool {
			return projectSessions[i].StartedAt.After(projectSessions[j].StartedAt)
		})
		if len(projectSessions) > keep {
			prune = append(prune, projectSessions[keep:]...)
		}
	}
	sort.SliceStable(prune, func(i, j int) bool {
		return prune[i].StartedAt.Before(prune[j].StartedAt)
	})
	return prune
}

func isActiveSessionStatus(status string) bool {
	switch status {
	case "running", "queued", "stopping":
		return true
	default:
		return false
	}
}

func retentionProjectKey(sess storage.CrawlSession) string {
	if sess.ProjectID == nil || *sess.ProjectID == "" {
		return "_unassigned"
	}
	return *sess.ProjectID
}

// resolveBackupDir returns the backup directory from config or a default.
func resolveBackupDir(cfg *config.Config) string {
	if cfg.Backup.Dir != "" {
		return cfg.Backup.Dir
	}
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		return filepath.Join(".", "backups")
	}
	return filepath.Join(dataDir, "backups")
}

// runServeSetupMode starts the server in setup mode on Windows when no ClickHouse is available.
// The onboarding wizard guides the user through installing ClickHouse via Docker or WSL.
// A background goroutine polls for ClickHouse availability and transitions to ready automatically.
func runServeSetupMode(cfg *config.Config) error {
	applog.Info("cli", "Windows detected without ClickHouse — starting in setup mode")

	srv := server.NewSetupServer(cfg)
	srv.UpdateStatus = updater.NewUpdateStatus()

	var (
		mu        sync.Mutex
		cleanupFn func()
	)

	// Background goroutine: poll for ClickHouse, then setup
	go func() {
		for detectMode(cfg) != "external" {
			time.Sleep(3 * time.Second)
		}

		applog.Info("cli", "ClickHouse detected, completing setup...")

		store, cleanup, _, err := setupClickHouse(cfg, cfg.ClickHouse.Database)
		if err != nil {
			applog.Errorf("cli", "ClickHouse setup failed: %v", err)
			return
		}

		keyStore, err := apikeys.NewStore(cfg.Server.SQLitePath)
		if err != nil {
			store.Close()
			cleanup()
			applog.Errorf("cli", "SQLite store failed: %v", err)
			return
		}

		mu.Lock()
		cleanupFn = func() {
			keyStore.Close()
			store.Close()
			cleanup()
		}
		mu.Unlock()

		srv.TransitionToReady(store, keyStore)
		applog.Init(store)
		srv.SetDownloadProgress(server.SetupProgress{Percent: 100})

		// Background update check
		go func() {
			time.Sleep(5 * time.Second)
			srv.UpdateStatus.Check()
		}()

		applog.Info("cli", "Setup complete — server is ready")
	}()

	defer telemetry.Close()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		applog.Info("cli", "Shutting down web server...")
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		srv.Stop(ctx)
	}()

	err := srv.Start()

	mu.Lock()
	if cleanupFn != nil {
		cleanupFn()
	}
	mu.Unlock()

	return err
}
