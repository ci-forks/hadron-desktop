package main

// `isotope backup --export FILE`: the backup of this machine as one file.
// The file is a tar of a restic repository made for it alone, so it is
// encrypted and compressed like any other backup, and `isotope restore
// --from FILE` reads it with the same code.

import (
	"archive/tar"
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func backupExport(p backupPaths, file string, containers, passStdin bool, in *bufio.Reader, stdout, stderr io.Writer) int {
	if err := exportTo(p, file, containers, passStdin, in, stdout, stderr); err != nil {
		if errors.Is(err, errPartial) {
			fmt.Fprintf(stderr, "isotope: exported, but %v.\n", err)
			return exitOK
		}
		fmt.Fprintln(stderr, "isotope: export:", err)
		return exitError
	}
	return exitOK
}

func exportTo(p backupPaths, file string, containers, passStdin bool, in *bufio.Reader, stdout, stderr io.Writer) error {
	file, err := normalizeTarget(file)
	if err != nil || isURLTarget(file) {
		return fmt.Errorf("%q is not an absolute file path", file)
	}
	if exists(file) {
		return fmt.Errorf("%s already exists", file)
	}
	dir := filepath.Dir(file)
	if err := checkOnDisk(dir); err != nil {
		return err
	}

	// The machine's backup password when it has one, which the user already
	// keeps; otherwise a new one, shown once.
	var pw string
	if b, err := os.ReadFile(p.password()); err == nil && !passStdin {
		pw = strings.TrimSpace(string(b))
		fmt.Fprintln(stdout, "The file is encrypted with this machine's backup password.")
	} else if pw, err = newPassword(passStdin, in, stdout); err != nil {
		return err
	}

	// The repository is made in a directory only root can reach, on the
	// persistent partition, then packed into the file. Not next to the file:
	// a user who owns that directory could swap ours for a link.
	if err := os.MkdirAll(p.State, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(p.State, "work-")
	if err != nil {
		return err
	}
	partial := file + ".partial"
	passFile := filepath.Join(p.Run, "password.export")
	clean := func() {
		_ = os.RemoveAll(work)
		_ = os.Remove(partial)
		_ = os.Remove(passFile)
	}
	stop := onInterrupt(clean)
	defer stop()
	defer clean()
	if err := writeSecret(passFile, pw); err != nil {
		return err
	}
	cmd, err := resticCmd(p, work, passFile, "init")
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = io.Discard, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restic init: %w", err)
	}
	backupErr := backupOnce(p, work, passFile, containers, false, stdout, stderr)
	if backupErr != nil && !errors.Is(backupErr, errPartial) {
		return backupErr
	}
	if err := packDir(work, partial); err != nil {
		return err
	}
	if err := os.Rename(partial, file); err != nil {
		return err
	}
	size := ""
	if fi, err := os.Stat(file); err == nil {
		size = fmt.Sprintf(" (%.1f MB)", float64(fi.Size())/1e6)
	}
	fmt.Fprintf(stdout, "Wrote %s%s. Restore it with `isotope restore --from %s`.\n", file, size, file)
	return backupErr
}

// packDir writes the files under dir into a tar at out, relative to dir.
// Only directories and regular files: a restic repository has nothing else.
func packDir(dir, out string) error {
	// O_EXCL also refuses a link a user put at that name.
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(f)
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("%s: not a file or a directory", path)
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		h.Uname, h.Gname, h.Uid, h.Gid = "", "", 0, 0
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.Mode().IsDir() {
			return nil
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(tw, src)
		return err
	})
	if err == nil {
		err = tw.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = syncPath(out)
	}
	return err
}

func syncPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// unpackFile extracts an exported backup into dir, a new directory under
// /usr/local/isotope-backup, which only root can change. Names that are absolute, climb out with "..", or are
// anything but a directory or a regular file are refused.
func unpackFile(file, dir string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(bufio.NewReader(f))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s is not an exported backup: %w", file, err)
		}
		name := filepath.FromSlash(h.Name)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%s: refusing the entry %q", file, h.Name)
		}
		path := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: refusing the entry %q: not a file or a directory", file, h.Name)
		}
	}
}
