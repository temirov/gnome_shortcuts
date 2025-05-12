// gnome_shortcuts.go
//
// Build
//
//	go mod init gnome_shortcuts
//	go get github.com/manifoldco/promptui
//	go build -o gnome-shortcuts gnome_shortcuts.go
//
// Non-interactive
//
//	KEY_LAYOUT=apple|pc|chrome ./gnome-shortcuts
//
// Interactive
//
//	./gnome-shortcuts          ← ↑ / ↓  or 1-3   (Ctrl-C aborts)
package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/manifoldco/promptui"
)

/*──────────────── keyboard-layout ────────────────*/

type kb int

const (
	kbApple kb = iota
	kbPC
	kbChrome
)

func layout() kb {
	switch strings.ToLower(os.Getenv("KEY_LAYOUT")) {
	case "apple", "mac":
		return kbApple
	case "pc", "windows":
		return kbPC
	case "chrome", "chromebook":
		return kbChrome
	}

	list := []string{
		"Mac / Apple    (Command)",
		"PC / Windows   (Alt)",
		"Chromebook     (Search)",
	}
	sel := promptui.Select{
		Label: "Select keyboard layout",
		Items: list,
		Templates: &promptui.SelectTemplates{
			Active:   "⮕ {{ . }}",
			Inactive: "  {{ . }}",
			Selected: "{{ . }}",
		},
		Size: len(list),
	}
	i, _, err := sel.Run()
	if err != nil {
		os.Exit(130)
	}
	return kb(i)
}

/*──────────── modifier → printable label ──────────*/

func modLabels(k kb) map[string]string {
	m := map[string]string{
		"<Primary>": "Ctrl", "<Control>": "Ctrl", "<Ctrl>": "Ctrl",
		"<Shift>": "Shift",
	}
	switch k {
	case kbApple:
		m["<Alt>"] = "Option"
		m["<Super>"] = "Command"
	case kbChrome:
		m["<Alt>"] = "Alt"
		m["<Super>"] = "Search"
	default:
		m["<Alt>"] = "Alt"
		m["<Super>"] = "Win"
	}
	return m
}

/*──────────────────── string helpers ──────────────*/

func asciiTitle(s string) string {
	if s == "" {
		return s
	}
	r := []rune(strings.ToLower(s))
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func humanise(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "-", " ")
	w := strings.Fields(s)
	for i, v := range w {
		w[i] = asciiTitle(v)
	}
	return strings.Join(w, " ")
}

/*──────── schema → app name + precedence rank ─────*/

func classify(schema string) (string, int) {
	switch {
	case strings.Contains(schema, ".desktop.wm.keybindings"),
		strings.Contains(schema, ".mutter.wayland.keybindings"),
		strings.Contains(schema, ".mutter.keybindings"):
		return "Window Manager", 1
	case strings.Contains(schema, ".shell.keybindings"):
		return "GNOME Shell", 2
	case strings.Contains(schema, ".settings-daemon.plugins.media-keys"):
		return "Media Keys", 2
	case strings.Contains(schema, ".custom-keybinding"):
		return "Custom", 4
	}
	trim := strings.TrimSuffix(schema, ".keybindings")
	trim = strings.TrimPrefix(trim, "org.")
	trim = trim[strings.Index(trim, ".")+1:] // drop vendor prefix
	segs := strings.Split(trim, ".")
	return humanise(segs[len(segs)-1]), 3
}

/*──────────────── accelerator formatting ──────────*/

var tokenRE = regexp.MustCompile(`(<[^>]+>|[A-Za-z0-9_]+)`)

func fmtAccel(spec string, lbl map[string]string) (string, bool) {
	if strings.Contains(spec, "XF86") {
		return "", false
	}
	var out []string
	for _, t := range tokenRE.FindAllString(spec, -1) {
		switch {
		case lbl[t] != "":
			out = append(out, lbl[t])
		case strings.HasPrefix(t, "<") && strings.HasSuffix(t, ">"):
			out = append(out, humanise(strings.Trim(t, "<>")))
		default:
			out = append(out, humanise(t))
		}
	}
	if len(out) == 0 {
		return "", false
	}
	return strings.Join(out, " + "), true
}

/*────────── static hard-wired shortcuts (immutable) ─────────*/

type staticBind struct{ spec, action string }

var coreShortcuts = []staticBind{
	{"<Super>", "Show Activities / Search"},
	{"<Super>+Left", "Tile Window Left"},
	{"<Super>+Right", "Tile Window Right"},
	{"<Super>+Up", "Maximise Window"},
	{"<Super>+Down", "Restore / Minimise Window"},
}

/*──────────────────── collect shortcuts ───────────*/

type row struct {
	accel, app, action string
	rank, prio         int
}

var quoteRE = regexp.MustCompile(`'([^']*)'`)

func gsettingsDump() []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, _ := exec.CommandContext(ctx, "gsettings", "list-recursively").Output()
	return b
}

type custom struct{ binding, name, command string }

func collect(lbl map[string]string) []row {
	unique := map[string]row{}
	customMap := map[string]*custom{}

	sc := bufio.NewScanner(bytes.NewReader(gsettingsDump()))
	for sc.Scan() {
		line := sc.Text()
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		schema, key := f[0], f[1]
		val := strings.Join(f[2:], " ")

		/* handle per-user custom keybinding paths */
		if strings.Contains(schema, ".custom-keybinding") {
			pathKey := schema[strings.Index(schema, ":")+1:]
			c := customMap[pathKey]
			if c == nil {
				c = &custom{}
				customMap[pathKey] = c
			}
			switch key {
			case "binding":
				c.binding = strings.Trim(val, "'")
			case "name":
				c.name = strings.Trim(val, "'")
			case "command":
				c.command = strings.Trim(val, "'")
			}
			continue
		}

		if !strings.Contains(schema, "keybinding") {
			continue
		}
		for _, m := range quoteRE.FindAllStringSubmatch(val, -1) {
			acc, ok := fmtAccel(m[1], lbl)
			if !ok {
				continue
			}
			app, rk := classify(schema)
			id := schema + ":" + key + ":" + m[1]
			unique[id] = row{acc, app, humanise(key), rk, 1000}
		}
	}

	/* add custom bindings */
	for _, c := range customMap {
		if acc, ok := fmtAccel(c.binding, lbl); ok {
			app := humanise(path.Base(c.command))
			if app == "" {
				app = "Custom"
			}
			action := humanise(c.name)
			if action == "" {
				action = c.command
			}
			unique["custom:"+c.binding] = row{acc, app, action, 4, 1000}
		}
	}

	/* add static immutable bindings with priority according to list order */
	for i, s := range coreShortcuts {
		if acc, ok := fmtAccel(s.spec, lbl); ok {
			unique["static:"+s.spec] = row{acc, "Window Manager", s.action, 0, i}
		}
	}

	out := make([]row, 0, len(unique))
	for _, r := range unique {
		out = append(out, r)
	}
	return out
}

/*──────────────────── table helpers ───────────────*/

const rowFmt = "%-28s %-28s %-40s\n"

func printRow(a, b, c string) { fmt.Printf(rowFmt, a, b, c) }

/*──────────────────────── main ─────────────────────*/

func main() {
	lbl := modLabels(layout())
	rows := collect(lbl)

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].rank != rows[j].rank {
			return rows[i].rank < rows[j].rank
		}
		if rows[i].prio != rows[j].prio {
			return rows[i].prio < rows[j].prio
		}
		if rows[i].app != rows[j].app {
			return rows[i].app < rows[j].app
		}
		return rows[i].action < rows[j].action
	})

	line := strings.Repeat("─", 100)
	fmt.Println(line)
	printRow("Shortcut", "Application", "Action")
	fmt.Println(line)
	for _, r := range rows {
		printRow(r.accel, r.app, r.action)
	}
}
