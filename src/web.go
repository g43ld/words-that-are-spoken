// web.go serves the local HTTP UI, renders responses, and handles transcription and quit routes.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

func (app *application) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", app.handleIndex)
	mux.HandleFunc("GET /styles.css", app.handleStyles)
	mux.HandleFunc("POST /transcribe", app.handleTranscribe)
	mux.HandleFunc("POST /quit", app.handleQuit)
	return mux
}

func (app *application) handleIndex(w http.ResponseWriter, _ *http.Request) {
	app.render(w, http.StatusOK, "index.html", pageData{AppName: appName, Language: "en"})
}

func (app *application) handleStyles(w http.ResponseWriter, _ *http.Request) {
	styles, err := embeddedAssets.ReadFile("public/styles.css")
	if err != nil {
		log.Printf("read embedded stylesheet: %v", err)
		http.Error(w, "Internal server error.", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(styles)
}

func (app *application) handleTranscribe(w http.ResponseWriter, r *http.Request) {
	select {
	case app.transcribing <- struct{}{}:
		defer func() { <-app.transcribing }()
	default:
		app.renderError(w, &clientError{status: http.StatusServiceUnavailable, message: "Another transcription is already in progress. Please try again shortly."})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	ctx, cancel := context.WithTimeout(r.Context(), transcriptionTime)
	defer cancel()

	transcription, err := app.transcribe(ctx, r)
	if err != nil {
		var inputErr *clientError
		if errors.As(err, &inputErr) {
			app.renderError(w, inputErr)
			return
		}
		if errors.Is(err, errRequestTooLarge) {
			app.renderError(w, &clientError{status: http.StatusRequestEntityTooLarge, message: "The audio request is too large."})
			return
		}

		log.Printf("transcription failed: %v", err)
		app.renderError(w, &clientError{status: http.StatusInternalServerError, message: "Transcription failed. Check the application log for details."})
		return
	}

	app.render(w, http.StatusOK, "index.html", pageData{
		AppName:       appName,
		Language:      languageFromRequest(r),
		Transcription: transcription,
		Traceability:  app.traceability(),
	})
}

func (app *application) handleQuit(w http.ResponseWriter, _ *http.Request) {
	app.render(w, http.StatusOK, "index.html", pageData{
		AppName:       appName,
		Language:      "en",
		Closed:        true,
		ClosedMessage: "Application closed. You can close this tab.",
	})

	// Keep the server alive briefly so the browser can fetch the external
	// stylesheet referenced by the closed page before Whisper is stopped.
	time.AfterFunc(quitShutdownDelay, app.requestStop)
}

func (app *application) traceability() string {
	_, port, err := net.SplitHostPort(app.applicationAddress)
	if err != nil {
		port = "unknown"
	}

	return fmt.Sprintf("**** These words were transcribed with %s on %s via localhost port %s using whisper.cpp and FFmpeg. ****", appName, time.Now().Format("2006-01-02 15:04:05 MST"), port)
}

func (app *application) renderError(w http.ResponseWriter, err *clientError) {
	app.render(w, err.status, "index.html", pageData{AppName: appName, Language: "en", Error: err.message})
}

func (app *application) render(w http.ResponseWriter, status int, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := app.templates.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}
