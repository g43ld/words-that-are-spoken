// main.go coordinates application startup, server creation, and graceful shutdown.
package main

import (
	"errors"
	"log"
	"net"
	"net/http"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	resources, err := locateResources()
	if err != nil {
		log.Fatal(err)
	}

	templates, err := loadTemplates()
	if err != nil {
		log.Fatalf("load embedded templates: %v", err)
	}

	app := newApplication(resources, templates)

	if err := app.startWhisper(); err != nil {
		log.Fatal(err)
	}

	if err := app.waitForWhisper(); err != nil {
		app.stopWhisper()
		log.Fatal(err)
	}

	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		app.stopWhisper()
		log.Fatalf("start application server: %v", err)
	}

	app.server = &http.Server{
		Handler:           app.routes(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       5 * time.Second,
		WriteTimeout:      transcriptionTime + time.Minute,
	}

	url := "http://" + listener.Addr().String()
	app.applicationAddress = listener.Addr().String()
	log.Printf("%s ready: %s", appName, url)
	openBrowser(url)

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- app.server.Serve(listener)
	}()

	select {
	case err := <-serveDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP server stopped: %v", err)
		}
	case <-app.stop:
	}

	app.shutdown()
}
