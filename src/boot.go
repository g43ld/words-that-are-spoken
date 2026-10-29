// boot.go loads runtime resources and manages the Whisper and browser processes.
package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

func loadTemplates() (*template.Template, error) {
	return template.ParseFS(embeddedAssets, "views/*.html")
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

func (app *application) shutdown() {
	if app.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = app.server.Shutdown(ctx)
		cancel()
	}

	app.stopWhisper()
}

func (app *application) requestStop() {
	app.stopOnce.Do(func() { close(app.stop) })
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
