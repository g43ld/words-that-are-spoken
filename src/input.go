// input.go validates and prepares uploaded files, Base64 audio, and language choices.
package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

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
		return &clientError{status: http.StatusBadRequest, message: "The uploaded audio encoding is invalid."}
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
