package ffmpeg

import (
	"os"
	"slices"
	"strings"
)

// DownloadURL lists FFmpeg builds for every OS.
const DownloadURL = "https://ffmpeg.org/download.html"

// InstallCommands returns the commands that install FFmpeg on goos, or nil
// when fftui does not know them; then point the user to DownloadURL. On
// Linux, distro is the distribution's ID followed by the IDs it is like, as
// from Distro, so Linux Mint ("linuxmint", "ubuntu", "debian") gets apt.
func InstallCommands(goos string, distro []string) []string {
	switch goos {
	case "darwin":
		return []string{"brew install ffmpeg", "sudo port install ffmpeg"}
	case "windows":
		return []string{"winget install --id Gyan.FFmpeg", "scoop install ffmpeg", "choco install ffmpeg"}
	case "freebsd", "dragonfly":
		return []string{"sudo pkg install ffmpeg"}
	case "openbsd":
		return []string{"doas pkg_add ffmpeg"}
	case "linux":
		for _, id := range distro {
			switch id {
			case "termux":
				return []string{"pkg install ffmpeg"}
			case "debian", "ubuntu":
				return []string{"sudo apt install ffmpeg"}
			case "rhel", "centos":
				// Fedora's ffmpeg-free needs EPEL here; send users to the
				// download page rather than give a command that fails.
				return nil
			case "fedora":
				return []string{"sudo dnf install ffmpeg-free"}
			case "arch":
				return []string{"sudo pacman -S ffmpeg"}
			case "alpine":
				return []string{"sudo apk add ffmpeg"}
			case "opensuse", "suse":
				return []string{"sudo zypper install ffmpeg"}
			case "void":
				return []string{"sudo xbps-install ffmpeg"}
			case "gentoo":
				return []string{"sudo emerge media-video/ffmpeg"}
			case "nixos":
				return []string{"nix profile install nixpkgs#ffmpeg"}
			}
		}
	}
	return nil
}

// Distro identifies the Linux distribution from /etc/os-release, returning
// its ID followed by its ID_LIKE entries, or ["termux"] inside Termux.
func Distro() []string {
	if strings.Contains(os.Getenv("PREFIX"), "com.termux") {
		return []string{"termux"}
	}
	for _, path := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		if data, err := os.ReadFile(path); err == nil {
			return ParseOSRelease(string(data))
		}
	}
	return nil
}

// ParseOSRelease returns the ID and ID_LIKE entries of an os-release file.
func ParseOSRelease(s string) []string {
	var id, like []string
	for _, line := range splitLines(s) {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"'`)
		switch key {
		case "ID":
			id = []string{value}
		case "ID_LIKE":
			like = strings.Fields(value)
		}
	}
	// openSUSE's IDs are "opensuse-tumbleweed" or "opensuse-leap".
	ids := slices.Concat(id, like)
	for i, v := range ids {
		if strings.HasPrefix(v, "opensuse") {
			ids[i] = "opensuse"
		}
	}
	return ids
}
