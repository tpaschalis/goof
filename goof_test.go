package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// binaryPath is the goof binary built once by TestMain for the end-to-end
// tests, which exercise the real command line rather than the functions.
var binaryPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "goof-test-")
	if err != nil {
		panic(err)
	}
	binaryPath = filepath.Join(dir, "goof")
	if runtime.GOOS == "windows" {
		binaryPath += ".exe"
	}

	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestParseHTTPURL(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"http://example.com/file.txt", true},
		{"https://example.com/a/b.txt", true},
		{"HTTPS://example.com/x", true},
		{"http://127.0.0.1:8080/x", true},
		{"/tmp/file.txt", false}, // an absolute path is not a URL
		{"file.txt", false},
		{"./file.txt", false},
		{"file:///tmp/file.txt", false},
		{"ftp://example.com/x", false},
		{"http://", false},
		{"", false},
	}
	for _, tt := range tests {
		if _, got := parseHTTPURL(tt.in); got != tt.want {
			t.Errorf("parseHTTPURL(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestDownloadName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"http://example.com/a/b.txt", "b.txt"},
		{"http://example.com/a%20b.txt", "a b.txt"},
		{"http://example.com/dir/", "index.html"},
		{"http://example.com", "index.html"},
		{"http://example.com/..", "index.html"},
		{"http://example.com/a/..%2F..", "index.html"},
		{"http://example.com/..%5Cevil.txt", "evil.txt"},
		{"http://example.com/%5C..%5C..", "index.html"},
	}
	for _, tt := range tests {
		u, ok := parseHTTPURL(tt.in)
		if !ok {
			t.Fatalf("parseHTTPURL(%q) failed", tt.in)
		}
		if got := downloadName(u); got != tt.want {
			t.Errorf("downloadName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestByteCountBinary(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"},
		{3 << 30, "3.0 GiB"},
	}
	for _, tt := range tests {
		if got := byteCountBinary(tt.in); got != tt.want {
			t.Errorf("byteCountBinary(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestZipDirectory(t *testing.T) {
	src := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("beta"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(src, "a.txt"), filepath.Join(src, "link.txt")); err != nil {
			t.Fatal(err)
		}
	}

	dst := filepath.Join(t.TempDir(), "tree.zip")
	if err := zipDirectory(src, dst); err != nil {
		t.Fatalf("zipDirectory: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	got := readZipEntries(t, data)
	want := map[string]string{
		"tree/a.txt":     "alpha",
		"tree/sub/b.txt": "beta",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("archive entries = %v, want %v", got, want)
	}

	// File modes should survive the round trip.
	if runtime.GOOS != "windows" {
		r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range r.File {
			if f.Name == "tree/sub/b.txt" && f.Mode().Perm() != 0o600 {
				t.Errorf("mode of %s = %v, want 0600", f.Name, f.Mode().Perm())
			}
		}
	}
}

func TestPrepareTargetCurrentDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })

	diskPath, urlPath, cleanup, err := prepareTarget(".", io.Discard)
	if err != nil {
		t.Fatalf("prepareTarget: %v", err)
	}
	defer cleanup()

	if urlPath != "/proj.zip" {
		t.Errorf("urlPath = %q, want /proj.zip", urlPath)
	}
	data, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"proj/a.txt": "alpha"}
	if got := readZipEntries(t, data); !reflect.DeepEqual(got, want) {
		t.Errorf("archive entries = %v, want %v", got, want)
	}
}

func TestDownload(t *testing.T) {
	payload := bytes.Repeat([]byte("goof"), 2048)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")
	var progress lockedBuffer
	if err := download(context.Background(), srv.URL+"/payload.bin", dest, &progress); err != nil {
		t.Fatalf("download: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("downloaded %d bytes, want %d", len(got), len(payload))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("left %d files behind, want 1 (no .tmp)", len(entries))
	}
	if out := progress.String(); !strings.Contains(out, "Saved "+dest) {
		t.Errorf("progress output %q missing a completion line", out)
	}
}

func TestDownloadFailureLeavesNoFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")
	var progress lockedBuffer
	if err := download(context.Background(), srv.URL+"/payload.bin", dest, &progress); err == nil {
		t.Fatal("download of a 404 succeeded")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("left %d files behind after a failed download", len(entries))
	}
}

func TestDownloadCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dest := filepath.Join(t.TempDir(), "payload.bin")
	if err := download(ctx, srv.URL, dest, io.Discard); err == nil {
		t.Fatal("download with a cancelled context succeeded")
	}
}

func TestServeCountsCompleteDownloads(t *testing.T) {
	file, payload := writeFile(t, "payload.bin", 4096)

	var out lockedBuffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- serve(context.Background(), "127.0.0.1:0", file, "/payload.bin", 2, &out)
	}()

	url := waitForServingURL(t, &out, 10*time.Second)
	for i := 0; i < 2; i++ {
		resp, body := get(t, url)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status %d", i+1, resp.StatusCode)
		}
		if !bytes.Equal(body, payload) {
			t.Fatalf("request %d: got %d bytes, want %d", i+1, len(body), len(payload))
		}
		if got := resp.Header.Get("Content-Disposition"); !strings.Contains(got, "payload.bin") {
			t.Errorf("Content-Disposition = %q, want an attachment for payload.bin", got)
		}
		if resp.ContentLength != int64(len(payload)) {
			t.Errorf("Content-Length = %d, want %d", resp.ContentLength, len(payload))
		}
	}

	if err := waitErr(errCh, 10*time.Second); err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestServeIgnoresIncompleteTransfers(t *testing.T) {
	file, payload := writeFile(t, "payload.bin", 4096)

	var out lockedBuffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- serve(context.Background(), "127.0.0.1:0", file, "/payload.bin", 1, &out)
	}()
	url := waitForServingURL(t, &out, 10*time.Second)

	// A HEAD request, a ranged request and a 404 must not use up the serve.
	head, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	headResp, err := http.DefaultClient.Do(head)
	if err != nil {
		t.Fatal(err)
	}
	headResp.Body.Close()

	rangeReq, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	rangeReq.Header.Set("Range", "bytes=0-3")
	rangeResp, err := http.DefaultClient.Do(rangeReq)
	if err != nil {
		t.Fatal(err)
	}
	rangeBody, _ := io.ReadAll(rangeResp.Body)
	rangeResp.Body.Close()
	if rangeResp.StatusCode != http.StatusPartialContent || len(rangeBody) != 4 {
		t.Fatalf("ranged request: status %d, %d bytes", rangeResp.StatusCode, len(rangeBody))
	}

	notFound, err := http.Get(url + "-missing")
	if err != nil {
		t.Fatal(err)
	}
	notFound.Body.Close()
	if notFound.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path: status %d, want 404", notFound.StatusCode)
	}

	select {
	case err := <-errCh:
		t.Fatalf("serve returned early: %v", err)
	case <-time.After(250 * time.Millisecond):
	}

	// The full download is the one that counts.
	_, body := get(t, url)
	if !bytes.Equal(body, payload) {
		t.Fatalf("full download got %d bytes, want %d", len(body), len(payload))
	}
	if err := waitErr(errCh, 10*time.Second); err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestServeCountsResumedDownload(t *testing.T) {
	file, payload := writeFile(t, "payload.bin", 4096)

	var out lockedBuffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- serve(context.Background(), "127.0.0.1:0", file, "/payload.bin", 1, &out)
	}()
	url := waitForServingURL(t, &out, 10*time.Second)

	// Resuming from the last bytes delivers the end of the file, which is a
	// completed download even though it did not start at offset zero.
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=4092-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("resume status = %d, want 206", resp.StatusCode)
	}
	if !bytes.Equal(body, payload[len(payload)-4:]) {
		t.Fatalf("resumed body = %q, want the last 4 bytes", body)
	}

	if err := waitErr(errCh, 10*time.Second); err != nil {
		t.Fatalf("serve did not finish after a resumed download: %v", err)
	}
}

func TestContentRangeEnd(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"bytes 0-9/100", 9},
		{"bytes 4092-4095/4096", 4095},
		{"", -1},
		{"bogus", -1},
		{"bytes */100", -1},
	}
	for _, tt := range tests {
		if got := contentRangeEnd(tt.in); got != tt.want {
			t.Errorf("contentRangeEnd(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestServeStopsOnContextCancel(t *testing.T) {
	file, _ := writeFile(t, "payload.bin", 128)

	var out lockedBuffer
	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		errCh <- serve(ctx, "127.0.0.1:0", file, "/payload.bin", 5, &out)
	}()
	waitForServingURL(t, &out, 10*time.Second)
	cancel()

	if err := waitErr(errCh, 10*time.Second); err != nil {
		t.Fatalf("serve after cancel: %v", err)
	}
}

func TestRunRejectsBadArguments(t *testing.T) {
	cases := [][]string{
		{},
		{"-s", "some-file"},
		{"first", "second"},
		{"-c", "0", "some-file"},
	}
	for _, args := range cases {
		var stdout, stderr lockedBuffer
		if err := run(context.Background(), args, &stdout, &stderr); !errors.Is(err, errUsage) {
			t.Errorf("run(%q) = %v, want errUsage", args, err)
		}
	}
}

func TestRunHelpSucceeds(t *testing.T) {
	var stdout, stderr lockedBuffer
	if err := run(context.Background(), []string{"-h"}, &stdout, &stderr); err != nil {
		t.Fatalf("run(-h) = %v, want nil", err)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("help output %q has no usage section", stderr.String())
	}
}

func TestCLIServesFileOnce(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("hello from goof\n")
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binaryPath, "-p", "0", "hello.txt")
	cmd.Dir = dir
	var out lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	url := waitForServingURL(t, &out, 10*time.Second)
	if !strings.HasSuffix(url, "/hello.txt") {
		t.Errorf("serving URL %q does not end in /hello.txt", url)
	}

	_, body := get(t, url)
	if !bytes.Equal(body, payload) {
		t.Errorf("downloaded %q, want %q", body, payload)
	}

	if err := waitErr(cmdWait(cmd), 10*time.Second); err != nil {
		t.Fatalf("goof exited with %v; output:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Exiting gracefully") {
		t.Errorf("output %q has no exit message", out.String())
	}
}

func TestCLIDownloadsURL(t *testing.T) {
	payload := bytes.Repeat([]byte("payload-"), 512)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cmd := exec.Command(binaryPath, srv.URL+"/payload.bin")
	cmd.Dir = dir
	var out lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("goof %s: %v; output:\n%s", srv.URL, err, out.String())
	}

	got, err := os.ReadFile(filepath.Join(dir, "payload.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("downloaded %d bytes, want %d", len(got), len(payload))
	}
}

func TestCLIServesItself(t *testing.T) {
	cmd := exec.Command(binaryPath, "-s", "-p", "0")
	var out lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	url := waitForServingURL(t, &out, 10*time.Second)
	_, body := get(t, url)

	want, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, want) {
		t.Errorf("served itself as %d bytes, want %d", len(body), len(want))
	}

	if err := waitErr(cmdWait(cmd), 10*time.Second); err != nil {
		t.Fatalf("goof -s exited with %v; output:\n%s", err, out.String())
	}
}

func TestCLIServesDirectory(t *testing.T) {
	src := filepath.Join(t.TempDir(), "photos")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binaryPath, "-p", "0", src)
	var out lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	url := waitForServingURL(t, &out, 10*time.Second)
	if !strings.HasSuffix(url, "/photos.zip") {
		t.Errorf("serving URL %q does not end in /photos.zip", url)
	}

	_, body := get(t, url)
	entries := readZipEntries(t, body)
	want := map[string]string{"photos/sub/a.txt": "alpha"}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("served archive = %v, want %v", entries, want)
	}

	if err := waitErr(cmdWait(cmd), 10*time.Second); err != nil {
		t.Fatalf("goof exited with %v; output:\n%s", err, out.String())
	}
}

func TestCLIUsageError(t *testing.T) {
	cmd := exec.Command(binaryPath)
	var out lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("goof with no arguments = %v, want a non-zero exit", err)
	}
	if code := exitErr.ExitCode(); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(out.String(), "specify a file/directory") {
		t.Errorf("output %q does not explain the problem", out.String())
	}
}

// --- helpers ---

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var servingURL = regexp.MustCompile(`Now serving on (http://\S+)`)

// waitForServingURL polls the process output until goof announces its URL.
func waitForServingURL(t *testing.T, out *lockedBuffer, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m := servingURL.FindStringSubmatch(out.String()); m != nil {
			return m[1]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no serving URL within %s; output:\n%s", timeout, out.String())
	return ""
}

func waitErr(ch <-chan error, timeout time.Duration) error {
	select {
	case err := <-ch:
		return err
	case <-time.After(timeout):
		return errors.New("timed out")
	}
}

// cmdWait waits for the command in the background so the test can bound it.
func cmdWait(cmd *exec.Cmd) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- cmd.Wait() }()
	return ch
}

func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", url, err)
	}
	return resp, body
}

func writeFile(t *testing.T, name string, size int) (string, []byte) {
	t.Helper()
	payload := bytes.Repeat([]byte("goof"), size/4)
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, payload
}

func readZipEntries(t *testing.T, data []byte) map[string]string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("reading zip: %v", err)
	}
	entries := make(map[string]string, len(r.File))
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening %s: %v", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("reading %s: %v", f.Name, err)
		}
		entries[f.Name] = string(b)
	}
	return entries
}
