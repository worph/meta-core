package snapshot

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// PluginOutputArchivePrefix is where plugin-output files live inside the ZIP.
// Trailing slash because ZIP entry names are forward-slash relative paths.
const PluginOutputArchivePrefix = "cache/plugin-output/"

// addPluginOutput walks srcDir (typically $FILES_PATH/plugin) and copies every
// file under it into zw at PluginOutputArchivePrefix + relPath.
//
// Returns the number of files added. A missing srcDir is not an error: it
// just means no plugin has produced output yet, so the section stays empty.
func addPluginOutput(zw *zip.Writer, srcDir string) (int, error) {
	info, err := os.Stat(srcDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", srcDir, err)
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("%s is not a directory", srcDir)
	}

	count := 0
	err = filepath.WalkDir(srcDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		entry := PluginOutputArchivePrefix + filepath.ToSlash(rel)

		w, err := zw.Create(entry)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(w, f); err != nil {
			return err
		}
		count++
		return nil
	})
	if err != nil {
		return count, fmt.Errorf("walk %s: %w", srcDir, err)
	}
	return count, nil
}

// extractPluginOutput pulls every entry under PluginOutputArchivePrefix from
// the ZIP and writes it under dstDir. Existing files are overwritten when
// overwrite=true; otherwise they're left alone (treats existing files as the
// "mine wins" choice — same toggle the metadata import uses).
//
// Path traversal is rejected: any entry whose relative path resolves outside
// dstDir is skipped with a warning.
func extractPluginOutput(zr *zip.Reader, dstDir string, overwrite bool) (written int, skipped int, err error) {
	if dstDir == "" {
		return 0, 0, fmt.Errorf("plugin-output destination dir is empty")
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return 0, 0, fmt.Errorf("mkdir %s: %w", dstDir, err)
	}

	cleanRoot, err := filepath.Abs(dstDir)
	if err != nil {
		return 0, 0, fmt.Errorf("abs %s: %w", dstDir, err)
	}

	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, PluginOutputArchivePrefix) {
			continue
		}
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		rel := strings.TrimPrefix(f.Name, PluginOutputArchivePrefix)
		dst := filepath.Join(cleanRoot, filepath.FromSlash(rel))

		// Reject anything that escapes the dst root.
		if !strings.HasPrefix(dst, cleanRoot+string(os.PathSeparator)) && dst != cleanRoot {
			log.Printf("[Snapshot] import: skipping path-escaping entry %q", f.Name)
			skipped++
			continue
		}

		if !overwrite {
			if _, err := os.Stat(dst); err == nil {
				skipped++
				continue
			}
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return written, skipped, fmt.Errorf("mkdir for %s: %w", dst, err)
		}

		if err := writeZipFile(f, dst); err != nil {
			return written, skipped, err
		}
		written++
	}
	return written, skipped, nil
}

func writeZipFile(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open zip entry %s: %w", f.Name, err)
	}
	defer rc.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}
