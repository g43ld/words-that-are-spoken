// transcription.go converts audio with FFmpeg and sends normalized WAV data to Whisper.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
)

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
