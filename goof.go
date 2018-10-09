package main

import (
    "flag"
    "fmt"
    "strings"
    "net/http"
    "os"

    "archive/zip"
    "path/filepath"
    "io"
)

//TODO find out how to close server after X connections, or times file was accessed
//TODO find out how to make the binary serve itself
//TODO find out how to exit/return correctly
//TODO rewrite our own Recursive Zip

func main() {

    ipPtr := flag.String("i", "127.0.0.1", "-i <ip_addr>.   The address to serve the file or directory from.")
    portPtr := flag.String("p", "8080", "-p <port>.  The port to serve the file or directory from.")
    countPtr := flag.Int("c", 1, "-c <count>.  How many times the file or directory will get served.")
    itselfPtr := flag.Bool("s", false, "-s.  When specified, goof will distribute/serve itself")

    flag.Parse()
    args := flag.Args()

    if len(args) > 1 {
        fmt.Println("Can only serve single files/directories")
        // figure out the appropriate way to return/exit
    }
    name := strings.Join(args, "")

    fd, err := os.Stat(name)
    if err != nil {
        fmt.Println(err)
        return
    }
    var filename string
    switch mode := fd.Mode(); {
        case mode.IsDir():
            fmt.Println("It's a directory, zip it up!")
            filename = name+".zip"
            err = RecursiveZip(name, filename)
            if err != nil {
                fmt.Println(err)
                return
            }
        case mode.IsRegular():
            fmt.Println("It's a file, send it through!")
            filename = name
    }

    fmt.Println(args)
    fmt.Println(filename)

    ip := *ipPtr
    port := *portPtr
    count := *countPtr
    distribute_itself := *itselfPtr

    fmt.Println(ip, port, count, distribute_itself)
    fmt.Println("Exiting gracefully...")


    http.HandleFunc("/"+filename, func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Disposition", "attachment; filename="+filename)
        w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
        w.Header().Set("Content-Length", r.Header.Get("Content-Length"))

        http.ServeFile(w, r, filename)
    })

    fmt.Printf("Now serving on http://xxx:%s/%s", port, filename)
    http.ListenAndServe("localhost:"+port, nil)

}



// Rewrite function as my own
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
