package command

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// upgradeRepoURL is the GitHub releases base. install.sh uses the same
// `releases/latest/download/` redirect; mt upgrade resolves the redirect
// to capture the resolved tag for the current/latest comparison.
const upgradeRepoURL = "https://github.com/jinyuanlu/metatree"

// RunUpgrade replaces the running binary with the latest release from
// GitHub. Flow (see plan §E):
//
//  1. Resolve the running binary path via os.Executable. Refuse if it's
//     not a regular owner-writable file (the dev wrapper at ./bin/mt is
//     a shell script, NOT a Mach-O binary — replacing it with a
//     tarball-extracted binary would silently break dev workflows).
//
//  2. Resolve the latest tag by following GitHub's
//     /releases/latest/download/<asset> redirect — no API auth needed.
//
//  3. If latest == current (Version), report "already on vX.Y.Z" unless
//     --force is passed. --check exits here without downloading.
//
//  4. Download asset + checksums.txt. Verify SHA-256 strictly (fail
//     closed) — install.sh skips on missing tooling, but in Go we have
//     crypto/sha256 unconditionally so there's no excuse.
//
//  5. Extract `mt` from the tarball to a sibling tempfile next to the
//     running binary. Atomic rename over self.
//
// On POSIX, renaming over a running executable works because the kernel
// keeps the original mmap'd until the process exits. The next invocation
// loads the new bytes. We rely on this — no two-stage upgrade.
func RunUpgrade(env *Env, args []string) error {
	checkOnly := false
	force := false
	for _, a := range args {
		switch a {
		case "--check":
			checkOnly = true
		case "--force":
			force = true
		case "-h", "--help":
			fmt.Fprint(env.Stdout, upgradeUsage)
			return nil
		default:
			return ExitWith(2, "unknown upgrade flag: %s (try `mt upgrade --help`)", a)
		}
	}

	self, err := os.Executable()
	if err != nil {
		return ExitWith(1, "resolve self path: %v", err)
	}
	// Resolve any symlinks so the rename target is the actual file (a
	// homebrew-style symlink-to-cellar would make the replacement
	// invisible without resolving).
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}

	if err := assertOwnerWritableBinary(self); err != nil {
		return ExitWith(1, "%v", err)
	}

	asset, err := pickAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return ExitWith(1, "%v", err)
	}

	currentTag := strings.TrimSpace(Version)
	latestTag, err := resolveLatestTag(asset)
	if err != nil {
		return ExitWith(1, "resolve latest release: %v", err)
	}
	fmt.Fprintf(env.Stderr, "mt: current=%s  latest=%s\n", displayTag(currentTag), latestTag)

	if checkOnly {
		return nil
	}
	if !force && tagsEqual(currentTag, latestTag) {
		fmt.Fprintf(env.Stderr, "mt: already on %s — pass --force to reinstall.\n", latestTag)
		return nil
	}

	tmpDir, err := os.MkdirTemp("", "mt-upgrade-*")
	if err != nil {
		return ExitWith(1, "tempdir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tarballName := asset + ".tar.gz"
	tarballPath := filepath.Join(tmpDir, tarballName)
	checksumsPath := filepath.Join(tmpDir, "checksums.txt")

	if err := download(env.Stderr, urlForAsset(tarballName), tarballPath); err != nil {
		return ExitWith(1, "download tarball: %v", err)
	}
	if err := download(env.Stderr, urlForAsset("checksums.txt"), checksumsPath); err != nil {
		return ExitWith(1, "download checksums: %v", err)
	}
	if err := verifyChecksum(tarballPath, tarballName, checksumsPath); err != nil {
		return ExitWith(1, "%v", err)
	}

	// Extract to a sibling tempfile of the running binary so the rename
	// is on the same filesystem (atomic on POSIX). Picking /tmp would
	// risk a cross-fs rename, which os.Rename refuses on Linux.
	stagePath := self + ".upgrade.tmp"
	if err := extractMt(tarballPath, stagePath); err != nil {
		return ExitWith(1, "extract: %v", err)
	}
	defer os.Remove(stagePath) // no-op if rename succeeded

	if err := os.Chmod(stagePath, 0o755); err != nil {
		return ExitWith(1, "chmod staged binary: %v", err)
	}
	if err := os.Rename(stagePath, self); err != nil {
		return ExitWith(1, "atomic replace %s: %v", self, err)
	}

	// Sanity: run the new binary's --version to confirm. If it crashes,
	// we report — but the upgrade itself has already happened (the user
	// can roll back with `mt upgrade --force` once a fix lands).
	out, err := exec.Command(self, "--version").Output()
	if err != nil {
		fmt.Fprintf(env.Stderr,
			"mt: replaced %s but new binary --version failed (%v); reinstall via install.sh if needed.\n",
			self, err)
		return ExitWith(1, "post-upgrade --version failed: %v", err)
	}
	fmt.Fprintf(env.Stderr, "mt: upgraded → %s", out)
	return nil
}

const upgradeUsage = `mt upgrade — replace this binary with the latest GitHub release

Usage:
  mt upgrade           # download + verify + atomic replace
  mt upgrade --check   # report current vs latest, no download
  mt upgrade --force   # reinstall even if current == latest

The binary at the resolved-symlink path is overwritten via temp+rename.
Your config at ~/.metatree/config.toml is never touched.
`

// pickAsset maps the Go runtime OS/arch onto the goreleaser asset
// names used by .goreleaser.yaml. Mirrors install.sh's case statement.
func pickAsset(goos, goarch string) (string, error) {
	switch goos {
	case "darwin":
		switch goarch {
		case "arm64":
			return "mt-darwin-arm64", nil
		case "amd64":
			return "mt-darwin-amd64", nil
		}
	case "linux":
		switch goarch {
		case "arm64":
			return "mt-linux-arm64", nil
		case "amd64":
			return "mt-linux-amd64", nil
		}
	}
	return "", fmt.Errorf("unsupported platform: %s/%s — install manually from %s/releases", goos, goarch, upgradeRepoURL)
}

func urlForAsset(name string) string {
	return upgradeRepoURL + "/releases/latest/download/" + name
}

// assertOwnerWritableBinary refuses to upgrade a path that's a symlink
// to a system location (homebrew cellar, /usr/local, …) the user
// doesn't own, OR a script (install.sh-installed binaries are Mach-O/
// ELF; the dev wrapper at ./bin/mt is a 4-line bash script we MUST
// NOT replace with a tarball binary).
func assertOwnerWritableBinary(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	// Check user-writable via a probe: open O_WRONLY|O_APPEND with the
	// existing perms, immediately close. Avoids hardcoding a uid check
	// that misbehaves under sudo / inside containers.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, fi.Mode())
	if err != nil {
		return fmt.Errorf("%s is not writable by you (try install.sh): %w", path, err)
	}
	_ = f.Close()

	// Refuse shell-script wrappers. The dev tree's bin/mt starts with
	// "#!/usr/bin/env bash" — replacing it with a Mach-O blob would
	// break the developer's workflow. Magic-byte sniff.
	head := make([]byte, 4)
	rf, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	n, _ := io.ReadFull(rf, head)
	_ = rf.Close()
	if n >= 2 && head[0] == '#' && head[1] == '!' {
		return fmt.Errorf("%s looks like a script wrapper, not a release binary; reinstall via install.sh", path)
	}
	return nil
}

// resolveLatestTag captures the first redirect from
// /releases/latest/download/<asset>, which goes to
// /releases/download/<tag>/<asset> — the only hop that contains the
// tag in the URL. Following further redirects loses the tag (GitHub
// rewrites to a CDN URL with signed querystring).
//
// We use a custom http.Client with CheckRedirect that stops at the
// first 302 and returns http.ErrUseLastResponse; that returns the
// 302 response itself (with its Location header) instead of an error.
func resolveLatestTag(asset string) (string, error) {
	cl := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest(http.MethodHead, urlForAsset(asset+".tar.gz"), nil)
	if err != nil {
		return "", err
	}
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusMovedPermanently {
		return "", fmt.Errorf("HEAD %s: HTTP %d (expected 302 redirect to tagged URL)", req.URL, resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("HEAD %s: missing Location header", req.URL)
	}
	parsed, err := url.Parse(loc)
	if err != nil {
		return "", fmt.Errorf("parse Location %q: %w", loc, err)
	}
	return tagFromAssetURL(parsed)
}

// tagFromAssetURL pulls the v* segment out of a resolved download path
// like /<owner>/<repo>/releases/download/v1.2.3/<asset>.tar.gz.
func tagFromAssetURL(u *url.URL) (string, error) {
	parts := strings.Split(u.Path, "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "releases" && parts[i+1] == "download" {
			tag := parts[i+2]
			if tag != "" {
				return tag, nil
			}
		}
	}
	return "", fmt.Errorf("could not parse tag from %s", u)
}

// displayTag normalizes the build-stamped Version (which may be
// `dev`, a `git describe` output like `v1.0.3-2-gabc123-dirty`, or a
// raw tag) into something readable in upgrade output.
func displayTag(v string) string {
	if v == "" {
		return "(unknown)"
	}
	return v
}

// tagsEqual treats Version=`v1.0.3` and Version=`1.0.3` as the same,
// and Version=`v1.0.3-...-dirty` as NOT equal (a dirty dev build
// should always reinstall when asked). Belt and braces — you'd be
// surprised what shows up here in practice.
func tagsEqual(current, latest string) bool {
	c := strings.TrimPrefix(current, "v")
	l := strings.TrimPrefix(latest, "v")
	return c == l
}

// download fetches url to path with a 5-minute timeout. Reports a
// one-line "fetching X" on stderr so the user sees something during
// the (typically <2s) download.
func download(stderr io.Writer, url, path string) error {
	fmt.Fprintf(stderr, "mt: fetching %s\n", url)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	cl := &http.Client{Timeout: 5 * time.Minute}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return err
	}
	return nil
}

// verifyChecksum looks up the SHA-256 for tarballName in checksumsPath
// (goreleaser format: "<sha256>  <filename>") and compares against the
// computed digest. Strict — no missing-line, no missing-tool fallback.
func verifyChecksum(tarballPath, tarballName, checksumsPath string) error {
	want, err := lookupChecksum(checksumsPath, tarballName)
	if err != nil {
		return err
	}
	f, err := os.Open(tarballPath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s want %s", tarballName, got, want)
	}
	return nil
}

// lookupChecksum scans checksums.txt for the line matching name and
// returns the hex digest. Format is `<digest>  <name>` (two spaces),
// matching goreleaser's default.
func lookupChecksum(path, name string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == name {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum line for %s in %s", name, path)
}

// extractMt opens tarballPath as gzip+tar and writes the `mt` entry to
// outPath. Refuses anything fishy: paths with .. components, absolute
// paths, non-regular entries.
func extractMt(tarballPath, outPath string) error {
	f, err := os.Open(tarballPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Base(hdr.Name)
		if name != "mt" {
			continue
		}
		out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			_ = out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("no `mt` entry in %s", tarballPath)
}
