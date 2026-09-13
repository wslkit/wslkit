//go:build windows

package cli

import (
	"flag"
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/disk"
)

func (a *App) diskConfig(args []string) int {
	verb := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		verb, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("disk config "+verb, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var f diskFlags
	f.register(fs)

	// The key and value may be written before the flags, which is how people
	// type them, and the flag package stops at the first bare word.
	var rest []string
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") && len(rest) < 2 {
		rest = append(rest, args[0])
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	rest = append(rest, fs.Args()...)

	switch verb {
	case "":
		return a.configShow(f)
	case "path":
		// Printed without reading the file, so it works even when the
		// file is unparseable and the user needs to go and fix it.
		return a.configPath(f)
	case "get":
		return a.configGet(f, rest)
	case "set":
		return a.configSet(f, rest)
	case "edit":
		return a.configEdit(f)
	default:
		fmt.Fprintf(a.Stderr, "unknown config verb %q; the verbs are: path, get, set, edit\n", verb)
		return ExitUsage
	}
}

func (a *App) configShow(f diskFlags) int {
	e := diskEnv()
	c, path, err := disk.LoadConfig(e.FS)
	if err != nil {
		// `config` itself fails on an unreadable file, unlike every other
		// command, which warns and carries on: the user is here to look
		// at the file, so hiding the problem would be unhelpful.
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}
	wsl := disk.WslConfigValues(e.FS)

	if f.jsonOut {
		if err := disk.WriteJSONLine(a.Stdout, disk.ConfigJSON(path, c, wsl)); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}

	fmt.Fprintf(a.Stdout, "%s\n\n", path)
	var d disk.Details
	for _, k := range disk.ConfigKeys() {
		v, _ := disk.ConfigValue(c, k)
		d.Add(k, v)
	}
	fmt.Fprint(a.Stdout, d.String())
	if len(wsl) > 0 {
		fmt.Fprintf(a.Stdout, "\nfrom .wslconfig (read-only):\n")
		var w disk.Details
		for _, k := range []string{"wsl2.defaultVhdSize", "wsl2.vhdSize", "wsl2.swapFile"} {
			if v, ok := wsl[k]; ok {
				w.Add(k, v)
			}
		}
		fmt.Fprint(a.Stdout, w.String())
	}
	return ExitOK
}

func (a *App) configPath(f diskFlags) int {
	path, err := disk.ConfigPath(diskEnv().FS)
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}
	if f.jsonOut {
		if err := disk.WriteJSONLine(a.Stdout, map[string]any{"path": path}); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	fmt.Fprintln(a.Stdout, path)
	return ExitOK
}

func (a *App) configGet(f diskFlags, args []string) int {
	if len(args) > 1 {
		fmt.Fprintln(a.Stderr, "usage: wslkit disk config get [KEY]")
		return ExitUsage
	}
	e := diskEnv()
	c, _, err := disk.LoadConfig(e.FS)
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}

	if len(args) == 0 {
		if f.jsonOut {
			o := map[string]any{}
			for _, k := range disk.ConfigKeys() {
				v, _ := disk.ConfigValue(c, k)
				o[k] = v
			}
			if err := disk.WriteJSONLine(a.Stdout, o); err != nil {
				fmt.Fprintf(a.Stderr, "error: %v\n", err)
				return ExitFindings
			}
			return ExitOK
		}
		for _, k := range disk.ConfigKeys() {
			v, _ := disk.ConfigValue(c, k)
			fmt.Fprintf(a.Stdout, "%s = %s\n", k, v)
		}
		return ExitOK
	}

	key := args[0]
	v, ok := disk.ConfigValue(c, key)
	if !ok {
		err := disk.UnknownSettingError(key)
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}
	if f.jsonOut {
		if err := disk.WriteJSONLine(a.Stdout, map[string]any{"key": key, "value": v}); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	// The bare value on its own line, so $(...) and for /f work.
	fmt.Fprintln(a.Stdout, v)
	return ExitOK
}

func (a *App) configSet(f diskFlags, args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(a.Stderr, "usage: wslkit disk config set KEY VALUE")
		fmt.Fprintf(a.Stderr, "the settings are: %s\n", joinKeys())
		return ExitUsage
	}
	key, value := args[0], args[1]

	e := diskEnv()
	c, path, err := disk.LoadConfig(e.FS)
	if err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}
	if err := disk.SetConfigValue(&c, key, value); err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}
	// Read back what was stored rather than echoing what was typed, so the
	// user sees the value as the file will hold it.
	stored, _ := disk.ConfigValue(c, key)

	if f.dryRun {
		if f.jsonOut {
			if err := disk.WriteJSONLine(a.Stdout, map[string]any{
				"path": path, "dry_run": true, "key": key, "value": stored,
			}); err != nil {
				fmt.Fprintf(a.Stderr, "error: %v\n", err)
				return ExitFindings
			}
			return ExitOK
		}
		fmt.Fprintf(a.Stdout, "--dry-run: %s was not changed. It would have set %s = %s\n", path, key, stored)
		return ExitOK
	}

	if _, err := disk.SaveConfig(e.FS, c); err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}
	if f.jsonOut {
		if err := disk.WriteJSONLine(a.Stdout, map[string]any{"key": key, "value": stored}); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return ExitFindings
		}
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "%s = %s\n", key, stored)
	return ExitOK
}

func (a *App) configEdit(f diskFlags) int {
	e := diskEnv()
	c, path, err := disk.LoadConfig(e.FS)
	if err != nil {
		// Still editable: an unparseable file is exactly what someone
		// would open an editor to fix.
		fmt.Fprintf(a.Stderr, "warning: %v\n", err)
	}
	if !e.FS.Exists(path) {
		if _, err := disk.SaveConfig(e.FS, c); err != nil {
			fmt.Fprintf(a.Stderr, "error: %v\n", err)
			return diskExitFor(err)
		}
	}
	if f.dryRun {
		if f.jsonOut {
			if err := disk.WriteJSONLine(a.Stdout, map[string]any{"path": path, "dry_run": true}); err != nil {
				fmt.Fprintf(a.Stderr, "error: %v\n", err)
				return ExitFindings
			}
			return ExitOK
		}
		fmt.Fprintf(a.Stdout, "--dry-run: would open %s\n", path)
		return ExitOK
	}
	if err := disk.OpenEditor(e.FS, path); err != nil {
		fmt.Fprintf(a.Stderr, "error: %v\n", err)
		return diskExitFor(err)
	}
	return ExitOK
}

func joinKeys() string {
	keys := disk.ConfigKeys()
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ", "
		}
		out += k
	}
	return out
}

// diskConfigOrDefaults loads the settings for a command that is not `config`.
//
// Every other command warns and carries on when the file cannot be read: the
// user asked to compact a disk, and a broken settings file is a reason to tell
// them, not a reason to refuse.
func (a *App) diskConfigOrDefaults(e disk.Env) disk.Config {
	c, _, err := disk.LoadConfig(e.FS)
	if err != nil {
		fmt.Fprintf(a.Stderr, "warning: using the built-in defaults: %v\n", err)
		return disk.DefaultConfig()
	}
	return c
}
