// Command fftui is a terminal interface for FFmpeg.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"slices"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"github.com/part-avocado/fftui/internal/config"
	"github.com/part-avocado/fftui/internal/ffmpeg"
	"github.com/part-avocado/fftui/internal/job"
	"github.com/part-avocado/fftui/internal/ui"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fftui:", err)
		os.Exit(1)
	}
}

func run() error {
	defaultPath, pathErr := config.DefaultPath()
	configPath := flag.String("config", defaultPath, "path to the config file")
	ffmpegPath := flag.String("ffmpeg", "", "path to the ffmpeg program (default: $FFTUI_FFMPEG, the config file, then PATH)")
	ascii := flag.Bool("ascii", false, "draw with plain ASCII characters only")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: fftui [flags] [media files] [-- ffmpeg arguments]\n\n"+
			"Arguments after -- run as an FFmpeg job, such as:\n  fftui -- -i in.mp4 -c:v libx264 out.mkv\n\nFlags:\n")
		flag.PrintDefaults()
	}
	// Everything after the first -- is an FFmpeg command, which can contain
	// arguments that look like fftui's flags.
	args, command := os.Args[1:], []string(nil)
	if i := slices.Index(args, "--"); i >= 0 {
		args, command = args[:i], args[i+1:]
		if len(command) == 0 {
			return fmt.Errorf("no FFmpeg arguments after --")
		}
	}
	if err := flag.CommandLine.Parse(args); err != nil {
		return err
	}

	if *showVersion {
		fmt.Println("fftui", buildVersion())
		return nil
	}
	if *configPath == "" {
		return fmt.Errorf("no config directory found (%v); pass --config", pathErr)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	var distro []string
	if runtime.GOOS == "linux" {
		distro = ffmpeg.Distro()
	}

	queue := job.NewQueue(cfg.Jobs, job.StartFFmpeg)
	// However fftui exits, stop the encodes it started. On Linux and
	// Windows the OS also kills them if fftui itself is killed.
	defer func() {
		if n := queue.Active(); n > 0 {
			fmt.Fprintf(os.Stderr, "fftui: stopping %d FFmpeg job(s), up to %v...\n", n, job.StopGrace)
		}
		// FFmpeg runs in its own process group, so it does not see these;
		// dying from one now would leave it running on macOS and the BSDs.
		signal.Ignore(os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		queue.StopAll()
	}()

	app := ui.New(ui.Options{
		Config:     cfg,
		ConfigPath: *configPath,
		ASCII:      *ascii,
		FindFFmpeg: func(ctx context.Context, cfg config.Config) (*ffmpeg.Tools, error) {
			// The first of --ffmpeg, FFTUI_FFMPEG, and the config file wins.
			search := ffmpeg.Search{FFprobe: cfg.FFprobePath, Dirs: ffmpeg.CommonDirs()}
			switch env := os.Getenv("FFTUI_FFMPEG"); {
			case *ffmpegPath != "":
				search.FFmpeg, search.Source = *ffmpegPath, "--ffmpeg"
			case env != "":
				search.FFmpeg, search.Source = env, "FFTUI_FFMPEG"
			case cfg.FFmpegPath != "":
				search.FFmpeg, search.Source = cfg.FFmpegPath, "config ffmpeg_path"
			}
			return ffmpeg.Find(ctx, search, ffmpeg.Exec{})
		},
		Install: ffmpeg.InstallCommands(runtime.GOOS, distro),
		Files:   flag.Args(),
		Jobs:    queue,
		Command: command,
	})
	p := tea.NewProgram(app)
	go func() {
		for range queue.Changed() {
			p.Send(ui.JobsChanged{})
		}
	}()
	// Bubble Tea quits on SIGINT and SIGTERM. A closed terminal sends
	// SIGHUP, which would otherwise end fftui without stopping its jobs.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		<-hup
		p.Quit()
	}()

	_, err = p.Run()
	return err
}

// buildVersion returns the release version, or the module version when built
// with `go install`.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}
