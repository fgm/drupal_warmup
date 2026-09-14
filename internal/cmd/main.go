package cmd

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
)

// NewLogger maps the verbosity to a level: 0 is silent, 1 warnings, 2 progress, 3 requests.
func NewLogger(stderr io.Writer, verbosity int) *slog.Logger {
	if verbosity <= 0 {
		return slog.New(slog.DiscardHandler)
	}
	levels := map[int]slog.Level{1: slog.LevelWarn, 2: slog.LevelInfo}
	level, ok := levels[verbosity]
	if !ok {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
}

// RealMain is the composition root: it takes every ambient value the program reads.
//
// It parses the global flags, builds the logger and dispatches on the command.
func RealMain(ctx context.Context, args, env []string,
	stdin io.Reader, stdout, stderr io.Writer,
	fsys fs.FS, transport http.RoundTripper,
) int {
	fs := flag.NewFlagSet("drupal_warmup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	help := fs.Bool("h", false, "Print this message on standard output and exit")
	verbosity := fs.Int("v", 1, "Verbosity on standard error: 0 silent, 1 warnings, 2 progress, 3 requests")
	fs.Usage = func() {
		_, _ = fmt.Fprint(fs.Output(), "Usage: drupal_warmup [-v N] <command> [flags]\n\n"+
			"The commands are:\n"+
			"  warm     enumerate the site and fetch every page\n"+
			"  list     enumerate the site and print the URLs, fetching none\n"+
			"  debug    fetch URLs one at a time with the Xdebug trigger set\n"+
			"  version  print the version\n\n"+
			"Each command takes -h. The global flags are:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args[1:]); err != nil {
		_, _ = fmt.Fprintf(stderr, "drupal_warmup: %v\n", err)
		fs.SetOutput(stderr)
		fs.Usage()
		return ExitUsage
	}
	if *help {
		fs.SetOutput(stdout)
		fs.Usage()
		return ExitOK
	}
	if fs.NArg() == 0 {
		_, _ = fmt.Fprintln(stderr, "drupal_warmup: no command")
		fs.SetOutput(stderr)
		fs.Usage()
		return ExitUsage
	}

	rt := Runtime{
		Env:       env,
		FS:        fsys,
		Log:       NewLogger(stderr, *verbosity),
		Stderr:    stderr,
		Stdin:     stdin,
		Stdout:    stdout,
		Transport: transport,
	}
	name, rest := fs.Arg(0), fs.Args()[1:]
	switch name {
	case "debug":
		return Debug(ctx, &rt, rest)
	case "list":
		return List(ctx, &rt, rest)
	case "version":
		return Version(&rt)
	case "warm":
		return Warm(ctx, &rt, rest)
	default:
		_, _ = fmt.Fprintf(stderr, "drupal_warmup: unknown command %q\n", name)
		fs.SetOutput(stderr)
		fs.Usage()
		return ExitUsage
	}
}
