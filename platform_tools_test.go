package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMediaToolResolution(t *testing.T) {
	for _, tc := range []struct{ name, os, override, available, want string }{
		{"mac arm brew", "darwin", "", "/opt/homebrew/bin/unrar", "/opt/homebrew/bin/unrar"},
		{"mac intel brew", "darwin", "", "/usr/local/bin/unrar", "/usr/local/bin/unrar"},
		{"mac source install", "darwin", "", "/Users/test/.local/share/local-dl/bin/unrar", "/Users/test/.local/share/local-dl/bin/unrar"},
		{"linux path", "linux", "", "unrar", "unrar"},
		{"linux no mac fallback", "linux", "", "/opt/homebrew/bin/unrar", ""},
		{"explicit tool", "darwin", "/custom/unrar", "/custom/unrar", "/custom/unrar"},
		{"bad override holds", "darwin", "/missing/unrar", "/opt/homebrew/bin/unrar", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(p string) (string, error) {
				if p == tc.available {
					return p, nil
				}
				return "", fmt.Errorf("missing")
			}
			got, e := resolveMediaTool("unrar", tc.override, tc.os, "/Users/test", lookup)
			if got != tc.want || (e != nil) != (tc.want == "") {
				t.Fatalf("got %q, %v; want %q", got, e, tc.want)
			}
		})
	}
}
func TestNativeTemporaryDirectorySafety(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS system aliases")
	}
	root, e := os.MkdirTemp("/tmp", "local-dl-platform-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(root)
	if e = musicMkdirAll(filepath.Join(root, "child")); e != nil {
		t.Fatal(e)
	}
	if e = safeArchiveDestination(root); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(filepath.Join(root, "child"), filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	if e = safeArchiveDestination(filepath.Join(root, "link")); e == nil {
		t.Fatal("accepted user symlink")
	}
	if e = musicMkdirAll(filepath.Join(root, "link", "unsafe")); e == nil {
		t.Fatal("created through user symlink")
	}
}

func TestArchiveDestinationRejectsSymlinkWithTrailingSeparator(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if e := os.Mkdir(target, 0700); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(root, "link")
	if e := os.Symlink(target, link); e != nil {
		t.Fatal(e)
	}
	if e := safeArchiveDestination(link + string(filepath.Separator)); e == nil {
		t.Fatal("accepted trailing slash on symlink")
	}
}

func TestConfiguredLibraryRoots(t *testing.T) {
	oldMovies, oldTV := MoviesPath, TVShowPath
	t.Cleanup(func() { MoviesPath, TVShowPath = oldMovies, oldTV })
	t.Setenv("MOVIES_PATH", "/Volumes/Plex/Movies")
	t.Setenv("TVSHOW_PATH", "/Volumes/Plex/TV")
	configureLibraryPaths()
	if MoviesPath != "/Volumes/Plex/Movies" || TVShowPath != "/Volumes/Plex/TV" {
		t.Fatal("ignored native library paths")
	}
	t.Setenv("MOVIES_PATH", "")
	t.Setenv("TVSHOW_PATH", "")
	configureLibraryPaths()
	if MoviesPath != "/mnt/movies" || TVShowPath != "/mnt/tvshows" {
		t.Fatal("changed container defaults")
	}
}
