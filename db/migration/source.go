// Package migration provides versioned SQL migrations and reusable CLI commands.
package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	filePattern = regexp.MustCompile(`^([0-9]{14})_([a-z][a-z0-9_]*)\.(up|down)\.sql$`)
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

const (
	upTemplate   = "-- Write the forward migration here.\n"
	downTemplate = "-- Write the rollback migration here.\n"
)

// Migration is one reversible, versioned database change.
type Migration struct {
	Version string
	Name    string
	UpSQL   string
	DownSQL string
	// NonTransactional runs PostgreSQL SQL outside a transaction (for example
	// CREATE INDEX CONCURRENTLY). MySQL always uses non-transactional execution.
	NonTransactional bool
}

// ID returns the stable version and name used in history and file names.
func (m Migration) ID() string { return m.Version + "_" + m.Name }

// Checksum detects changes to an already applied migration.
func (m Migration) Checksum() string {
	up := strings.ReplaceAll(m.UpSQL, "\r\n", "\n")
	down := strings.ReplaceAll(m.DownSQL, "\r\n", "\n")
	input := up + "\x00" + down
	if m.NonTransactional {
		input += "\x00non-transactional"
	}
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

func (m Migration) validate() error {
	if len(m.Version) != 14 {
		return fmt.Errorf("migration: invalid version %q", m.Version)
	}
	if _, err := time.Parse("20060102150405", m.Version); err != nil {
		return fmt.Errorf("migration: invalid version %q: %w", m.Version, err)
	}
	if !namePattern.MatchString(m.Name) {
		return fmt.Errorf("migration: invalid name %q", m.Name)
	}
	if strings.TrimSpace(m.UpSQL) == "" || m.UpSQL == upTemplate {
		return fmt.Errorf("migration: %s has empty up SQL", m.ID())
	}
	if strings.TrimSpace(m.DownSQL) == "" || m.DownSQL == downTemplate {
		return fmt.Errorf("migration: %s has empty down SQL", m.ID())
	}
	return nil
}

// Source supplies migrations in any order. Runner validates and sorts them.
type Source interface {
	Migrations(context.Context) ([]Migration, error)
}

// DirectorySource loads paired SQL files from a directory.
type DirectorySource struct{ Path string }

func (source DirectorySource) Migrations(ctx context.Context) ([]Migration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(source.Path) == "" {
		return nil, errors.New("migration: source directory must not be empty")
	}
	root, err := os.OpenRoot(source.Path)
	if err != nil {
		return nil, fmt.Errorf("migration: open directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("migration: read directory %q: %w", source.Path, err)
	}
	type pair struct {
		migration Migration
		upSeen    bool
		downSeen  bool
	}
	pairs := make(map[string]*pair)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() {
			continue
		}
		matches := filePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			if strings.HasSuffix(strings.ToLower(entry.Name()), ".sql") {
				return nil, fmt.Errorf("migration: invalid SQL migration file %q", entry.Name())
			}
			continue
		}
		id := matches[1] + "_" + matches[2]
		item := pairs[id]
		if item == nil {
			item = &pair{migration: Migration{Version: matches[1], Name: matches[2]}}
			pairs[id] = item
		}
		contents, err := root.ReadFile(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("migration: read %q: %w", entry.Name(), err)
		}
		if strings.SplitN(strings.ReplaceAll(string(contents), "\r\n", "\n"), "\n", 2)[0] == "-- chitu:nontransactional" {
			item.migration.NonTransactional = true
		}
		switch matches[3] {
		case "up":
			if item.upSeen {
				return nil, fmt.Errorf("migration: duplicate up file for %s", id)
			}
			item.migration.UpSQL = string(contents)
			item.upSeen = true
		case "down":
			if item.downSeen {
				return nil, fmt.Errorf("migration: duplicate down file for %s", id)
			}
			item.migration.DownSQL = string(contents)
			item.downSeen = true
		}
	}
	migrations := make([]Migration, 0, len(pairs))
	for id, item := range pairs {
		if !item.upSeen || !item.downSeen {
			return nil, fmt.Errorf("migration: %s must have both up and down files", id)
		}
		if err := item.migration.validate(); err != nil {
			return nil, err
		}
		migrations = append(migrations, item.migration)
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].ID() < migrations[j].ID() })
	return migrations, nil
}

// Files contains paths created for a migration.
type Files struct {
	Up   string
	Down string
}

// Create writes a new exclusive up/down pair and never overwrites files.
func Create(directory, name string, now time.Time) (Files, error) {
	if strings.TrimSpace(directory) == "" {
		return Files{}, errors.New("migration: directory must not be empty")
	}
	name = normalizeName(name)
	if !namePattern.MatchString(name) {
		return Files{}, errors.New("migration: name must contain ASCII letters, digits, or underscores and start with a letter")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return Files{}, fmt.Errorf("migration: create directory: %w", err)
	}
	base := now.UTC().Format("20060102150405") + "_" + name
	files := Files{
		Up:   filepath.Join(directory, base+".up.sql"),
		Down: filepath.Join(directory, base+".down.sql"),
	}
	if err := writeExclusive(files.Up, upTemplate); err != nil {
		return Files{}, err
	}
	if err := writeExclusive(files.Down, downTemplate); err != nil {
		_ = os.Remove(files.Up)
		return Files{}, err
	}
	return files, nil
}

func normalizeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var result strings.Builder
	underscore := false
	for _, char := range name {
		valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
		if valid {
			result.WriteRune(char)
			underscore = false
		} else if result.Len() > 0 && !underscore {
			result.WriteByte('_')
			underscore = true
		}
	}
	return strings.Trim(result.String(), "_")
}

func writeExclusive(path, contents string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("migration: create %q: %w", path, err)
	}
	if _, err := file.WriteString(contents); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("migration: write %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("migration: close %q: %w", path, err)
	}
	return nil
}
