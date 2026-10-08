package main

import (
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testApplication(t *testing.T) *application {
	t.Helper()
	templates, err := template.ParseFS(embeddedAssets, "views/*.html")
	if err != nil {
		t.Fatal(err)
	}
	return &application{
		templates:    templates,
		transcribing: make(chan struct{}, 1),
		stop:         make(chan struct{}),
	}
}

func TestAudioExtension(t *testing.T) {
	for _, name := range []string{"voice.MP3", "voice.m4a", "voice.wav", "voice.webm", "voice.ogg", "voice.flac"} {
		if _, err := audioExtension(name); err != nil {
			t.Errorf("audioExtension(%q): %v", name, err)
		}
	}

	if _, err := audioExtension("voice.exe"); err == nil {
		t.Fatal("expected unsupported extension to fail")
	}
}

func TestRequestedLanguage(t *testing.T) {
	tests := map[string]string{
		"":     "en",
		"en":   "en",
		"fr":   "fr",
		"es":   "es",
		"ru":   "ru",
		"auto": "auto",
	}

	for input, expected := range tests {
		actual, err := requestedLanguage(input)
		if err != nil {
			t.Fatalf("requestedLanguage(%q): %v", input, err)
		}
		if actual != expected {
			t.Errorf("requestedLanguage(%q) = %q, want %q", input, actual, expected)
		}
	}

	if _, err := requestedLanguage("de"); err == nil {
		t.Fatal("expected unsupported language to fail")
	}
}

func TestStripDataURL(t *testing.T) {
	value, err := stripDataURL("data:audio/webm;base64,QUJD")
	if err != nil || value != "QUJD" {
		t.Fatalf("stripDataURL returned %q, %v", value, err)
	}

	value, err = stripDataURL("QUJD")
	if err != nil || value != "QUJD" {
		t.Fatalf("raw Base64 changed: %q, %v", value, err)
	}

	if _, err := stripDataURL("data:audio/webm,QUJD"); err == nil {
		t.Fatal("expected non-Base64 data URL to fail")
	}
}

func TestWriteBase64Input(t *testing.T) {
	directory := t.TempDir()
	encoded := base64.StdEncoding.EncodeToString([]byte("audio"))
	path, err := writeBase64Input(directory, encoded)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "audio" {
		t.Fatalf("decoded input = %q", data)
	}

	if _, err := writeBase64Input(directory, "not-valid-base64!"); err == nil {
		t.Fatal("expected invalid Base64 to fail")
	}
}

func TestNormalizeEncodedAudio(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "example.m4a")

	originalAudio := []byte("fake m4a audio")
	base64Audio := base64.StdEncoding.EncodeToString(originalAudio)
	var binaryOctets strings.Builder
	for index, value := range []byte(base64Audio) {
		if index > 0 {
			binaryOctets.WriteByte(' ')
		}
		binaryOctets.WriteString(fmt.Sprintf("%08b", value))
	}
	if err := os.WriteFile(path, []byte(binaryOctets.String()), 0600); err != nil {
		t.Fatal(err)
	}

	if err := normalizeEncodedAudio(path); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(originalAudio) {
		t.Fatalf("normalized audio = %q", data)
	}
}

func TestNormalizeEncodedAudioLeavesBinaryUntouched(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "example.m4a")
	originalAudio := []byte{0, 1, 2, 3, 4}
	if err := os.WriteFile(path, originalAudio, 0600); err != nil {
		t.Fatal(err)
	}

	if err := normalizeEncodedAudio(path); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(originalAudio) {
		t.Fatalf("binary audio changed: %v", data)
	}
}

func TestMultipartInput(t *testing.T) {
	directory := t.TempDir()
	var body strings.Builder
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("audioFile", "voice.ogg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "audio"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/transcribe", strings.NewReader(body.String()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	app := &application{}
	path, err := app.writeMultipartInput(request, directory)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(path) != ".ogg" {
		t.Fatalf("input extension = %q", filepath.Ext(path))
	}
}

func TestRoutesAndTemplates(t *testing.T) {
	app := testApplication(t)
	server := httptest.NewServer(app.routes())
	defer server.Close()

	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d", response.StatusCode)
	}

	styleResponse, err := http.Get(server.URL + "/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	defer styleResponse.Body.Close()
	if styleResponse.StatusCode != http.StatusOK {
		t.Fatalf("GET /styles.css status = %d", styleResponse.StatusCode)
	}

	form := url.Values{}
	form.Set("audio", "")
	requestResponse, err := http.PostForm(server.URL+"/transcribe", form)
	if err != nil {
		t.Fatal(err)
	}
	defer requestResponse.Body.Close()
	if requestResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty transcription status = %d", requestResponse.StatusCode)
	}

	quitResponse, err := http.Post(server.URL+"/quit", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer quitResponse.Body.Close()
	if quitResponse.StatusCode != http.StatusOK {
		t.Fatalf("quit status = %d", quitResponse.StatusCode)
	}
}

func TestTraceability(t *testing.T) {
	app := &application{applicationAddress: "127.0.0.1:45678"}
	value := app.traceability()
	for _, expected := range []string{
		"****",
		"localhost port 45678",
		"whisper.cpp",
		"FFmpeg",
	} {
		if !strings.Contains(value, expected) {
			t.Fatalf("traceability %q does not contain %q", value, expected)
		}
	}
}
