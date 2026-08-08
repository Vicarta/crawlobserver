package cli

import "github.com/spf13/cobra"

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Create or update ClickHouse tables",
	RunE:  runMigrate,
}

func init() {
	rootCmd.AddCommand(migrateCmd)
}

func runMigrate(cmd *cobra.Command, args []string) error {
	cfg, releaseWriterLock, err := loadWriterConfig()
	if err != nil {
		return err
	}
	defer releaseWriterLock()
	initializeTelemetry(cmd, cfg)

	store, cleanup, _, err := setupClickHouse(cfg, "default")
	if err != nil {
		return err
	}
	defer store.Close()
	defer cleanup()

	return nil
}
