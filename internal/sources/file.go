package sources

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// File enumerates a plain text list, one URL or path per line.
//
// Blank lines and lines starting with # are skipped,
// which lets the output of the list command feed a later run unchanged.
// A line with a scheme is taken as is;
// anything else is a path joined onto the base path.
type File struct {
	Base *url.URL
	In   io.Reader
}

// Entries reads the list to its end.
func (f *File) Entries(_ context.Context) ([]Entry, error) {
	scope := baseScope(f.Base)
	var entries []Entry
	sc := bufio.NewScanner(f.In)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		u, err := f.resolve(line)
		if err != nil {
			return nil, fmt.Errorf("line %q: %w", line, err)
		}
		// The base is the only scope a list has.
		entries = append(entries, check(u, scope, scope))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading list: %w", err)
	}
	return entries, nil
}

func (f *File) resolve(line string) (*url.URL, error) {
	rel, err := url.Parse(line)
	if err != nil {
		return nil, err
	}
	if rel.IsAbs() {
		return rel, nil
	}
	return joinBase(f.Base, rel), nil
}
