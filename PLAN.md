# fftui: Plan

A terminal UI for FFmpeg, written in Go, that runs on as many operating systems as possible.

## Goals

- Make common FFmpeg tasks (convert, compress, trim, extract audio, resize, make GIFs) possible without memorizing flags.
- Always show the exact `ffmpeg` command that will run, so the TUI also teaches FFmpeg and its output can be copied into scripts.
- Ship one static binary per platform with no runtime dependencies other than `ffmpeg` and `ffprobe`.
- Work the same on Linux, macOS, Windows, and the BSDs, in any reasonable terminal down to 80×24.

## Non-goals (for v1)

- Linking against libav* through cgo. It would break easy cross-compilation and most of the portability goal.
- Bundling FFmpeg binaries. Licensing (GPL/LGPL builds), binary size, and per-platform builds make this a distribution problem, not an app feature. fftui finds an installed FFmpeg and explains how to install one if it is missing.
- Video preview inside the terminal. It may come later through sixel or kitty graphics, but it is not portable enough for v1.
- A full non-linear editor or timeline.

## Key decisions

| Decision | Choice | Why |
| --- | --- | --- |
| Language | Go, `CGO_ENABLED=0` | Pure-Go builds cross-compile to every target from one machine. |
| TUI framework | [Bubble Tea](https://github.com/charmbracelet/bubbletea) with Bubbles and Lip Gloss | Mature, Elm-style architecture that is easy to test, works on Windows consoles, and degrades color automatically by terminal profile. Use v2 if it is stable when work starts. |
| FFmpeg integration | Run the `ffmpeg` and `ffprobe` executables with `os/exec` | No cgo, works with any FFmpeg build the user has, including hardware-accelerated ones. |
| Arguments | Always an argument slice, never a shell string | No quoting bugs with spaces or Unicode in paths, and no shell injection on any OS. |
| Media info | `ffprobe -v error -print_format json -show_format -show_streams` | Stable, machine-readable output. |
| Progress | `ffmpeg -progress pipe:1 -nostats` | Line-based `key=value` output (`out_time_us`, `speed`, `total_size`, `progress=end`) that is far easier and safer to parse than stderr. |
| Config and user presets | TOML in `os.UserConfigDir()/fftui/` (`config.toml` for settings, `presets.toml` for user presets) | TOML is simple to read and edit by hand, and the directory resolves to the right place on each OS (`~/.config`, `~/Library/Application Support`, `%AppData%`). |
| Releases | GoReleaser | One config builds archives, checksums, and package-manager manifests for every target. |

## Target platforms

Built and released for every target that both Go and the terminal stack support:

- **Linux**: amd64, arm64, armv6, armv7, 386, riscv64, ppc64le, s390x, loong64
- **macOS**: amd64, arm64 (universal binary as well)
- **Windows**: amd64, arm64, 386
- **FreeBSD, OpenBSD, NetBSD**: amd64, arm64 (plus 386 and arm where Go supports them)
- **Others to try**: DragonFly BSD, illumos/Solaris. Ship them if the build and a smoke test pass; drop them otherwise.

Out of reach: Plan 9, `js/wasm`, `wasip1`, iOS, and Android (except via Termux, where the linux/arm64 build should work and is worth a smoke test).

Minimum OS versions follow the Go release used for builds: with Go 1.27, Windows 10 and macOS 13 Ventura or later.

## Cross-platform concerns and how to handle them

- **Finding FFmpeg**: check, in order, the `--ffmpeg` flag, the `FFTUI_FFMPEG` environment variable, the config file, then `exec.LookPath("ffmpeg")`. Find `ffprobe` next to the chosen `ffmpeg` first, then on `PATH`. On Windows, `LookPath` handles `.exe`. Also check common locations that are often not on `PATH` (`/opt/homebrew/bin`, `/usr/local/bin`, Scoop and winget shims, `C:\ffmpeg\bin`).
- **Missing FFmpeg**: show a friendly screen with the install command for the detected OS (`brew install ffmpeg`, `winget install ffmpeg`, `scoop install ffmpeg`, `apt install ffmpeg`, `dnf install ffmpeg`, `pkg install ffmpeg`, `pacman -S ffmpeg`, and so on) and a way to set a custom path.
- **Version and capability detection**: parse `ffmpeg -version`, `-encoders`, `-decoders`, `-muxers`, and `-hwaccels` once at startup and cache them. Presets only offer encoders the local build actually has (for example, hide `libx265` on builds without it, and offer `h264_videotoolbox`, `h264_nvenc`, `h264_qsv`, `h264_vaapi`, or `h264_amf` only when present).
- **Cancelling a job**: write `q` to FFmpeg's stdin, which works on every OS and lets FFmpeg finish the file cleanly. If it has not exited after a timeout, kill the process. Do not rely on `SIGINT`, which cannot be sent to a child process on Windows.
- **Job success**: FFmpeg exits with status 0 after `q`, and FFmpeg 9 also exits with 0 when it refuses to overwrite a file. A job succeeded only if FFmpeg exited with 0 and wrote `progress=end`; fftui itself remembers which jobs it cancelled. Jobs run with `-n` unless they pass `-y`, so FFmpeg never waits for an answer to its overwrite question.
- **Two-pass encodes**: both passes need `-fps_mode passthrough` (`-vsync passthrough` before FFmpeg 5.1). Without it the passes can see different frame counts, and FFmpeg 5.1 fails the second pass with "Incomplete MB-tree stats file". The pass log goes in the OS temp folder and is deleted when the job ends, whether it succeeded or not.
- **Hardware encoders**: some need a device set up first (`-init_hw_device` and `format=nv12,hwupload` for VA-API, Vulkan, and D3D12). A failed hardware job keeps the same job with the software encoder ready, so retrying is one key.
- **Error messages**: FFmpeg ends a failure with generic lines such as "Conversion failed!" and, since FFmpeg 6, several "Task finished with error code" lines. fftui skips those and shows the line that names the cause, with the encoder or filter it came from, using failure logs captured from FFmpeg 4.4, 5.1, and 9.0 as test fixtures.
- **Copying commands**: commands are written with POSIX single quotes, or double quotes on Windows, so a copied command runs in the user's shell. Text goes to the terminal with OSC 52 and also to `pbcopy`, `clip.exe` (as UTF-16, or it garbles non-ASCII), `wl-copy`, `xclip`, or `xsel` when present.
- **Terminal differences**: let Lip Gloss pick the color profile (true color, 256, 16, or none) and respect `NO_COLOR`. Offer an `--ascii` option that swaps box-drawing characters and icons for plain ASCII for legacy consoles and limited fonts. Mouse support is optional and everything is reachable by keyboard.
- **Paths**: use `path/filepath` everywhere, handle drive letters and UNC paths on Windows, and expand `~`. Never overwrite an input file; detect output collisions and ask.
- **Line endings and encodings**: read FFmpeg output as bytes and split on both `\n` and `\r`, since FFmpeg uses carriage returns in its stats lines.
- **Process cleanup**: when fftui exits or crashes, child FFmpeg processes must not keep running. On every OS, quitting (including SIGINT, SIGTERM, and SIGHUP) stops each job with `q` and waits for it. FFmpeg runs in its own process group on Unix, so a closing terminal signals only fftui, which then stops FFmpeg cleanly instead of FFmpeg dying mid-write. If fftui itself is killed or crashes, Linux kills FFmpeg through `Pdeathsig` and Windows through a kill-on-close job object. macOS and the BSDs have no equivalent, so there `kill -9` on fftui leaves FFmpeg running.

## Features

### v1 (MVP)

1. **File picker**: browse the file system, filter to media files, select one or many inputs. Paths can also be passed on the command line (`fftui video.mp4`).
2. **Media info view**: container, duration, size, bitrate, and per-stream details (codec, resolution, frame rate, pixel format, sample rate, channels, language, subtitles) from `ffprobe`.
3. **Task presets**, each a form with sensible defaults:
   - Convert container / remux (no re-encode when codecs allow it)
   - Transcode video (H.264, H.265, VP9, AV1) with quality (CRF) or bitrate, speed preset, and audio options
   - Compress to a target file size (computes bitrate from duration; two-pass optional)
   - Extract audio (MP3, AAC, Opus, FLAC, WAV)
   - Trim / cut by start and end time, with a fast stream-copy option
   - Resize / change frame rate
   - Make a GIF (palette generated and applied in one FFmpeg run with `palettegen` and `paletteuse`, for good quality without a temporary file)
   - Strip or select streams (drop subtitles, keep one audio language)
4. **Command preview**: the exact command, updated live as the form changes, with a key to copy it to the clipboard (OSC 52 so it works over SSH, with a native clipboard fallback).
5. **Raw mode**: edit the arguments directly for anything the presets do not cover, either from the Custom command task or by opening a preset's command as text. The text is split like a shell would (spaces separate arguments, `'` and `"` quote) but never run through a shell. `fftui -- <ffmpeg arguments>` also runs a command as a job.
6. **Job runner**: progress bar, percentage, elapsed and estimated remaining time, speed, output size, and a scrollable log of FFmpeg's stderr. Clear error messages when FFmpeg fails, with the last lines of its log.
7. **Hardware encoder choice on every run**: each encoding form has an Encoder field that lists the software encoder and every hardware encoder the local FFmpeg build has (for example `h264_nvenc`, `h264_qsv`, `h264_vaapi`, `h264_videotoolbox`, `h264_amf`). The user picks one for each run. fftui does not save the choice or reuse it as a default, so the field always starts on the software encoder. If a hardware encoder fails to start (for example, no device or driver), the job shows FFmpeg's error and offers to retry with the software encoder.
8. **Batch**: apply one preset to many inputs and run them as a queue. The number of jobs that run at once is a setting (see below), not a per-batch prompt.
9. **Settings screen**: edits `config.toml` from inside the TUI. v1 settings:
   - `jobs`: how many FFmpeg jobs run at once (default 1, minimum 1)
   - `ffmpeg_path` and `ffprobe_path`: optional custom binaries
   - `output_dir`: default output folder (empty means next to the input)
   - `ascii`: plain-ASCII drawing for limited terminals

### Later

- User-defined presets saved to `presets.toml`, plus import and export.
- Concatenate files, burn in subtitles, add or replace audio tracks, watermark overlay.
- Thumbnails or frame preview in terminals that support sixel or the kitty graphics protocol.
- Theme support and remappable key bindings.
- A non-interactive `fftui run <preset> <files>` command that reuses the same presets for scripts.

## Architecture

```
cmd/fftui/            main: flags, config loading, starts the TUI
internal/ffmpeg/      locate binaries, detect capabilities, run ffprobe, run ffmpeg jobs, parse progress
internal/preset/      task definitions: form fields, defaults, validation, and the function that turns options into an argument slice
internal/job/         queue, workers, cancellation, child-process tracking
internal/config/      config file and user presets
internal/ui/          Bubble Tea models: app shell and one model per screen (picker, info, preset form, command preview, jobs, log, settings)
internal/ui/theme/    styles, color and ASCII fallbacks
```

Rules that keep it testable and portable:

- `internal/preset` is pure: options in, `[]string` out. No I/O, no UI.
- `internal/ffmpeg` is the only package that starts processes. It exposes a small interface so the UI and job queue can be tested with a fake runner.
- Long-running work (probing, encoding) runs in commands that send messages back to the Bubble Tea program, so the UI never blocks.
- OS-specific code lives in files with build tags (`proc_windows.go`, `proc_unix.go`) and is kept as small as possible.

## Testing

- **Unit tests** for every preset's argument builder, with table tests that assert on the exact argument slices, including paths with spaces, quotes, and non-ASCII characters.
- **Parser tests** for `ffprobe` JSON and `-progress` output, using fixtures captured from several FFmpeg versions (at least the oldest supported and the latest).
- **Integration tests** that generate small media with FFmpeg's `lavfi` sources (`testsrc2`, `sine`), run real presets, and check the output with `ffprobe` (codec, duration, resolution). Skipped when FFmpeg is not installed.
- **Hardware encoders**: CI runners have no GPUs, so automated tests only cover the encoder list and the arguments each choice produces. Each hardware encoder gets a manual test on real hardware before a release.
- **UI tests** for key flows (pick a file, choose a preset, start, cancel, batch, settings), driving the Bubble Tea model directly with key messages and a fake job runner, and checking the rendered screen at several terminal sizes and in ASCII mode.
- **CI** (GitHub Actions) on Linux, macOS, and Windows runners, installing FFmpeg on each, running `go vet`, `staticcheck`, unit and integration tests. A cross-compile job builds every release target to catch build-tag mistakes. A FreeBSD job (through a VM action) runs the tests on at least one BSD.

## Distribution

- GoReleaser builds archives (`.tar.gz`, `.zip` for Windows) and checksums for all targets on each tagged release.
- Package managers: Homebrew tap, Scoop bucket, winget manifest, AUR package, `.deb`, `.rpm`, and `.apk` through nfpm, and `go install github.com/part-avocado/fftui/cmd/fftui@latest`.
- Later: submit to the FreeBSD ports tree, Nixpkgs, and Debian/Ubuntu.
- Sign and notarize macOS binaries if a developer account is available; otherwise document the Gatekeeper workaround.

## Supported FFmpeg versions

Target FFmpeg 4.4 and later, which covers current LTS distributions. Features that need newer FFmpeg are enabled based on the detected version and capabilities rather than failing at run time.

## Milestones

1. **Skeleton**: Go module, `cmd/fftui`, Bubble Tea app shell with screen switching and help bar, config loading, CI on three OSes, GoReleaser snapshot builds for every target.
2. **FFmpeg core**: binary discovery, missing-FFmpeg screen, capability detection, `ffprobe` wrapper, and media info view.
3. **Running jobs**: job runner with `-progress` parsing, progress view, log view, cancellation, and child-process cleanup on every OS.
4. **Presets**: preset framework with forms and live command preview, the per-run hardware encoder field with software retry, then the v1 presets one by one, each with argument-builder tests and an integration test.
5. **Batch, settings, and polish**: multi-file queue honoring the `jobs` setting, settings screen, raw mode, clipboard copy, ASCII mode, error messages, key-binding help, README with screenshots.
6. **Release v0.1.0**: tagged release with binaries for all targets and the first package-manager manifests. Pushing a `v*` tag runs `.github/workflows/release.yml`, which publishes the GitHub release with `docs/releases/<tag>.md` as its description. The Homebrew formula lives in `part-avocado/homebrew-tap`, builds from the release's source tarball, and depends on `ffmpeg`; a scheduled workflow in the tap points it at each new release, so no cross-repository token is needed. Windows: a Scoop bucket (`part-avocado/scoop-bucket`) with the same kind of updater and a Windows install test, and winget manifests in `packaging/winget/`, laid out like `microsoft/winget-pkgs` and checked by `.github/workflows/winget.yml` with the real winget before each submission.
