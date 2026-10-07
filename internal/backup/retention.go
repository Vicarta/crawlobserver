package backup

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

const weeklyRetentionDailyCount = 3
const preUpdateBackupSuffix = "-pre-update.tar.gz"

// IsPreUpdateBackupFilename reports whether a filename is explicitly marked
// as a SQLite/config-only pre-update copy.
func IsPreUpdateBackupFilename(filename string) bool {
	return strings.HasPrefix(filename, "backup-") && strings.HasSuffix(filename, preUpdateBackupSuffix)
}

// RetentionLocation resolves the timezone used by both scheduled runs and
// calendar-based retention. An empty value preserves the server's local zone.
func RetentionLocation(timezone string) (*time.Location, error) {
	if timezone == "" {
		return time.Local, nil
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("loading backup timezone %q: %w", timezone, err)
	}
	return location, nil
}

// BackupsToRetain returns the archives selected by the configured retention
// policy. Weekly mode keeps the latest archive from three distinct local dates
// plus the latest archive from the previous local calendar week.
func BackupsToRetain(backups []BackupInfo, retain, retainWeekly int, location *time.Location, now time.Time) ([]BackupInfo, error) {
	if retainWeekly != 0 && retainWeekly != 1 {
		return nil, fmt.Errorf("backup.retain_weekly must be 0 or 1")
	}

	ordered := append([]BackupInfo(nil), backups...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].Filename < ordered[j].Filename
		}
		return ordered[i].CreatedAt.After(ordered[j].CreatedAt)
	})

	if retainWeekly == 0 {
		if retain < 0 {
			return nil, fmt.Errorf("backup.retain must be >= 0")
		}
		if retain > len(ordered) {
			retain = len(ordered)
		}
		return ordered[:retain], nil
	}
	if location == nil {
		return nil, fmt.Errorf("backup timezone is required for weekly retention")
	}
	if retain < 0 {
		return nil, fmt.Errorf("backup.retain must be >= 0")
	}

	fullTimes := make([]time.Time, 0, len(ordered))
	fullIndices := make([]int, 0, len(ordered))
	preUpdateIndices := make([]int, 0, len(ordered))
	for i, archive := range ordered {
		if IsPreUpdateBackupFilename(archive.Filename) {
			preUpdateIndices = append(preUpdateIndices, i)
			continue
		}
		fullTimes = append(fullTimes, archive.CreatedAt)
		fullIndices = append(fullIndices, i)
	}
	indices, err := CalendarRetentionIndices(fullTimes, location, now)
	if err != nil {
		return nil, err
	}
	kept := make(map[int]struct{}, len(indices)+retain)
	for _, index := range indices {
		kept[fullIndices[index]] = struct{}{}
	}
	if retain > len(preUpdateIndices) {
		retain = len(preUpdateIndices)
	}
	for _, index := range preUpdateIndices[:retain] {
		kept[index] = struct{}{}
	}
	selected := make([]BackupInfo, 0, len(kept))
	for i, archive := range ordered {
		if _, ok := kept[i]; ok {
			selected = append(selected, archive)
		}
	}
	return selected, nil
}

// CalendarRetentionIndices returns input indices for the latest archive from
// three distinct local dates and the newest archive from the previous local
// Monday-to-Sunday week. The returned indices are newest first.
func CalendarRetentionIndices(times []time.Time, location *time.Location, now time.Time) ([]int, error) {
	if location == nil {
		return nil, fmt.Errorf("backup timezone is required for weekly retention")
	}
	ordered := make([]int, len(times))
	for i := range times {
		ordered[i] = i
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return times[ordered[i]].After(times[ordered[j]])
	})

	kept := make([]bool, len(times))
	dates := make(map[[3]int]struct{}, weeklyRetentionDailyCount)
	dailyCount := 0
	for _, index := range ordered {
		local := times[index].In(location)
		year, month, day := local.Date()
		date := [3]int{year, int(month), day}
		if _, exists := dates[date]; exists {
			continue
		}
		dates[date] = struct{}{}
		kept[index] = true
		dailyCount++
		if dailyCount == weeklyRetentionDailyCount {
			break
		}
	}

	localNow := now.In(location)
	weekdayOffset := (int(localNow.Weekday()) + 6) % 7
	thisWeekStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, location).AddDate(0, 0, -weekdayOffset)
	previousWeekStart := thisWeekStart.AddDate(0, 0, -7)
	for _, index := range ordered {
		local := times[index].In(location)
		if !local.Before(previousWeekStart) && local.Before(thisWeekStart) {
			kept[index] = true
			break
		}
	}

	selected := make([]int, 0, weeklyRetentionDailyCount+1)
	for _, index := range ordered {
		if kept[index] {
			selected = append(selected, index)
		}
	}
	return selected, nil
}

// PruneBackupsWithPolicy removes archives not selected by the configured
// retention policy. A zero weekly value preserves the legacy count behavior.
func PruneBackupsWithPolicy(backupDir string, retain, retainWeekly int, location *time.Location, now time.Time) (deleted int, err error) {
	backups, err := ListBackups(backupDir)
	if err != nil {
		return 0, err
	}
	selected, err := BackupsToRetain(backups, retain, retainWeekly, location, now)
	if err != nil {
		return 0, err
	}
	keep := make(map[string]struct{}, len(selected))
	for _, archive := range selected {
		keep[archive.Path] = struct{}{}
	}
	for _, archive := range backups {
		if _, ok := keep[archive.Path]; ok {
			continue
		}
		if err := os.Remove(archive.Path); err != nil {
			return deleted, fmt.Errorf("removing backup %s: %w", archive.Filename, err)
		}
		deleted++
	}
	return deleted, nil
}
