# goof
An implementation of Simon's wonderful `woof` [Python script](http://www.home.unix-ag.org/simon/woof.html) in Golang.

It's 2018 and serving a file over a network is [not as easy as it should be.](https://tpaschalis.github.io/excellent-software-woof/). The original script, as well as this Go command-line application try to provide a solution to this problem.


## Usage
The binary can either serve a file/directory or itself to the specified IP and port. If a URL is provided, it will download from that URL.   
The help text is pretty straightforward and conveys the main idea.  
```
$ ./goof --help

Serves a single file <count> times via http on port <port>, on IP address <ip_addr>.
If a directory is specified, a .zip archive of that directory archive is served instead.

If started with an url as an argument, goof will act as a client, and will download and save the file in the current directory.

Usage of ./goof:
  -c int
        -c <count>.      How many times the file or directory will get served. (default 1)
  -i string
        -i <ip_addr>.    The address to serve the file or directory from. (default "127.0.0.1")
  -p string
        -p <port>.       The port to serve the file or directory from. (default "8080")
  -s    -s.              When specified, goof will distribute/serve itself

Can only serve single files/directories
```

```
$ ./goof myfile
Now serving on http://127.0.0.1:8080/myfile
# after file has been received on the other end
Exiting gracefully...

$ ./goof http://ipv4.download.thinkbroadband.com:8080/10MB.zip
Download Progress : 10.0 MiB complete

File downloaded in: 4.531359s

$ ./goof -s 
Now serving on http://127.0.0.1:8080/goof
```


## Installation 
The code was developed using go `v1.9.7`, but should run on any non-totally-antiquated go version.  
You can clone the repository or download the `goof.go` source file and then `go build` it to get the executable binary, or `go install goof.go` to put in on your `$GOPATH/bin`.   
To access the binary from anywhere in your system, you can add its location to `$PATH`.  

## ToDo List
- Add test coverage
- Make code more Golang-idiomatic
- Document and port on other platforms.
- Test throughput limits
- Get feedback for future improvements

## Feedback
It's my first Go code that I'm getting out in public, so criticism and improvement points are not only welcome, but encouraged. Feel free to open an issue, or send me an email!

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


