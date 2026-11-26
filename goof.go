// Command goof serves a file or a directory over HTTP, or downloads a file
// from a URL. It is a Go implementation of Simon Budig's woof.
package main

import (
	"archive/zip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// servesOnce is how many times a file is served unless -c says otherwise.
	servesOnce = 1

	// shutdownGrace bounds how long we wait for an in-flight transfer to
	// finish once the serve count is reached or a signal arrives.
	shutdownGrace = 10 * time.Second

	// headerTimeout bounds how long a download may take to return its
	// response headers. The body itself is unlimited.
	headerTimeout = 30 * time.Second
)

// errUsage marks command-line mistakes so main can exit with status 2, the
// convention the flag package itself follows for -h.
var errUsage = errors.New("usage")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	switch {
	case err == nil:
	case errors.Is(err, errUsage):
		// The message (and usage) has already been printed.
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "goof: %v\n", err)
		os.Exit(1)
	}
}

// run performs one invocation of goof. It returns errUsage for bad arguments
// and any other error for failures, which keeps main testable.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("goof", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		ip     = fs.String("i", "127.0.0.1", "-i <ip_addr>.    The address to serve the file or directory from.")
		port   = fs.String("p", "8080", "-p <port>.       The port to serve the file or directory from. Use 0 for any free port.")
		serves = fs.Int("c", servesOnce, "-c <count>.      How many times the file or directory will get served.")
		self   = fs.Bool("s", false, "-s.              When specified, goof will distribute/serve itself.")
	)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `%s serves a single file, or a zip archive of a directory, over HTTP.

The file is served <count> times, then %s exits. If a URL is given instead,
%s downloads it into the current directory.

Usage:
  %s [flags] <file|directory|url>

Flags:
`, fs.Name(), fs.Name(), fs.Name(), fs.Name())
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errUsage
	}

	rest := fs.Args()
	switch {
	case *self && len(rest) > 0:
		fmt.Fprintln(stderr, "It is possible to either serve goof itself or a file, but not both.")
		fs.Usage()
		return errUsage
	case !*self && len(rest) == 0:
		fmt.Fprintln(stderr, "You need to specify a file/directory to serve or a URL to download from.")
		fs.Usage()
		return errUsage
	case !*self && len(rest) > 1:
		fmt.Fprintln(stderr, "Can only serve single files/directories.")
		fs.Usage()
		return errUsage
	case *serves < 1:
		fmt.Fprintln(stderr, "-c must be at least 1.")
		return errUsage
	}

	var target string
	if *self {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locating own binary: %w", err)
		}
		target = exe
	} else {
		target = rest[0]
	}

	// A URL argument turns goof into a download client.
	if u, ok := parseHTTPURL(target); ok {
		return download(ctx, u.String(), downloadName(u), stderr)
	}

	diskPath, urlPath, cleanup, err := prepareTarget(target, stderr)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := serve(ctx, net.JoinHostPort(*ip, *port), diskPath, urlPath, *serves, stdout); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Exiting gracefully...")
	return nil
}

// prepareTarget resolves the argument to the file that will be served on disk
// and the path it will be reachable at. Directories are archived into a
// temporary directory; the returned cleanup removes that directory.
func prepareTarget(target string, out io.Writer) (diskPath, urlPath string, cleanup func(), err error) {
	info, err := os.Stat(target)
	if err != nil {
		return "", "", func() {}, err
	}
	if info.IsDir() {
		// Resolve relative names such as "." or ".." so the archive is named
		// after the real directory and its entries carry that prefix.
		if target, err = filepath.Abs(target); err != nil {
			return "", "", func() {}, err
		}
	}
	if !info.IsDir() {
		return target, "/" + filepath.Base(target), func() {}, nil
	}

	tmpDir, err := os.MkdirTemp("", "goof-archive-")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup = func() { os.RemoveAll(tmpDir) }

	base := filepath.Base(target)
	if base == "." || base == ".." || base == string(filepath.Separator) {
		base = "root" // a filesystem root has no name of its own
	}
	archiveName := base + ".zip"
	diskPath = filepath.Join(tmpDir, archiveName)
	if err := zipDirectory(target, diskPath); err != nil {
		return "", "", cleanup, fmt.Errorf("archiving %s: %w", target, err)
	}
	fmt.Fprintf(out, "Archived %s to %s\n", target, archiveName)
	return diskPath, "/" + archiveName, cleanup, nil
}

// serve listens on addr and serves diskPath until count complete downloads
// have been made, ctx is cancelled, or the listener fails. The listener stays
// bound for the whole run, so consecutive serves never hit a closed port.
func serve(ctx context.Context, addr, diskPath, urlPath string, count int, out io.Writer) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	// done is signalled once the last requested download has been written in
	// full, so 404s, aborted transfers and probes do not count.
	done := make(chan struct{}, 1)
	remaining := &atomic.Int64{}
	remaining.Store(int64(count))

	mux := http.NewServeMux()
	mux.Handle("/", serveHandler(diskPath, urlPath, remaining, done))

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	displayURL := (&url.URL{Scheme: "http", Host: ln.Addr().String(), Path: urlPath}).String()
	fmt.Fprintf(out, "Now serving on %s\n", displayURL)

	select {
	case <-done:
	case <-ctx.Done():
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serving %s: %w", diskPath, err)
		}
		return nil
	}

	// Shut down with a fresh context: ctx may be the cancelled signal context.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	<-serveErr
	return nil
}

// serveHandler serves diskPath at urlPath, decrementing remaining for every
// transfer that leaves the client with the end of the file and signalling done
// when it reaches zero.
func serveHandler(diskPath, urlPath string, remaining *atomic.Int64, done chan<- struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != urlPath {
			http.NotFound(w, r)
			return
		}

		f, err := os.Open(diskPath)
		if err != nil {
			http.Error(w, "cannot open file", http.StatusInternalServerError)
			return
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			http.Error(w, "cannot stat file", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
			"filename": filepath.Base(diskPath),
		}))

		// ServeContent handles Range requests, Content-Length, conditional
		// requests and MIME type detection by extension.
		rec := &recordingWriter{ResponseWriter: w}
		http.ServeContent(rec, r, info.Name(), info.ModTime(), f)

		if rec.completed(info.Size()) {
			if remaining.Add(-1) <= 0 {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		}
	})
}

// recordingWriter tracks the response status and body size so a serve can be
// counted only when the client received the whole file.
type recordingWriter struct {
	http.ResponseWriter
	status  int
	written int64
	lastEnd int64 // last byte offset of a Content-Range response, or -1
}

// completed reports whether the client received the end of the file: either
// the full body, or the tail of a resumed ranged request.
func (w *recordingWriter) completed(size int64) bool {
	switch w.status {
	case http.StatusOK:
		return w.written == size
	case http.StatusPartialContent:
		return w.written > 0 && w.lastEnd == size-1
	}
	return false
}

func (w *recordingWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.lastEnd = contentRangeEnd(w.Header().Get("Content-Range"))
	}
	w.ResponseWriter.WriteHeader(status)
}

// contentRangeEnd returns the last byte offset of a "bytes start-end/size"
// header, or -1 when it is absent or malformed.
func contentRangeEnd(v string) int64 {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "bytes "))
	slash := strings.LastIndex(v, "/")
	if slash < 0 {
		return -1
	}
	dash := strings.Index(v[:slash], "-")
	if dash < 0 {
		return -1
	}
	end, err := strconv.ParseInt(v[dash+1:slash], 10, 64)
	if err != nil {
		return -1
	}
	return end
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.written += int64(n)
	return n, err
}

// ReadFrom preserves the sendfile fast path that net/http offers when the
// ResponseWriter implements io.ReaderFrom.
func (w *recordingWriter) ReadFrom(src io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(src)
		w.written += n
		return n, err
	}
	n, err := io.Copy(writerOnly{w.ResponseWriter}, src)
	w.written += n
	return n, err
}

// writerOnly hides any ReadFrom/WriteTo methods on the wrapped writer so
// io.Copy does not recurse back into recordingWriter.ReadFrom.
type writerOnly struct {
	io.Writer
}

// zipDirectory writes a zip archive of src to dst. Entry names keep the
// directory's own name as their prefix, so unpacking yields a single
// top-level directory. Non-regular files (symlinks, devices, sockets) are
// skipped.
func zipDirectory(src, dst string) (err error) {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	zw := zip.NewWriter(f)
	defer func() {
		if cerr := zw.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	root := filepath.Clean(src)
	parent := filepath.Dir(root)

	return filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.Type().IsRegular() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(parent, p)
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		header.Method = zip.Deflate

		w, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		file, err := os.Open(p)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(w, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

// download fetches rawURL and saves it as dest, writing progress to progress.
func download(ctx context.Context, rawURL, dest string, progress io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "goof (https://github.com/tpaschalis/goof)")

	client := &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: headerTimeout,
			ForceAttemptHTTP2:     true,
		},
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: unexpected status %s", rawURL, resp.Status)
	}

	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	counter := &writeProgress{w: progress, total: resp.ContentLength}
	_, copyErr := io.Copy(io.MultiWriter(f, counter), resp.Body)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(tmp)
		return errors.Join(copyErr, closeErr)
	}

	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}

	fmt.Fprintf(progress, "\nSaved %s (%s) in %s\n", dest, byteCountBinary(counter.seen), time.Since(start).Round(time.Millisecond))
	return nil
}

// parseHTTPURL reports whether target is an absolute http(s) URL. A bare
// absolute path such as /tmp/file is not a URL, even though
// url.ParseRequestURI accepts it.
func parseHTTPURL(target string) (*url.URL, bool) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, false
	}
	if u.Host == "" {
		return nil, false
	}
	return u, true
}

// downloadName is the local file name a URL is saved as.
func downloadName(u *url.URL) string {
	// A trailing slash denotes a directory index rather than a file.
	if strings.HasSuffix(u.Path, "/") {
		return "index.html"
	}
	// The URL path is decoded, so it may contain backslashes (%5C). Treat them
	// as separators too, otherwise Windows would resolve them as directories.
	name := path.Base(strings.ReplaceAll(u.Path, `\`, "/"))
	if name == "." || name == "/" || name == "" {
		return "index.html"
	}
	// Refuse anything that is not a plain file name in the current directory:
	// "..", drive prefixes such as "C:x", reserved device names on Windows.
	if !filepath.IsLocal(name) || strings.ContainsAny(name, `/\`) {
		return "index.html"
	}
	return name
}

// writeProgress renders transfer progress on a single line.
type writeProgress struct {
	w     io.Writer
	total int64 // expected size, or -1 when unknown
	seen  int64
}

func (p *writeProgress) Write(b []byte) (int, error) {
	p.seen += int64(len(b))
	if p.total > 0 {
		fmt.Fprintf(p.w, "\r%-60s", fmt.Sprintf("Downloaded %s / %s (%d%%)",
			byteCountBinary(p.seen), byteCountBinary(p.total), p.seen*100/p.total))
	} else {
		fmt.Fprintf(p.w, "\r%-60s", "Downloaded "+byteCountBinary(p.seen))
	}
	return len(b), nil
}

// byteCountBinary formats a byte count in binary (IEC) units.
func byteCountBinary(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
