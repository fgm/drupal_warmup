// Package cmd implements the commands: warm, list, debug and version.
//
// Each command takes the injected runtime and its own arguments
// and returns an exit status.
// Nothing here reads os.* or flag.CommandLine: main hands everything over.
package cmd

import (
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
)

// Exit statuses. They hold for the binary: go run collapses every non-zero status to 1.
const (
	// ExitOK reports that every listed URL answered 2xx in the last run.
	ExitOK = 0
	// ExitFailed reports a last run where a URL failed, was redirected without
	// --follow or was out of scope without --lax, or an empty enumeration.
	ExitFailed = 1
	// ExitUsage reports a bad command line, or a source that did not answer 200.
	ExitUsage = 2
	// ExitLogin reports a Drupal login the site did not accept.
	ExitLogin = 3
)

// Runtime is every ambient value the commands read, injected by main.
type Runtime struct {
	// configuration
	Env []string

	// services
	FS        fs.FS
	Log       *slog.Logger
	Stderr    io.Writer
	Stdin     io.Reader
	Stdout    io.Writer
	Transport http.RoundTripper
}

// OSFS is the OS filesystem behind an fs.FS.
//
// Not os.DirFS: fs.ValidPath rejects a leading slash and "..",
// and --list-file has to accept any path the OS does.
type OSFS struct{}

// Open opens a file the way os.Open does.
func (OSFS) Open(name string) (fs.File, error) {
	return os.Open(name)
}
