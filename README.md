# goof

Go Offer File — serve a file or a directory over HTTP, then get out of the way.
It is a Go implementation of Simon Budig's [`woof`](http://www.home.unix-ag.org/simon/woof.html).

```console
$ goof holiday-photos
Archived holiday-photos to holiday-photos.zip
Now serving on http://127.0.0.1:8080/holiday-photos.zip
# ...the other side downloads it...
Exiting gracefully...
```

## Installation

Requires Go 1.22 or later.

```console
$ go install github.com/tpaschalis/goof@latest
```

From a checkout, `go build` produces a `goof` binary, and `go install` puts it in
`$(go env GOBIN)` (or `$(go env GOPATH)/bin`).

## Usage

```
$ goof -h

goof serves a single file, or a zip archive of a directory, over HTTP.

The file is served <count> times, then goof exits. If a URL is given instead,
goof downloads it into the current directory.

Usage:
  goof [flags] <file|directory|url>

Flags:
  -c int
    	-c <count>.      How many times the file or directory will get served. (default 1)
  -i string
    	-i <ip_addr>.    The address to serve the file or directory from. (default "127.0.0.1")
  -p string
    	-p <port>.       The port to serve the file or directory from. Use 0 for any free port. (default "8080")
  -s	-s.              When specified, goof will distribute/serve itself.
```

## Examples

Serve a file once, then exit:

```console
$ goof report.pdf
Now serving on http://127.0.0.1:8080/report.pdf
```

Serve a directory to everyone on the LAN, up to three times, on any free port:

```console
$ goof -i 0.0.0.0 -p 0 -c 3 ./build
Archived ./build to build.zip
Now serving on http://0.0.0.0:41337/build.zip
```

Send the binary itself, which is handy on a host without a shared filesystem:

```console
$ goof -s
Now serving on http://127.0.0.1:8080/goof
```

Fetch a URL instead of serving:

```console
$ goof https://example.com/releases/tool-linux-amd64
Downloaded 12.4 MiB / 12.4 MiB (100%)
Saved tool-linux-amd64 (12.4 MiB) in 1.2s
```

## Behaviour

- **Counting.** `-c` counts *completed* downloads: a full body, or a ranged
  request that delivers the end of the file (so `curl -C -` finishes a serve).
  A 404, a `HEAD` request or an aborted transfer does not use up a serve. The
  listener stays bound between rounds, so back-to-back downloads do not race a
  restart.
- **Range requests** are supported, so an interrupted download can resume with
  `curl -C -` or any resuming client.
- **Directories** are zipped into a temporary directory, so nothing is written
  next to your files and an existing archive of the same name is never
  overwritten. Entry names keep the directory's own name (`photos/a.jpg`), file
  modes are preserved, and non-regular files such as symlinks and sockets are
  skipped.
- **Downloads** are written to `<name>.tmp` and renamed on success, so a failed
  or cancelled transfer never leaves a truncated file behind. Progress is
  printed on stderr and the saved path on completion.
- **Exit codes.** `0` on success or cancellation, `2` for a bad command line,
  `1` for any other failure.
- **Shutdown.** After the last serve, or on `Ctrl-C`/`SIGTERM`, goof waits up to
  10 seconds for an in-flight transfer before closing the connection.

## Development

```console
$ go vet ./...
$ go test -race ./...
```

The test suite covers the zip builder, the download client, and the HTTP
handler directly; it also builds the binary and drives the real command line
for the end-to-end cases. CI runs it on Linux, macOS and Windows.

## Feedback

Criticism and improvement points are welcome — open an issue or a pull request.
The tool started as a first public Go project, and remains deliberately small.

```
                    __ 
                   / _|
  ____  ___   ___ | |_ 
 / _  |/ _ \ / _ \|  _|
| (_| | (_) | (_) | |  
 \__, |\___/ \___/|_|  
  __/ |               
 |___/                
```
