# Words That Are Spoken

Words That Are Spoken is a local audio-to-text application. It accepts an audio file or Base64-encoded audio, converts the audio to the format required by Whisper.cpp, and displays the transcription in a local browser window.

Audio is processed locally on the user's machine. The application does not send audio to a remote service or require an internet connection while transcribing.

## Features

- Audio file input and Base64 audio input.
- Supported audio formats:
  - MP3
  - M4A
  - WAV
  - WebM
  - OGG
  - FLAC
- FFmpeg conversion to 16 kHz, mono, PCM WAV.
- Local Whisper.cpp transcription.
- French, English, Russian, and Spanish language selection.
- Automatic language detection with the `I don't know` option.
- Transcription traceability information showing the local date/time, application port, Whisper.cpp, and FFmpeg.
- HTML and CSS embedded into the Go executable.
- FFmpeg, Whisper.cpp, and model files kept as external packaged resources.

## Requirements

- Go 1.22 or newer for development and building.
- FFmpeg executable and its required files under `bin/ffmpeg/`.
- Whisper.cpp server executable and its required files under `bin/whisper/`.
- A multilingual Whisper.cpp model at:

  ```text
  models/ggml-base.bin
  ```

The application currently expects the multilingual base model. A larger multilingual model can be used by changing the model filename in `main.go` and placing the matching file in `models/`.

## Running from source

The application resolves `bin/` and `models/` relative to the executable location. Build the executable before launching it.

### Windows

From PowerShell at the project root:

```powershell
go build -o wtas.exe .
.\wtas.exe
```

Or create a complete Windows package containing the executable, native resources, and model:

```powershell
.\scripts\build-windows.ps1
```

The package is created at:

```text
dist/windows/
├── wtas.exe
├── bin/
└── models/
```

### Linux

From a shell at the project root:

```bash
go build -o wtas .
./wtas
```

Or create a complete Linux package:

```bash
chmod +x scripts/build-linux.sh
./scripts/build-linux.sh
```

The package is created at:

```text
dist/linux/
├── wtas
├── bin/
└── models/
```

The application opens the local interface automatically. If it cannot open a browser, copy the displayed `http://127.0.0.1:<port>` address into a browser manually.

## Whisper.cpp model setup

The application starts the bundled Whisper.cpp server automatically and passes it the model path. It uses a dynamically selected localhost port for the internal Whisper server.

To download the multilingual base model on Windows:

```powershell
Invoke-WebRequest `
  -Uri "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.bin?download=true" `
  -OutFile "models\ggml-base.bin"
```

On Linux:

```bash
curl -L \
  "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.bin?download=true" \
  -o models/ggml-base.bin
```

Models with the `.en` suffix are English-only. Use a model without `.en` for French, English, Russian, Spanish, and automatic language detection.

## Local-only behavior

The application binds its web server to `127.0.0.1`, so it is intended for use on the local machine. It is not designed to be deployed as a public web service.

The application starts and stops its local Whisper.cpp process together with the main application. Temporary audio files are removed after each transcription.

## Project layout

```text
words-that-are-spoken/
├── main.go                 # Go application
├── main_test.go            # Standard-library tests
├── views/                  # Embedded HTML templates
├── public/                 # Embedded CSS
├── bin/                    # External FFmpeg and Whisper.cpp resources
├── models/                 # External Whisper.cpp models
├── examples/               # Example audio inputs
└── scripts/                # Windows and Linux packaging scripts
```

The `views/` and `public/` directories are embedded into the executable with Go's `embed` package. The `bin/` and `models/` directories remain external so native binaries, DLLs, and large model files can be packaged separately.

## Acknowledgements

This project is made possible by the work of the open-source projects below. Thank you to their maintainers and contributors:

- [Whisper.cpp](https://github.com/ggml-org/whisper.cpp) — local speech recognition and transcription.
- [FFmpeg](https://github.com/FFmpeg/FFmpeg) — audio decoding and conversion.

## License

Words That Are Spoken is distributed together with external open-source software and model files that have their own licenses. Review the respective licenses for Whisper.cpp, FFmpeg, and the selected Whisper model before redistributing a package.
