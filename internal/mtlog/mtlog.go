// Package mtlog provides a best-effort, write-only invocation log.
//
// The log records one line per `mt` invocation, mirroring the bash mt.sh
// format:
//
//	[2026-05-10T15:24:33Z] pid=12345 INVOKE cmd=switch args=...
//	[2026-05-10T15:24:34Z] pid=12345 EXIT cmd=switch rc=0
//
// Writes are best-effort: any error (mkdir, open, write) is silently
// swallowed. The log is purely diagnostic — failure to write must never
// fail the caller, panic, or otherwise affect program behavior.
package mtlog

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Path resolves the on-disk log file location.
//
// Resolution order:
//  1. $MT_LOG, if set and non-empty.
//  2. $XDG_STATE_HOME/mt/mt.log, if XDG_STATE_HOME is set and non-empty.
//  3. $HOME/.local/state/mt/mt.log otherwise.
func Path() string {
	if v := os.Getenv("MT_LOG"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "mt", "mt.log")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Fall back to relative path; Write is best-effort and will simply
		// fail silently if this is unwritable.
		return filepath.Join(".local", "state", "mt", "mt.log")
	}
	return filepath.Join(home, ".local", "state", "mt", "mt.log")
}

// Write appends one line to the log file in the format:
//
//	[YYYY-MM-DDTHH:MM:SSZ] pid=N <line>\n
//
// All errors are silently swallowed. Write never returns an error and
// never panics, even if the parent directory cannot be created or the
// file cannot be opened for append.
func Write(line string) {
	defer func() {
		// Defensive: a panic here would defeat the best-effort contract.
		_ = recover()
	}()

	p := Path()
	dir := filepath.Dir(p)
	if dir != "" && dir != "." {
		// Ignore mkdir errors — a read-only parent is a valid runtime state
		// for this best-effort logger.
		_ = os.MkdirAll(dir, 0o755)
	}

	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	pid := strconv.Itoa(os.Getpid())

	// Single Write call to minimize the chance of interleaving when
	// multiple processes share the file. O_APPEND already guarantees
	// atomic appends on POSIX for writes <= PIPE_BUF.
	_, _ = f.WriteString("[" + ts + "] pid=" + pid + " " + line + "\n")
}

// Tail returns the last n complete lines from the log file.
//
// Returns an empty slice (never nil-with-error) if the file does not
// exist, cannot be opened, or contains fewer than n lines (in which case
// all available lines are returned). Trailing newlines are stripped from
// each returned line.
func Tail(n int) []string {
	if n <= 0 {
		return []string{}
	}

	f, err := os.Open(Path())
	if err != nil {
		return []string{}
	}
	defer f.Close()

	// Ring buffer of the last n lines. bufio.Scanner handles line
	// splitting and strips the trailing newline automatically.
	scanner := bufio.NewScanner(f)
	// Allow lines up to 1 MiB — log lines should be modest, but this
	// avoids surprises on long argv dumps.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	ring := make([]string, 0, n)
	for scanner.Scan() {
		line := scanner.Text()
		if len(ring) < n {
			ring = append(ring, line)
			continue
		}
		// Shift left by one, then append. n is small (caller-controlled,
		// typically 10-100), so the O(n) shift is fine.
		copy(ring, ring[1:])
		ring[len(ring)-1] = line
	}
	if err := scanner.Err(); err != nil {
		// Partial read is still useful for diagnose; return what we have.
		return ring
	}

	// Defensive: strip any stray trailing CR that bufio.Scanner left in
	// place on \r\n-terminated logs (Scanner only strips \n).
	for i, s := range ring {
		ring[i] = strings.TrimRight(s, "\r")
	}
	return ring
}
