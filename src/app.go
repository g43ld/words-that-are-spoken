// app.go defines shared application state, configuration constants, and embedded assets.
package main

import (
	"embed"
	"errors"
	"html/template"
	"net/http"
	"os/exec"
	"sync"
	"time"
)

const (
	appName           = "WTAS (Words That Are Spoken)"
	host              = "127.0.0.1"
	maxRequestBytes   = 50 * 1024 * 1024
	transcriptionTime = 10 * time.Minute
	quitShutdownDelay = 2 * time.Second
)

var (
	//go:embed views/*.html public/styles.css
	embeddedAssets embed.FS

	supportedAudioExtensions = map[string]bool{
		".mp3":  true,
		".m4a":  true,
		".wav":  true,
		".webm": true,
		".ogg":  true,
		".flac": true,
	}

	supportedLanguages = map[string]bool{
		"en":   true,
		"fr":   true,
		"es":   true,
		"ru":   true,
		"auto": true,
	}
)

type pageData struct {
	AppName       string
	Language      string
	Transcription string
	Traceability  string
	Error         string
	Closed        bool
	ClosedMessage string
}

type resourcePaths struct {
	ffmpeg  string
	whisper string
	model   string
}

type application struct {
	resources          resourcePaths
	templates          *template.Template
	whisper            *exec.Cmd
	whisperURL         string
	applicationAddress string
	server             *http.Server
	transcribing       chan struct{}

	stopOnce sync.Once
	stop     chan struct{}
}

type clientError struct {
	status  int
	message string
}

func (e *clientError) Error() string {
	return e.message
}

func newApplication(resources resourcePaths, templates *template.Template) *application {
	return &application{
		resources:    resources,
		templates:    templates,
		transcribing: make(chan struct{}, 1),
		stop:         make(chan struct{}),
	}
}

var errRequestTooLarge = errors.New("request body too large")
