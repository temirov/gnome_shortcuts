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
//	./gnome-shortcuts          ← ↑ / ↓  or 1–3   (Ctrl-C aborts)
//
// Goal
//   - show *only* the shortcut that will actually fire
//     when several GNOME actions share the same key-combo
//   - no hand-written “special cases”
//   - priority is derived from Mutter’s own gschema files
package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/manifoldco/promptui"
)

/*──────────────── keyboard layout ───────────────*/

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
	items := []string{
		"Mac / Apple    (Command)",
		"PC / Windows   (Alt)",
		"Chromebook     (Search)",
	}
	sel := promptui.Select{
		Label: "Select keyboard layout",
		Items: items,
		Templates: &promptui.SelectTemplates{
			Active:   "⮕ {{ . }}",
			Inactive: "  {{ . }}",
			Selected: "{{ . }}",
		},
		Size: len(items),
	}
	i, _, err := sel.Run()
	if err != nil {
		os.Exit(130)
	}
	return kb(i)
}

/*────────── modifier → printable label ──────────*/

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

/*────────────────── helpers ───────────────────*/

func titleCase(s string) string {
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
	for i := range w {
		w[i] = titleCase(w[i])
	}
	return strings.Join(w, " ")
}

/*──────────── accelerator formatting ───────────*/

var tokenRE = regexp.MustCompile(`(<[^>]+>|[A-Za-z0-9_]+)`)

func fmtAccel(spec string, lbl map[string]string) (string, bool) {
	if strings.Contains(spec, "XF86") { // media keys – skip
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

/*────── immutable Mutter shortcuts (Activities etc.) ─────*/

type staticBind struct{ spec, action string }

var coreShortcuts = []staticBind{
	{"<Super>", "Show Activities / Search"},
	{"<Super>+Left", "Tile Window Left"},
	{"<Super>+Right", "Tile Window Right"},
	{"<Super>+Up", "Maximise Window"},
	{"<Super>+Down", "Restore / Minimise Window"},
}

/*
───────────────────── schema ordering ──────────

	Mutter resolves conflicts by the *order in the
	corresponding gschema.xml file*.

	We parse those XML files once to obtain an
	action → order index map (no hand-written list).
*/
var schemaDirs = []string{
	"/usr/share/glib-2.0/schemas",
	"/usr/local/share/glib-2.0/schemas",
}

func loadKeyOrder(schemaID string) map[string]int {
	order := map[string]int{}
	var path string
	for _, dir := range schemaDirs {
		filepath.WalkDir(dir, func(p string, d os.DirEntry, _ error) error {
			if path != "" || !strings.HasSuffix(p, ".gschema.xml") {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			if bytes.Contains(data, []byte(`id="`+schemaID+`"`)) {
				path = p
			}
			return nil
		})
		if path != "" {
			break
		}
	}
	if path == "" {
		return order // fallback: empty ⇒ “last”
	}
	reKey := regexp.MustCompile(`<key[^>]*name="([^"]+)"`)
	data, _ := os.ReadFile(path)
	idx := 0
	for _, m := range reKey.FindAllSubmatch(data, -1) {
		order[string(m[1])] = idx
		idx++
	}
	return order
}

/*──────── schema → app & rank (family) ────────*/

func classify(schema, key string) (app string, rank int) {
	switch {
	case strings.Contains(schema, ".desktop.wm.keybindings"),
		strings.Contains(schema, ".mutter.wayland.keybindings"),
		strings.Contains(schema, ".mutter.keybindings"):
		return "Window Manager", 0
	case strings.Contains(schema, ".shell.keybindings"):
		return "GNOME Shell", 1
	case strings.Contains(schema, ".settings-daemon.plugins.media-keys"):
		return "Media Keys", 1
	case strings.Contains(schema, ".custom-keybinding"):
		return "Custom", 3
	}
	trim := strings.TrimSuffix(schema, ".keybindings")
	trim = strings.TrimPrefix(trim, "org.")
	if idx := strings.IndexByte(trim, '.'); idx >= 0 {
		trim = trim[idx+1:]
	}
	seg := trim[strings.LastIndexByte(trim, '.')+1:]
	return humanise(seg), 2
}

/*──────────── gather gsettings bindings ───────*/

type row struct {
	accel, app, action string
	rank, order        int
}

func gsettingsDump() []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "gsettings", "list-recursively").Output()
	return out
}

var quoteRE = regexp.MustCompile(`'([^']*)'`)

type custom struct{ bind, name, cmd string }

func collect(lbl map[string]string) []row {
	keyOrderCache := map[string]map[string]int{}
	orderIdx := func(schema, key string) int {
		m, ok := keyOrderCache[schema]
		if !ok {
			m = loadKeyOrder(schema)
			keyOrderCache[schema] = m
		}
		if v, ok := m[key]; ok {
			return v
		}
		return 1 << 20 // very large ⇒ “last”
	}

	// accelerator → chosen row
	chosen := map[string]row{}

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

		if strings.Contains(schema, ".custom-keybinding") {
			p := schema[strings.Index(schema, ":")+1:]
			c := customMap[p]
			if c == nil {
				c = &custom{}
				customMap[p] = c
			}
			switch key {
			case "binding":
				c.bind = strings.Trim(val, "'")
			case "name":
				c.name = strings.Trim(val, "'")
			case "command":
				c.cmd = strings.Trim(val, "'")
			}
			continue
		}

		if !strings.Contains(schema, "keybinding") {
			continue
		}
		app, rank := classify(schema, key)
		ord := orderIdx(schema, key)

		for _, m := range quoteRE.FindAllStringSubmatch(val, -1) {
			acc, ok := fmtAccel(m[1], lbl)
			if !ok {
				continue
			}
			if rOld, ok := chosen[acc]; !ok ||
				rank < rOld.rank ||
				(rank == rOld.rank && ord < rOld.order) {
				chosen[acc] = row{acc, app, humanise(key), rank, ord}
			}
		}
	}

	/* attach custom shortcuts */
	for _, c := range customMap {
		if acc, ok := fmtAccel(c.bind, lbl); ok {
			if _, ok := chosen[acc]; ok {
				continue
			} // overridden by core/schema
			app := humanise(filepath.Base(c.cmd))
			if app == "" {
				app = "Custom"
			}
			act := humanise(c.name)
			if act == "" {
				act = c.cmd
			}
			chosen[acc] = row{acc, app, act, 3, 0}
		}
	}

	/* immutable core shortcuts override everything */
	for i, s := range coreShortcuts {
		if acc, ok := fmtAccel(s.spec, lbl); ok {
			chosen[acc] = row{acc, "Window Manager", s.action, -1, i}
		}
	}

	out := make([]row, 0, len(chosen))
	for _, r := range chosen {
		out = append(out, r)
	}
	return out
}

/*────────────────── table helpers ──────────────*/

const rowFmt = "%-28s %-28s %-40s\n"

func printRow(a, b, c string) { fmt.Printf(rowFmt, a, b, c) }

/*───────────────────── main ────────────────────*/

func main() {
	lbl := modLabels(layout())
	rows := collect(lbl)

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].rank != rows[j].rank {
			return rows[i].rank < rows[j].rank
		}
		if rows[i].order != rows[j].order {
			return rows[i].order < rows[j].order
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
