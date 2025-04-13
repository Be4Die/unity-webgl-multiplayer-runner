package main

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Printf("usage: %s [PORT]\n", os.Args[0])
		return
	}

	port, err := strconv.Atoi(os.Args[1])
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	http.HandleFunc("/", handler)
	fmt.Printf("Serving at http://localhost:%d\n", port)
	http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
}

func handler(w http.ResponseWriter, r *http.Request) {
	path := "." + r.URL.Path

	file, err := os.Open(path)
	if err != nil {
		serveFallback(w, r)
		return
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil || stat.IsDir() {
		serveFallback(w, r)
		return
	}

	switch {
	case strings.HasSuffix(path, ".js.gz"):
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Content-Encoding", "gzip")
	case strings.HasSuffix(path, ".wasm.gz"):
		w.Header().Set("Content-Type", "application/wasm")
		w.Header().Set("Content-Encoding", "gzip")
	case strings.HasSuffix(path, ".gz"):
		w.Header().Set("Content-Encoding", "gzip")
		baseExt := filepath.Ext(strings.TrimSuffix(path, ".gz"))
		if mimeType := mime.TypeByExtension(baseExt); mimeType != "" {
			w.Header().Set("Content-Type", mimeType)
		}
	}

	io.Copy(w, file)
}

func serveFallback(w http.ResponseWriter, r *http.Request) {
	http.FileServer(http.Dir(".")).ServeHTTP(w, r)
}