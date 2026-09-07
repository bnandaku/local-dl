package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Explicit operator paths are authoritative. Native macOS services commonly
// omit Homebrew from PATH; Linux keeps its existing PATH-based resolution.
func findMediaTool(name string) (string, error) {
	home, _ := os.UserHomeDir()
	return resolveMediaTool(name, os.Getenv(strings.ToUpper(name)+"_PATH"), runtime.GOOS, home, exec.LookPath)
}
func resolveMediaTool(name, override, platform, home string, lookup func(string) (string, error)) (string, error) {
	if override != "" {
		return lookup(override)
	}
	candidates := []string{name}
	if platform == "darwin" {
		if home != "" {
			candidates = append(candidates, filepath.Join(home, ".local/share/local-dl/bin", name))
		}
		candidates = append(candidates, filepath.Join("/opt/homebrew/bin", name), filepath.Join("/usr/local/bin", name))
	}
	for _, p := range candidates {
		if found, e := lookup(p); e == nil {
			return found, nil
		}
	}
	return "", fmt.Errorf("%s unavailable on %s; install media tools or configure %s_PATH", name, platform, strings.ToUpper(name))
}

// Darwin's OS-owned aliases are expected, unlike links inside user libraries.
// Resolve only an alias with its exact system target; callers still validate
// every component of the resulting path and reject all other symlinks.
func nativeStoragePath(path string) string {
	if runtime.GOOS != "darwin" {
		return path
	}
	path = filepath.Clean(path)
	for _, alias := range []string{"/tmp", "/var", "/etc"} {
		if path != alias && !strings.HasPrefix(path, alias+"/") {
			continue
		}
		target, e := os.Readlink(alias)
		if e != nil {
			return path
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join("/", target)
		}
		if filepath.Clean(target) != "/private"+alias {
			return path
		}
		return "/private" + path
	}
	return path
}
