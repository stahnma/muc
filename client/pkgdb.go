package main

import (
	"fmt"
	"os"
	"strings"
)

// packageDBFiles are the files a package transaction is certain to change.
//
// muc-client-recheck.path watches the whole /var/lib/rpm and /var/lib/dpkg
// directories, which also catches reads: any query against rpm's SQLite backend
// (Fedora 33+, EL9+) writes the WAL index, rpmdb.sqlite-shm, even a read-only
// one. Our own update check is such a query, so without a filter every check
// signals the next one, about every 30s forever on an idle host.
//
// The -wal file is listed alongside the database because a transaction's
// writes can sit there until a checkpoint. -shm is deliberately absent.
// Packages is the Berkeley DB backend older rpm releases use.
var packageDBFiles = []string{
	"/var/lib/rpm/rpmdb.sqlite",
	"/var/lib/rpm/rpmdb.sqlite-wal",
	"/var/lib/rpm/Packages",
	"/var/lib/dpkg/status",
}

// packageDBFingerprint summarizes the size and mtime of whichever of files
// exist. ok is false when none do: on a host without rpm or dpkg there is
// nothing to compare, and callers must not treat that as "unchanged".
func packageDBFingerprint(files []string) (fp string, ok bool) {
	var b strings.Builder
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		ok = true
		fmt.Fprintf(&b, "%s:%d:%d;", f, info.Size(), info.ModTime().UnixNano())
	}
	return b.String(), ok
}
