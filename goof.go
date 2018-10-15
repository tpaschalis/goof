package main

import (
	"archive/zip"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {

	// Setup command-line flags and arguments
	setupFlags(flag.CommandLine)
	ipPtr := flag.String("i", "127.0.0.1", "-i <ip_addr>.    The address to serve the file or directory from.")
	portPtr := flag.String("p", "8080", "-p <port>.       The port to serve the file or directory from.")
	countPtr := flag.Int("c", 1, "-c <count>.      How many times the file or directory will get served.")
	itselfPtr := flag.Bool("s", false, "-s.              When specified, goof will distribute/serve itself")

	flag.Parse()
	args := flag.Args()

	ip := *ipPtr
	port := *portPtr
	count := *countPtr
	distributeItself := *itselfPtr

	// Check conditions for premature exit
	err := checkEarlyExit(count, distributeItself, ip, port, args)
	if err != nil {
		panic(err)
	}

	// Handles user specifying the binary to serve itself
	name := strings.Join(args, "")
	if distributeItself == true {
		name = os.Args[0]
	}

	// Handle user specifying a URL for download
	if isURL(name) {
		u, err := url.Parse(name)
		if err != nil {
			panic(err)
		}
		filepath := u.Path[1:]
		downloadFile(filepath, name) // 'Streams' the file to avoid loading it into memory
		os.Exit(0)
	}

	// Handle user specifying a file/directory to serve
	fd, err := os.Stat(name)
	if err != nil {
		fmt.Println(err)
		return
	}
	var filename string
	switch mode := fd.Mode(); {
	case mode.IsDir():
		name = filepath.Clean(name) // Avoid stuff such as trailing slashes
		filename = name + ".zip"
		err = RecursiveZip(name, filename)
		if err != nil {
			fmt.Println(err)
			return
		}
	case mode.IsRegular():
		filename = name
	}

	// Initialize triggers for our webserver
	ctx, cancel := context.WithCancel(context.Background())

	// Determine how the file will be served.
	// Headers are used so that browsers will try to download the file instead of opening it.
	http.HandleFunc("/"+filename, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", "attachment; filename="+filename)
		w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
		w.Header().Set("Content-Length", r.Header.Get("Content-Length"))

		Openfile, err := os.Open(filename)
		if err != nil {
			fmt.Println(err)
			return
		}

		io.Copy(w, Openfile) // 'Stream' the file to the client to avoid loading in memory
		io.WriteString(w, "Bye\n")
		cancel()
	})

	fmt.Printf("Now serving on http://%s:%s/%s\n", ip, port, name)

	// Serve as many times as the user has specified
	for count > 0 {
		ctx, cancel = context.WithCancel(context.Background())
		srv := &http.Server{Addr: ip + ":" + port}

		go func() {
			// Handles networking errors, such as being unable to bind IP or port
			if err := srv.ListenAndServe(); err != nil {
				log.Printf("Httpserver: ListenAndServe() error: %s", err)
			}
		}()
		<-ctx.Done()
		if err := srv.Shutdown(ctx); err != nil && err != context.Canceled {
			log.Println(err)
		}
		fmt.Println("COUNT ::: ", count)
		count = count - 1
	}
	fmt.Println("Exiting gracefully...")
}

func checkEarlyExit(c int, s bool, i string, p string, args []string) (err error) {

	// Handle user specifying multiple arguments
	if len(args) > 1 {
		fmt.Println("Can only serve single files/directories.")
		os.Exit(1)
	}

	// Handle user not specifying a file/directory to serve
	if len(args) == 0 && s == false {
		fmt.Println("You need to specify a file/directory to serve or a URL to download from.\n")
		flag.Usage()
		os.Exit(0)
	}

	// Handle user specifying `goof` to serve both itself and a file/directory
	if len(args) > 0 && s == true {
		fmt.Println("It is possible to either serve `goof` itself or a file, but not both.")
		flag.Usage()
		os.Exit(0)
	}

	return nil
}

// Many thanks to Mark Mellar
// https://stackoverflow.com/questions/49057032/recursively-zipping-a-directory-in-golang
func RecursiveZip(pathToZip, destinationPath string) error {
	destinationFile, err := os.Create(destinationPath)
	if err != nil {
		return err
	}
	myZip := zip.NewWriter(destinationFile)
	err = filepath.Walk(pathToZip, func(filePath string, info os.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}
		if err != nil {
			return err
		}
		relPath := strings.TrimPrefix(filePath, filepath.Dir(pathToZip))
		zipFile, err := myZip.Create(relPath)
		if err != nil {
			return err
		}
		fsFile, err := os.Open(filePath)
		if err != nil {
			return err
		}
		_, err = io.Copy(zipFile, fsFile)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	err = myZip.Close()
	if err != nil {
		return err
	}
	return nil
}

func downloadFile(filepath, url string) (err error) {
	startTime := time.Now()
	tempFilepath := filepath + ".tmp"
	file, err := os.Create(tempFilepath)
	if err != nil {
		return err
	}
	defer file.Close()

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Response status not OK: %s", resp.Status)
	}

	counter := &WriteProgress{}
	_, err = io.Copy(file, io.TeeReader(resp.Body, counter))
	if err != nil {
		return err
	}

	fmt.Println("\n")

	err = os.Rename(tempFilepath, filepath)
	if err != nil {
		return err
	}

	elapsedTime := time.Since(startTime)
	fmt.Printf("File downloaded in: %s\n", elapsedTime)

	return nil
}

func isURL(testString string) bool {
	_, err := url.ParseRequestURI(testString)
	if err != nil {
		return false
	}
	return true
}

// https://programming.guide/go/formatting-byte-size-to-human-readable-format.html
func ByteCountBinary(b int64) string {
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

func setupFlags(f *flag.FlagSet) {
	f.Usage = func() {
		fmt.Println("\nServes a single file <count> times via http on port <port> on IP address <ip_addr>.")
		fmt.Println("If a directory is specified, a .zip archive of that directory archive is served instead.\n")
		fmt.Println("If started with an url as an argument, woof acts as a client, downloading the file and saving it in the current directory.\n")

		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		f.PrintDefaults()

		fmt.Println("\nCan only serve single files/directories")
	}
}

type WriteProgress struct {
	Total int64
}

func (wc *WriteProgress) Write(p []byte) (int, error) {
	n := len(p)
	wc.Total += int64(n)
	wc.ShowProgress()
	return n, nil
}

func (wc WriteProgress) ShowProgress() {
	fmt.Printf("\r%s", strings.Repeat(" ", 40))
	fmt.Printf("\rDownload Progress : %s complete", ByteCountBinary(wc.Total))
}
