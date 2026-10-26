package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPackageDBFingerprintMissingFiles(t *testing.T) {
	dir := t.TempDir()
	if _, ok := packageDBFingerprint([]string{filepath.Join(dir, "nope")}); ok {
		t.Error("fingerprint reported ok with no database files present")
	}
}

func TestPackageDBFingerprintStableWithoutChanges(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "rpmdb.sqlite")
	shm := filepath.Join(dir, "rpmdb.sqlite-shm")
	for _, f := range []string{db, shm} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{db}

	before, ok := packageDBFingerprint(files)
	if !ok {
		t.Fatal("fingerprint not ok with the database present")
	}

	// A read touching only the WAL index must not look like a transaction.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(shm, later, later); err != nil {
		t.Fatal(err)
	}
	if after, _ := packageDBFingerprint(files); after != before {
		t.Errorf("fingerprint changed without a database write: %q -> %q", before, after)
	}

	if err := os.Chtimes(db, later, later); err != nil {
		t.Fatal(err)
	}
	if after, _ := packageDBFingerprint(files); after == before {
		t.Error("fingerprint unchanged after the database was modified")
	}
}

func TestPackageDBFingerprintSeesFileAppearing(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "rpmdb.sqlite")
	wal := filepath.Join(dir, "rpmdb.sqlite-wal")
	if err := os.WriteFile(db, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := []string{db, wal}

	before, _ := packageDBFingerprint(files)
	if err := os.WriteFile(wal, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after, _ := packageDBFingerprint(files); after == before {
		t.Error("fingerprint unchanged after the WAL appeared")
	}
}
