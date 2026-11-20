// Command sql-table-generator serves a web editor where a table is designed
// like a spreadsheet and turned into CREATE TABLE and INSERT statements for
// MySQL, PostgreSQL, SQLite or SQL Server — and SQL is turned back into a table.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "address for the HTTP server to listen on")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		v, c, d := buildInfo()
		fmt.Printf("sql-table-generator %s\n  commit : %s\n  built  : %s\n", v, c, d)
		return
	}

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("POST /api/generate", handleGenerate)
	mux.HandleFunc("POST /api/parse", handleParse)
	mux.HandleFunc("POST /api/upload", handleUpload)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	v, _, _ := buildInfo()
	log.Printf("SQL Table Generator %s: http://%s", v, *addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
