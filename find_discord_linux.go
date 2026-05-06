/*
 * SPDX-License-Identifier: GPL-3.0
 * Vencord Installer, a cross platform gui/cli app for installing Vencord
 * Copyright (c) 2023 Vendicated and Vencord contributors
 */

package main

import (
	"errors"
	"io/fs"
	"os"
	"os/user"
	path "path/filepath"
	"strconv"
	"strings"
)

var (
	Home        string
	DiscordDirs []string
)

func init() {
	// If ran as root, the HOME environment variable will be that of root.
	// SUDO_USER and DOAS_USER tell us the actual user
	var sudoUser = os.Getenv("SUDO_USER")
	if sudoUser == "" {
		sudoUser = os.Getenv("DOAS_USER")
		if sudoUser != "" {
			_ = os.Setenv("SUDO_USER", sudoUser)
		}
	}
	if sudoUser != "" {
		if sudoUser == "root" {
			panic("VencordInstaller must not be run as the root user. Please rerun as normal user. Use sudo or doas to run as root.")
		}

		Log.Debug("VencordInstaller was run with root privileges, actual user is", sudoUser)
		Log.Debug("Looking up HOME of", sudoUser)

		u, err := user.Lookup(sudoUser)
		if err != nil {
			Log.Warn("Failed to lookup HOME", err)
		} else {
			Log.Debug("Actual HOME is", u.HomeDir)
			_ = os.Setenv("HOME", u.HomeDir)
		}
	} else if os.Getuid() == 0 {
		panic("VencordInstaller was run as root but neither SUDO_USER nor DOAS_USER are set. Please rerun me as a normal user, with sudo/doas, or manually set SUDO_USER to your username")
	}
	Home = os.Getenv("HOME")

	DiscordDirs = []string{
		"/usr/share",
		"/usr/lib64",
		"/opt",
		path.Join(Home, ".local/share"),
		path.Join(Home, ".dvm"),
		"/var/lib/flatpak/app",
		path.Join(Home, "/.local/share/flatpak/app"),
	}
}

func ParseDiscord(p, _ string) *DiscordInstall {
	name := path.Base(p)

	if strings.HasPrefix(name, "app-") {
		parent := path.Base(path.Dir(p))
		if parent != "." && parent != "/" {
			name = parent
		}
	}

	needsFlatpakResolve := strings.Contains(p, "/flatpak/") && !strings.Contains(p, "/current/active/files/")
	if needsFlatpakResolve {
		discordName := strings.ToLower(name[len("com.discordapp."):])
		if discordName != "discord" {
			discordName = discordName[:7] + "-" + discordName[7:]
		}
		p = path.Join(p, "current/active/files", discordName)
	}

	resources := path.Join(p, "resources")
	app := path.Join(resources, "app")

	isPatched, isSystemElectron := false, false

	if ExistsFile(resources) {
		isPatched = ExistsFile(path.Join(resources, "_app.asar"))
	} else if ExistsFile(path.Join(p, "app.asar")) {
		isSystemElectron = true
		isPatched = ExistsFile(path.Join(p, "_app.asar.unpacked"))
	} else {
		Log.Warn("Tried to parse invalid Location:", p)
		return nil
	}

	return &DiscordInstall{
		path:             p,
		branch:           GetBranch(name),
		appPath:          app,
		isPatched:        isPatched,
		isFlatpak:        needsFlatpakResolve,
		isSystemElectron: isSystemElectron,
	}
}

func FindLatestAppDir(base string) string {
	children, err := os.ReadDir(base)
	if err != nil {
		return ""
	}

	var latest string
	var latestVer []int

	for _, child := range children {
		name := child.Name()
		if !child.IsDir() || !strings.HasPrefix(name, "app-") {
			continue
		}

		verStr := strings.TrimPrefix(name, "app-")
		parts := strings.Split(verStr, ".")
		var ver []int

		for _, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil {
				n = 0
			}
			ver = append(ver, n)
		}

		if latest == "" || CompareAppVersions(ver, latestVer) > 0 {
			latest = name
			latestVer = ver
		}
	}

	if latest == "" {
		return ""
	}

	return path.Join(base, latest)
}

func CompareAppVersions(a, b []int) int {
	l := len(a)
	if len(b) > l {
		l = len(b)
	}

	for i := 0; i < l; i++ {
		ai, bi := 0, 0
		if i < len(a) {
			ai = a[i]
		}
		if i < len(b) {
			bi = b[i]
		}
		if ai > bi {
			return 1
		}
		if ai < bi {
			return -1
		}
	}
	return 0
}

func FindDiscords() []any {
	var discords []any
	for _, dir := range DiscordDirs {
		children, err := os.ReadDir(dir)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				Log.Warn("Error during readdir "+dir+":", err)
			}
			continue
		}

		for _, child := range children {
			name := child.Name()
			if !child.IsDir() || !SliceContains(LinuxDiscordNames, name) {
				continue
			}

			discordDir := path.Join(dir, name)
			if discord := ParseDiscord(discordDir, ""); discord != nil {
				Log.Debug("Found Discord install at ", discordDir)
				discords = append(discords, discord)
			}
		}
	}

	configBase := path.Join(Home, ".config")
	configDirs, err := os.ReadDir(configBase)
	if err == nil {
		for _, child := range configDirs {
			name := child.Name()
			if !child.IsDir() || !SliceContains(LinuxDiscordNames, name) {
				continue
			}

			base := path.Join(configBase, name)
			appDir := FindLatestAppDir(base)
			if appDir == "" {
				continue
			}

			if discord := ParseDiscord(appDir, ""); discord != nil {
				Log.Debug("Found Discord install at ", appDir)
				discords = append(discords, discord)
			}
		}
	}

	return discords
}

func PreparePatch(di *DiscordInstall) {}

// FixOwnership fixes file ownership on Linux
func FixOwnership(p string) error {
	if os.Geteuid() != 0 {
		return nil
	}

	Log.Debug("Fixing Ownership of", p)

	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" {
		panic("SUDO_USER was empty. This point should never be reached")
	}

	Log.Debug("Looking up User", sudoUser)
	u, err := user.Lookup(sudoUser)
	if err != nil {
		Log.Error("Lookup failed:", err)
		return err
	}
	Log.Debug("Lookup successful, Uid", u.Uid, "Gid", u.Gid)
	// This conversion is safe because of the GOOS guard above
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	err = path.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err == nil {
			err = os.Chown(path, uid, gid)
			Log.Debug("chown", u.Uid+":"+u.Gid, path+":", Ternary(err == nil, "Success!", "Failed"))
		}
		return err
	})

	if err != nil {
		Log.Error("Failed to fix ownership:", err)
	}
	return err
}

func CheckScuffedInstall() bool {
	return false
}
