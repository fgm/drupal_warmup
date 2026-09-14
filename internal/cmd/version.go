package cmd

import (
	"fmt"
	"runtime/debug"
)

// Version prints the version go build stamped into the binary.
func Version(rt *Runtime) int {
	v := "(devel)"
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		v = bi.Main.Version
	}
	_, _ = fmt.Fprintf(rt.Stdout, "drupal_warmup %s\n", v)
	return ExitOK
}
