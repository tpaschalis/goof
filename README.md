# goof
An implementation of Simon's wonderful `woof` [Python script](http://www.home.unix-ag.org/simon/woof.html) in Golang.


```
$ ./goof --help

Serves a single file <count> times via http on port <port> on IP address <ip_addr>.
If a directory is specified, a .zip archive of that directory archive is served instead.

Usage of ./goof:
  -c int
        -c <count>.      How many times the file or directory will get served. (default 1)
  -i string
        -i <ip_addr>.    The address to serve the file or directory from. (default "127.0.0.1")
  -p string
        -p <port>.       The port to serve the file or directory from. (default "8080")
  -s    -s.              When specified, goof will distribute/serve itself
  ```
