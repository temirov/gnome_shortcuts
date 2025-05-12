# gnome-shortcuts

Utility that prints an **authoritative** list of active GNOME keyboard shortcuts
for the current session, showing only the shortcut that will actually fire even
when multiple components claim the same key-combo.

---

## 1 · Build

```bash
go mod init gnome_shortcuts
go get github.com/manifoldco/promptui
go build -o gnome-shortcuts gnome_shortcuts.go
```

(Requires Go ≥ 1.22.)

---

## 2 · Run

### Non-interactive

```bash
KEY_LAYOUT=apple   ./gnome-shortcuts   # Mac / Apple
KEY_LAYOUT=pc      ./gnome-shortcuts   # PC / Windows
KEY_LAYOUT=chrome  ./gnome-shortcuts   # Chromebook
```

### Interactive

```bash
./gnome-shortcuts   # choose layout with ↑/↓ or 1-3
```

`Ctrl-C` aborts.

---

## 3 · Output

```
────────────────────────────────────────────────────────────────────────────────────────────────────
Shortcut                     Application                  Action                                  
────────────────────────────────────────────────────────────────────────────────────────────────────
Command                      Window Manager               Show Activities / Search               
Command + Left               Window Manager               Tile Window Left                       
Command + Space              Input Source                 Switch Input Source                    
…
```

Ordering:
1. Immutable Mutter bindings  
2. Desktop WM bindings  
3. GNOME Shell / Settings-Daemon bindings  
4. Application-specific bindings  
5. User “Custom” bindings  

Within each group the winner of a key conflict is chosen using the order
defined in the relevant `*.gschema.xml` file.

---

## 4 · Logic

1. **Dynamic bindings** collected with `gsettings list-recursively`.  
2. **Schema priority**: position of each key in its `.gschema.xml`
   dictates precedence (parsed at runtime).  
3. **Conflict resolution**: keep the binding with the lowest
   `(category-rank, order-in-schema)` tuple.  
4. **Static Mutter shortcuts** for Activities & tiling injected with
   rank = −1 so they always win.  
5. **Keyboard labels** rendered as Ctrl/Option/Search/Win depending on
   selected layout.

---

## 5 · Extending

* Add layouts in `modLabels`.
* Add verified immutable shortcuts in `coreShortcuts`.
* Everything else is data-driven.

---

## 6 · Known limitations

* Re-parses schemas on each run (~10 ms on SSD).
* XKB or app-internal shortcuts are out of scope.

---

## License

gnome-shortcuts utility is released under the [MIT License](LICENSE).
