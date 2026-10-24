package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
	resources resourcePaths
	templates *template.Template

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

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	resources, err := locateResources()
	if err != nil {
		log.Fatal(err)
	}

	templates, err := template.ParseFS(embeddedAssets, "views/*.html")
	if err != nil {
		log.Fatalf("load embedded templates: %v", err)
	}

	app := &application{
		resources:    resources,
		templates:    templates,
		transcribing: make(chan struct{}, 1),
		stop:         make(chan struct{}),
	}

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

func locateResources() (resourcePaths, error) {
	base, err := executableDirectory()
	if err != nil {
		return resourcePaths{}, err
	}

	ffmpegName := "ffmpeg"
	whisperName := "whisper-server"
	if runtime.GOOS == "windows" {
		ffmpegName += ".exe"
		whisperName += ".exe"
	}

	resources := resourcePaths{
		ffmpeg:  filepath.Join(base, "bin", "ffmpeg", ffmpegName),
		whisper: filepath.Join(base, "bin", "whisper", whisperName),
		model:   filepath.Join(base, "models", "ggml-small.bin"),
	}

	for name, path := range map[string]string{
		"FFmpeg":         resources.ffmpeg,
		"Whisper server": resources.whisper,
		"Whisper model":  resources.model,
	} {
		if _, err := os.Stat(path); err != nil {
			return resourcePaths{}, fmt.Errorf("%s not found: %s", name, path)
		}
	}

	return resources, nil
}

func executableDirectory() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}

	return filepath.Dir(executable), nil
}

func (app *application) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", app.handleIndex)
	mux.HandleFunc("GET /styles.css", app.handleStyles)
	mux.HandleFunc("POST /transcribe", app.handleTranscribe)
	mux.HandleFunc("POST /quit", app.handleQuit)
	return mux
}

func (app *application) handleIndex(w http.ResponseWriter, _ *http.Request) {
	app.render(w, http.StatusOK, "index.html", pageData{
		AppName:  appName,
		Language: "en",
	})
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
		app.renderError(w, &clientError{
			status:  http.StatusServiceUnavailable,
			message: "Another transcription is already in progress. Please try again shortly.",
		})
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
			app.renderError(w, &clientError{
				status:  http.StatusRequestEntityTooLarge,
				message: "The audio request is too large.",
			})
			return
		}

		log.Printf("transcription failed: %v", err)
		app.renderError(w, &clientError{
			status:  http.StatusInternalServerError,
			message: "Transcription failed. Check the application log for details.",
		})
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

	return fmt.Sprintf(
		"**** These words were transcribed with %s on %s via localhost port %s using whisper.cpp and FFmpeg. ****",
		appName,
		time.Now().Format("2006-01-02 15:04:05 MST"),
		port,
	)
}

func (app *application) renderError(w http.ResponseWriter, err *clientError) {
	app.render(w, err.status, "index.html", pageData{
		AppName:  appName,
		Language: "en",
		Error:    err.message,
	})
}

func (app *application) render(w http.ResponseWriter, status int, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := app.templates.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

var errRequestTooLarge = errors.New("request body too large")

func (app *application) transcribe(ctx context.Context, r *http.Request) (string, error) {
	tempDir, err := os.MkdirTemp("", "speech-tool-")
	if err != nil {
		return "", fmt.Errorf("create temporary directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	inputPath, err := app.writeInput(r, tempDir)
	if err != nil {
		return "", err
	}

	language, err := requestedLanguage(r.FormValue("language"))
	if err != nil {
		return "", err
	}

	wavPath := filepath.Join(tempDir, "audio.wav")
	if err := app.convertToWav(ctx, inputPath, wavPath); err != nil {
		return "", err
	}

	return app.sendToWhisper(ctx, wavPath, language)
}

func languageFromRequest(r *http.Request) string {
	language, err := requestedLanguage(r.FormValue("language"))
	if err != nil {
		return "en"
	}
	
	return language
}

func requestedLanguage(value string) (string, error) {
	language := strings.ToLower(strings.TrimSpace(value))
	if language == "" {
		return "en", nil
	}

	if !supportedLanguages[language] {
		return "", &clientError{
			status:  http.StatusBadRequest,
			message: "Unsupported language. Choose English, French, Spanish, Russian, or I don't know.",
		}
	}

	return language, nil
}

func (app *application) writeInput(r *http.Request, tempDir string) (string, error) {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return app.writeMultipartInput(r, tempDir)
	}

	if err := r.ParseForm(); err != nil {
		if isRequestTooLarge(err) {
			return "", errRequestTooLarge
		}
		return "", &clientError{status: http.StatusBadRequest, message: "Invalid form data."}
	}

	return writeBase64Input(tempDir, r.FormValue("audio"))
}

func (app *application) writeMultipartInput(r *http.Request, tempDir string) (string, error) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		if isRequestTooLarge(err) {
			return "", errRequestTooLarge
		}

		return "", &clientError{status: http.StatusBadRequest, message: "Invalid multipart audio upload."}
	}

	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, header, err := r.FormFile("audioFile")
	if err == nil {
		defer file.Close()

		extension, extensionErr := audioExtension(header.Filename)
		if extensionErr != nil {
			return "", extensionErr
		}

		inputPath := filepath.Join(tempDir, "input"+extension)
		output, createErr := os.Create(inputPath)
		if createErr != nil {
			return "", fmt.Errorf("create temporary input: %w", createErr)
		}

		_, copyErr := io.Copy(output, file)
		closeErr := output.Close()
		if copyErr != nil {
			return "", fmt.Errorf("save uploaded audio: %w", copyErr)
		}

		if closeErr != nil {
			return "", fmt.Errorf("close temporary input: %w", closeErr)
		}

		if err := normalizeEncodedAudio(inputPath); err != nil {
			return "", err
		}

		return inputPath, nil
	}

	return writeBase64Input(tempDir, r.FormValue("audio"))
}

func writeBase64Input(tempDir, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", &clientError{status: http.StatusBadRequest, message: "No audio was provided."}
	}

	value, err := stripDataURL(value)
	if err != nil {
		return "", err
	}

	inputPath := filepath.Join(tempDir, "input.audio")
	output, err := os.Create(inputPath)

	if err != nil {
		return "", fmt.Errorf("create temporary input: %w", err)
	}

	decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(value))
	_, copyErr := io.Copy(output, decoder)
	closeErr := output.Close()

	if copyErr != nil {
		return "", &clientError{status: http.StatusBadRequest, message: "The supplied Base64 audio is invalid."}
	}

	if closeErr != nil {
		return "", fmt.Errorf("close temporary input: %w", closeErr)
	}

	info, err := os.Stat(inputPath)
	if err != nil {
		return "", fmt.Errorf("inspect temporary input: %w", err)
	}

	if info.Size() == 0 {
		return "", &clientError{status: http.StatusBadRequest, message: "The supplied Base64 contains no audio."}
	}

	return inputPath, nil
}

func isRequestTooLarge(err error) bool {
	var maxBytesError *http.MaxBytesError
	return errors.As(err, &maxBytesError)
}

// normalizeEncodedAudio accepts the legacy example representation where the
// file bytes are written as ASCII binary octets and those decoded bytes are a
// Base64-encoded audio file. Normal binary audio is returned unchanged.
func normalizeEncodedAudio(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read uploaded audio: %w", err)
	}

	bitText, bitCount := isBinaryOctetText(data)
	if !bitText || bitCount == 0 || bitCount%8 != 0 {
		return nil
	}

	decodedText := make([]byte, bitCount/8)
	bitIndex := 0
	for _, value := range data {
		if value != '0' && value != '1' {
			continue
		}

		byteIndex := bitIndex / 8
		decodedText[byteIndex] <<= 1
		if value == '1' {
			decodedText[byteIndex] |= 1
		}
		bitIndex++
	}

	encodedAudio := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, string(decodedText))

	audio, err := base64.StdEncoding.Strict().DecodeString(encodedAudio)
	if err != nil || len(audio) == 0 {
		return &clientError{
			status:  http.StatusBadRequest,
			message: "The uploaded audio encoding is invalid.",
		}
	}

	if err := os.WriteFile(path, audio, 0600); err != nil {
		return fmt.Errorf("write normalized audio: %w", err)
	}

	return nil
}

func isBinaryOctetText(data []byte) (bool, int) {
	digitCount := 0
	for _, value := range data {
		switch value {
		case '0', '1':
			digitCount++
		case ' ', '\t', '\r', '\n':
		default:
			return false, 0
		}
	}

	return true, digitCount
}

func stripDataURL(value string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(value), "data:audio/") {
		return value, nil
	}

	comma := strings.IndexByte(value, ',')
	if comma < 0 || !strings.Contains(strings.ToLower(value[:comma]), ";base64") {
		return "", &clientError{status: http.StatusBadRequest, message: "The supplied audio data URL is invalid."}
	}

	return value[comma+1:], nil
}

func audioExtension(filename string) (string, error) {
	extension := strings.ToLower(filepath.Ext(filename))

	if !supportedAudioExtensions[extension] {
		return "", &clientError{
			status:  http.StatusBadRequest,
			message: "Unsupported audio format. Use MP3, M4A, WAV, WebM, OGG, or FLAC.",
		}
	}

	return extension, nil
}

func (app *application) convertToWav(ctx context.Context, inputPath, outputPath string) error {
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-y",
		"-i", inputPath,
		"-vn",
		"-ar", "16000",
		"-ac", "1",
		"-c:a", "pcm_s16le",
		outputPath,
	}

	cmd := exec.CommandContext(ctx, app.resources.ffmpeg, args...)
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, truncate(stderr.String(), 2000))
	}

	return nil
}

func (app *application) sendToWhisper(ctx context.Context, wavPath, language string) (string, error) {
	audio, err := os.Open(wavPath)
	if err != nil {
		return "", fmt.Errorf("open WAV: %w", err)
	}

	defer audio.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "audio.wav")

	if err != nil {
		return "", fmt.Errorf("create Whisper upload: %w", err)
	}

	if _, err := io.Copy(part, audio); err != nil {
		return "", fmt.Errorf("copy WAV to Whisper upload: %w", err)
	}

	if err := writer.WriteField("response_format", "json"); err != nil {
		return "", fmt.Errorf("create Whisper request: %w", err)
	}

	if err := writer.WriteField("language", language); err != nil {
		return "", fmt.Errorf("create Whisper language field: %w", err)
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close Whisper upload: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, app.whisperURL+"/inference", &body)
	if err != nil {
		return "", fmt.Errorf("create Whisper request: %w", err)
	}

	request.Header.Set("Content-Type", writer.FormDataContentType())

	response, err := (&http.Client{Timeout: transcriptionTime}).Do(request)
	if err != nil {
		return "", fmt.Errorf("call Whisper: %w", err)
	}

	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Whisper returned HTTP %s", response.Status)
	}

	var result struct {
		Text string `json:"text"`
	}

	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return "", fmt.Errorf("decode Whisper response: %w", err)
	}

	return result.Text, nil
}

func (app *application) startWhisper() error {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))

	if err != nil {
		return fmt.Errorf("allocate Whisper port: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port

	if err := listener.Close(); err != nil {
		return fmt.Errorf("release Whisper port: %w", err)
	}

	app.whisperURL = "http://" + net.JoinHostPort(host, strconv.Itoa(port))
	app.whisper = exec.Command(
		app.resources.whisper,
		"-m", app.resources.model,
		"--host", host,
		"--port", strconv.Itoa(port),
	)
	app.whisper.Stdout = os.Stdout
	app.whisper.Stderr = os.Stderr

	if err := app.whisper.Start(); err != nil {
		return fmt.Errorf("start Whisper: %w", err)
	}

	return nil
}

func (app *application) waitForWhisper() error {
	client := &http.Client{Timeout: time.Second}

	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		response, err := client.Get(app.whisperURL + "/")

		if err == nil {
			response.Body.Close()

			if response.StatusCode >= 200 && response.StatusCode < 300 {
				log.Printf("Whisper ready")
				return nil
			}
		}

		time.Sleep(250 * time.Millisecond)
	}

	return errors.New("Whisper did not start within 30 seconds")
}

func (app *application) stopWhisper() {
	if app.whisper == nil || app.whisper.Process == nil {
		return
	}

	if err := app.whisper.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		log.Printf("stop Whisper: %v", err)
	}

	_ = app.whisper.Wait()
}

func (app *application) requestStop() {
	app.stopOnce.Do(func() { close(app.stop) })
}

func (app *application) shutdown() {
	if app.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = app.server.Shutdown(ctx)
		cancel()
	}

	app.stopWhisper()
}

func openBrowser(url string) {
	var command *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		command = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		command = exec.Command("open", url)
	default:
		command = exec.Command("xdg-open", url)
	}

	if err := command.Start(); err != nil {
		log.Printf("open browser: %v", err)
		return
	}

	_ = command.Process.Release()
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}

	return value[:max] + "..."
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)

	if b.limit == 0 {
		b.limit = 2000
	}

	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	}

	return originalLength, nil
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}
