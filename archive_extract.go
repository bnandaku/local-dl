package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type archiveLimits struct {
	MaxEntries       int
	MaxExpandedBytes int64
	Timeout          time.Duration
}
type archiveErrorKind string

const (
	archiveIntrinsic   archiveErrorKind = "intrinsic"
	archiveOperational archiveErrorKind = "operational"
)

type archiveError struct {
	Kind archiveErrorKind
	Op   string
	Err  error
}

func (e *archiveError) Error() string { return fmt.Sprintf("%s archive %s: %v", e.Kind, e.Op, e.Err) }
func (e *archiveError) Unwrap() error { return e.Err }
func isArchiveIntrinsic(err error) bool {
	var e *archiveError
	return errors.As(err, &e) && e.Kind == archiveIntrinsic
}
func archiveFailure(kind archiveErrorKind, op string, err error) error {
	return &archiveError{kind, op, err}
}

var archiveTool string
var errArchiveLimit = errors.New("archive limit exceeded")

// Only unrar's documented CLI is supported. 7z has a different listing grammar.
func findArchiveTool() (string, error) {
	name := archiveTool
	if name == "" {
		name = "unrar"
	}
	p, e := exec.LookPath(name)
	if e != nil {
		return "", archiveFailure(archiveOperational, "tool", errors.New("unrar is unavailable"))
	}
	return p, nil
}

type archiveBuffer struct {
	buffer   bytes.Buffer
	max      int
	overflow bool
}

func (b *archiveBuffer) Write(p []byte) (int, error) {
	if len(p) > b.max-b.buffer.Len() {
		b.overflow = true
		return 0, errArchiveLimit
	}
	return b.buffer.Write(p)
}
func (b *archiveBuffer) String() string { return b.buffer.String() }
func (b *archiveBuffer) Bytes() []byte  { return b.buffer.Bytes() }

func archiveCommandError(ctx context.Context, op string, err error) error {
	if ctx.Err() != nil {
		return archiveFailure(archiveOperational, op, ctx.Err())
	}
	if errors.Is(err, errArchiveLimit) {
		return archiveFailure(archiveIntrinsic, op, errArchiveLimit)
	}
	var ex *exec.ExitError
	if errors.As(err, &ex) {
		// unrar: 3=CRC, 10=no files, 11=wrong password, 12=read error.
		// Exit 2 is ambiguous (fatal/tool/storage); preserve rather than blacklist.
		switch ex.ExitCode() {
		case 11:
			return archiveFailure(archiveIntrinsic, "password", fmt.Errorf("password protected archive"))
		case 3, 10:
			return archiveFailure(archiveIntrinsic, op, fmt.Errorf("unrar exit %d", ex.ExitCode()))
		}
	}
	return archiveFailure(archiveOperational, op, err)
}

func extractRAR(ctx context.Context, volumes []string, destination string, limits archiveLimits) (result []string, err error) {
	if limits.MaxEntries <= 0 {
		limits.MaxEntries = 10000
	}
	if limits.MaxExpandedBytes <= 0 {
		limits.MaxExpandedBytes = 100 << 30
	}
	if limits.Timeout <= 0 {
		limits.Timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	paths, err := normalizeVolumes(volumes)
	if err != nil {
		return nil, err
	}
	tool, err := findArchiveTool()
	if err != nil {
		return nil, err
	}
	// Isolate exactly the supplied volume set so unrar cannot discover undeclared siblings.
	volumeDir, err := os.MkdirTemp(filepath.Dir(paths[0]), ".rar-volumes-")
	if err != nil {
		return nil, archiveFailure(archiveOperational, "staging", err)
	}
	defer os.RemoveAll(volumeDir)
	for _, p := range paths {
		info, e := os.Lstat(p)
		if e != nil {
			return nil, archiveFailure(archiveOperational, "volume", e)
		}
		if !info.Mode().IsRegular() {
			return nil, archiveFailure(archiveIntrinsic, "volume", errors.New("volume is not a regular file"))
		}
		if e = os.Link(p, filepath.Join(volumeDir, filepath.Base(p))); e != nil {
			return nil, archiveFailure(archiveOperational, "staging", e)
		}
	}
	first := filepath.Join(volumeDir, filepath.Base(paths[0]))
	entries, err := listRAR(ctx, tool, first, limits)
	if err != nil {
		return nil, err
	}
	output := &archiveBuffer{max: 1 << 20}
	cmd := exec.CommandContext(ctx, tool, "t", "-idq", "-p-", "--", first)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Stdout = output
	cmd.Stderr = output
	if e := cmd.Run(); e != nil {
		if output.overflow {
			return nil, archiveFailure(archiveIntrinsic, "integrity", errArchiveLimit)
		}
		// Missing supplied final volumes are intrinsic only after the declared set was downloaded.
		text := strings.ToLower(output.String())
		if ctx.Err() == nil && strings.Contains(text, "password is incorrect") {
			return nil, archiveFailure(archiveIntrinsic, "password", errors.New("password protected archive"))
		}
		if ctx.Err() == nil && (strings.Contains(text, "next volume is required") || strings.Contains(text, "checksum error") || strings.Contains(text, "password is incorrect")) {
			return nil, archiveFailure(archiveIntrinsic, "integrity", errors.New("archive integrity test failed"))
		}
		return nil, archiveCommandError(ctx, "integrity", e)
	}
	// The caller owns destination. Reject symlink ancestors, and extract only within a new private child.
	if err = safeArchiveDestination(destination); err != nil {
		return nil, archiveFailure(archiveOperational, "destination", err)
	}
	stage, err := os.MkdirTemp(destination, ".rar-extracted-")
	if err != nil {
		return nil, archiveFailure(archiveOperational, "destination", err)
	}
	defer func() {
		if err != nil {
			os.RemoveAll(stage)
		}
	}()
	var written int64
	for _, entry := range entries {
		if entry.directory {
			continue
		}
		target := filepath.Join(stage, entry.path)
		if e := os.MkdirAll(filepath.Dir(target), 0700); e != nil {
			return nil, archiveFailure(archiveOperational, "destination", e)
		}
		f, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, archiveFailure(archiveOperational, "output", e)
		}
		cw := &archiveCountWriter{w: f, max: entry.size}
		cmd := exec.CommandContext(ctx, tool, "p", "-inul", "-p-", "--", first, entry.path)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		cmd.Stdout = cw
		runErr := cmd.Run()
		syncErr := f.Sync()
		closeErr := f.Close()
		if cw.err != nil {
			kind := archiveOperational
			if errors.Is(cw.err, errArchiveLimit) {
				kind = archiveIntrinsic
			}
			return nil, archiveFailure(kind, "output", cw.err)
		}
		if runErr != nil {
			return nil, archiveCommandError(ctx, "extract", runErr)
		}
		if syncErr != nil {
			return nil, archiveFailure(archiveOperational, "output", syncErr)
		}
		if closeErr != nil {
			return nil, archiveFailure(archiveOperational, "output", closeErr)
		}
		if cw.n != entry.size {
			return nil, archiveFailure(archiveIntrinsic, "extract", errors.New("expanded size differs from manifest"))
		}
		if cw.n > limits.MaxExpandedBytes-written {
			return nil, archiveFailure(archiveIntrinsic, "extract", errArchiveLimit)
		}
		written += cw.n
		result = append(result, target)
	}
	if len(result) == 0 {
		return nil, archiveFailure(archiveIntrinsic, "no_media", errors.New("archive contains no files"))
	}
	return result, nil
}

type archiveCountWriter struct {
	w      io.Writer
	n, max int64
	err    error
}

func (w *archiveCountWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.max-w.n {
		w.err = errArchiveLimit
		return 0, w.err
	}
	n, e := w.w.Write(p)
	w.n += int64(n)
	if e == nil && n != len(p) {
		e = io.ErrShortWrite
	}
	w.err = e
	return n, e
}

type archiveEntry struct {
	path, kind, attributes, ratio string
	size                          int64
	hasSize, directory            bool
}

func listRAR(ctx context.Context, tool, path string, limits archiveLimits) ([]archiveEntry, error) {
	out := &archiveBuffer{max: 8 << 20}
	cmd := exec.CommandContext(ctx, tool, "lt", "-v", "-p-", "--", path)
	diagnostic := &archiveBuffer{max: 1 << 20}
	cmd.Stdout, cmd.Stderr = out, diagnostic
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if e := cmd.Run(); e != nil {
		if out.overflow || diagnostic.overflow {
			return nil, archiveFailure(archiveIntrinsic, "list", errArchiveLimit)
		}
		message := strings.ToLower(diagnostic.String() + out.String())
		if ctx.Err() == nil && (strings.Contains(message, "bad archive") || strings.Contains(message, "corrupt archive") || strings.Contains(message, "checksum error")) {
			return nil, archiveFailure(archiveIntrinsic, "list", errors.New("archive is corrupt"))
		}
		return nil, archiveCommandError(ctx, "list", e)
	}
	return parseRARListing(out.Bytes(), limits)
}
func parseRARListing(data []byte, limits archiveLimits) ([]archiveEntry, error) {
	var entries []archiveEntry
	var cur *archiveEntry
	seen := map[string]int{}
	pending := map[string]bool{}
	var total int64
	invalid := func(s string) error { return archiveFailure(archiveIntrinsic, "manifest", errors.New(s)) }
	flush := func() error {
		if cur == nil {
			return nil
		}
		e := *cur
		cur = nil
		if !safeArchiveName(e.path) {
			return invalid("unsafe archive path")
		}
		e.directory = e.kind == "Directory"
		if e.kind != "File" && !e.directory {
			return invalid("unsupported archive entry type")
		}
		if e.attributes == "" {
			return invalid("missing archive attributes")
		}
		if strings.ContainsAny(e.attributes[:1], "lbcps") {
			return invalid("archive links and special files are forbidden")
		}
		if !e.hasSize && !e.directory {
			return invalid("missing archive entry size")
		}
		if e.size < 0 {
			return invalid("negative archive entry size")
		}
		if index, ok := seen[e.path]; ok {
			if !pending[e.path] || !strings.Contains(e.ratio, "<") || entries[index].size != e.size || entries[index].kind != e.kind {
				return invalid("duplicate or inconsistent archive entry")
			}
			pending[e.path] = strings.Contains(e.ratio, ">")
			return nil
		}
		if strings.Contains(e.ratio, "<") {
			return invalid("archive starts with a continuation")
		}
		if len(entries) >= limits.MaxEntries || e.size > limits.MaxExpandedBytes-total {
			return invalid("archive expansion limit exceeded")
		}
		total += e.size
		seen[e.path] = len(entries)
		pending[e.path] = strings.Contains(e.ratio, ">")
		entries = append(entries, e)
		return nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 65536)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "Name:") {
			if e := flush(); e != nil {
				return nil, e
			}
			cur = &archiveEntry{path: strings.TrimSpace(strings.TrimPrefix(line, "Name:"))}
			continue
		}
		if cur == nil {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch field {
		case "Type":
			cur.kind = value
		case "Size":
			n, e := strconv.ParseInt(value, 10, 64)
			if e != nil {
				return nil, invalid("invalid archive entry size")
			}
			cur.size = n
			cur.hasSize = true
		case "Attributes":
			cur.attributes = value
		case "Ratio":
			cur.ratio = value
		case "Target", "Link target", "Hard link", "Symbolic link":
			return nil, invalid("archive links are forbidden")
		}
	}
	if scanner.Err() != nil {
		return nil, invalid("archive listing is incomplete")
	}
	if e := flush(); e != nil {
		return nil, e
	}
	for _, incomplete := range pending {
		if incomplete {
			return nil, archiveFailure(archiveIntrinsic, "volumes", errors.New("archive volume set is incomplete"))
		}
	}
	if len(entries) == 0 {
		return nil, archiveFailure(archiveIntrinsic, "no_media", errors.New("archive contains no entries"))
	}
	return entries, nil
}
func safeArchiveName(p string) bool {
	if p == "" || filepath.IsAbs(p) || filepath.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "-") {
		return false
	}
	if strings.ContainsAny(p, "\\:*?[]\x00\r\n") {
		return false
	}
	for _, r := range p {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
func safeArchiveDestination(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("destination must be absolute")
	}
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if e != nil {
			return e
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("destination contains a non-directory or symlink")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}

var rarPartPattern = regexp.MustCompile(`(?i)^(.*)\.part([0-9]+)\.rar$`)
var rarLegacyPattern = regexp.MustCompile(`(?i)^(.*)\.([r-z])([0-9]{2})$`)

func normalizeVolumes(in []string) ([]string, error) {
	bad := func() ([]string, error) {
		return nil, archiveFailure(archiveIntrinsic, "volumes", errors.New("incomplete or mixed archive volume set"))
	}
	if len(in) == 0 {
		return bad()
	}
	type volume struct {
		path, base, style string
		n                 int
	}
	var all []volume
	dir := filepath.Dir(in[0])
	for _, p := range in {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || filepath.Dir(p) != dir {
			return bad()
		}
		name := filepath.Base(p)
		v := volume{path: p}
		if m := rarPartPattern.FindStringSubmatch(name); m != nil {
			v.base = m[1]
			v.style = "part"
			n, e := strconv.Atoi(m[2])
			if e != nil || n < 1 {
				return bad()
			}
			v.n = n - 1
		} else if m := rarLegacyPattern.FindStringSubmatch(name); m != nil {
			v.base = m[1]
			v.style = "legacy"
			n, _ := strconv.Atoi(m[3])
			v.n = 1 + int(strings.ToLower(m[2])[0]-'r')*100 + n
		} else if strings.HasSuffix(strings.ToLower(name), ".rar") {
			v.base = name[:len(name)-4]
			v.style = "legacy"
		} else {
			return bad()
		}
		if v.base == "" {
			return bad()
		}
		all = append(all, v)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].n < all[j].n })
	out := make([]string, len(all))
	for j, v := range all {
		if v.n != j || v.base != all[0].base || v.style != all[0].style {
			return bad()
		}
		out[j] = v.path
	}
	return out, nil
}
