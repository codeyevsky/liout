package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/codeyevsky/liout/internal/model"
	"github.com/codeyevsky/liout/internal/store"
	"github.com/codeyevsky/liout/internal/style"
)

// SavedSearch is one search kept in history: its keyword and the people it found.
type SavedSearch struct {
	ID          string `json:"id"`
	Keyword     string `json:"keyword"`
	File        string `json:"file"`
	At          string `json:"at"`
	Connections bool   `json:"connections"`
}

const searchesDir = "data/searches"
const searchesIndex = "data/searches.json"

func loadSearches() []SavedSearch {
	var out []SavedSearch
	b, err := os.ReadFile(searchesIndex)
	if err == nil {
		json.Unmarshal(b, &out)
	}
	// drop entries whose file vanished
	kept := out[:0]
	for _, s := range out {
		if fileExists(s.File) {
			kept = append(kept, s)
		}
	}
	return kept
}

func saveSearches(list []SavedSearch) error {
	os.MkdirAll("data", 0o755)
	b, _ := json.MarshalIndent(list, "", "  ")
	return os.WriteFile(searchesIndex, append(b, '\n'), 0o644)
}

// addSearch writes a new saved search and returns it.
func addSearch(keyword string, people []model.Person, stamp time.Time, connections bool) (SavedSearch, error) {
	os.MkdirAll(searchesDir, 0o755)
	id := fmt.Sprintf("%d", stamp.UnixNano())
	file := filepath.Join(searchesDir, id+"-"+sanitizeName(keyword)+".csv")
	if err := store.WriteCSV(file, people); err != nil {
		return SavedSearch{}, err
	}
	s := SavedSearch{ID: id, Keyword: keyword, File: file, At: stamp.Format("2006-01-02 15:04"), Connections: connections}
	list := append(loadSearches(), s)
	return s, saveSearches(list)
}

func deleteSearch(id string) {
	var kept []SavedSearch
	for _, s := range loadSearches() {
		if s.ID == id {
			os.Remove(s.File)
			continue
		}
		kept = append(kept, s)
	}
	saveSearches(kept)
}

// pickSearch shows the saved searches and lets the user choose one (Enter),
// delete one (d), or go back (q). Returns the chosen search and true.
func pickSearch(title string) (SavedSearch, bool) {
	for {
		list := loadSearches()
		if len(list) == 0 {
			cprintln(style.Tint(style.Dim, "no searches yet · run Search first"))
			return SavedSearch{}, false
		}
		// newest first
		rev := make([]SavedSearch, len(list))
		for i, s := range list {
			rev[len(list)-1-i] = s
		}
		var lines []string
		for _, s := range rev {
			n := countCSV(s.File)
			lines = append(lines, fmt.Sprintf("%s%s",
				style.Tint(style.Bold, s.Keyword),
				style.Tint(style.Dim, fmt.Sprintf("   %d people · %s", n, s.At))))
		}
		idx, action := listSelect(title, lines)
		switch action {
		case "back":
			return SavedSearch{}, false
		case "select":
			return rev[idx], true
		case "del":
			deleteSearch(rev[idx].ID)
		}
	}
}

// browsePeople shows one search's people with a cursor; d removes a person.
func browsePeople(s SavedSearch) {
	people, err := store.ReadCSV(s.File)
	if err != nil {
		cprintln(style.Tint(style.Red, err.Error()))
		return
	}
	if !rawOn() {
		return
	}
	defer rawOff()
	cursor, offset, dirty := 0, 0, false
	for {
		if len(people) == 0 {
			fmt.Print(clr)
			cprintln(center(style.Tint(style.Bold, s.Keyword)))
			fmt.Println()
			cprintln(style.Tint(style.Dim, "list is empty"))
			cprintln(style.Tint(style.Dim, "q back"))
			os.Stdout.Sync()
			listKeyRaw()
			if dirty && confirmSave() {
				store.WriteCSV(s.File, people)
			}
			return
		}
		if cursor < 0 {
			cursor = 0
		}
		if cursor >= len(people) {
			cursor = len(people) - 1
		}
		rows, _ := termSize()
		per := (rows - 5) / 2
		if per < 1 {
			per = 1
		}
		if cursor < offset {
			offset = cursor
		}
		if cursor >= offset+per {
			offset = cursor - per + 1
		}
		if offset < 0 {
			offset = 0
		}
		fmt.Print(clr)
		head := style.Tint(style.Bold, s.Keyword) + style.Tint(style.Dim, fmt.Sprintf("   %d people", len(people)))
		fmt.Print(center(head) + "\r\n\r\n")
		end := offset + per
		if end > len(people) {
			end = len(people)
		}
		for i := offset; i < end; i++ {
			p := people[i]
			name := fmt.Sprintf("%3d  %s", i+1, p.FullName)
			if i == cursor {
				fmt.Print("\r" + contentLead() + style.Hl(name) + "\r\n")
			} else {
				fmt.Print("\r" + contentLead() + style.Tint(style.Bold, name) + "\r\n")
			}
			sub := p.Headline
			if sub == "" {
				sub = p.Title
			}
			fmt.Print("\r" + contentLead() + "     " + style.Tint(style.Dim, cut(sub, contentWidth()-7)) + "\r\n")
		}
		fmt.Print("\r\n" + center(style.Tint(style.Dim, "i k ↑↓ move · j l ←→ page · d remove · q back")))
		os.Stdout.Sync()

		switch listKeyRaw() {
		case "up":
			cursor--
		case "down":
			cursor++
		case "pageup":
			cursor -= per
		case "pagedown":
			cursor += per
		case "del":
			people = append(people[:cursor], people[cursor+1:]...)
			dirty = true
		case "back":
			if !dirty {
				return
			}
			if confirmSave() {
				store.WriteCSV(s.File, people)
			}
			return
		}
	}
}

// listSelect is a cursor list used for choosing a saved search.
func listSelect(title string, lines []string) (int, string) {
	if len(lines) == 0 {
		return 0, "back"
	}
	if !rawOn() {
		return 0, "back"
	}
	defer rawOff()
	cursor, offset := 0, 0
	for {
		rows, _ := termSize()
		per := rows - 5
		if per < 1 {
			per = 1
		}
		if cursor < 0 {
			cursor = 0
		}
		if cursor >= len(lines) {
			cursor = len(lines) - 1
		}
		if cursor < offset {
			offset = cursor
		}
		if cursor >= offset+per {
			offset = cursor - per + 1
		}
		fmt.Print(clr)
		if title != "" {
			fmt.Print(center(title) + "\r\n\r\n")
		}
		end := offset + per
		if end > len(lines) {
			end = len(lines)
		}
		for i := offset; i < end; i++ {
			if i == cursor {
				fmt.Print("\r" + contentLead() + style.Tint(style.Blue, "❯ ") + style.Hl(stripStyle(lines[i])) + "\r\n")
			} else {
				fmt.Print("\r" + contentLead() + "  " + lines[i] + "\r\n")
			}
		}
		fmt.Print("\r\n" + center(style.Tint(style.Dim, "i k ↑↓ move · enter open · d delete · q back")))
		os.Stdout.Sync()

		switch listKeyRaw() {
		case "up":
			cursor--
		case "down":
			cursor++
		case "pageup":
			cursor -= per
		case "pagedown":
			cursor += per
		case "select":
			return cursor, "select"
		case "del":
			return cursor, "del"
		case "back":
			return 0, "back"
		}
	}
}

// confirmSave asks to keep changes before leaving; returns true to save.
func confirmSave() bool {
	fmt.Print(clr)
	fmt.Print(center(style.Tint(style.Yellow, "You removed people from this list.")) + "\r\n\r\n")
	fmt.Print(center(style.Tint(style.Bold, "Save changes?") + style.Tint(style.Dim, "    y = save    n = discard")))
	os.Stdout.Sync()
	buf := make([]byte, 4)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return false
		}
		switch buf[0] {
		case 'y', 'Y', 13, 10:
			return true
		case 'n', 'N', 3, 27, 'q':
			return false
		}
	}
}

// scrollLines shows read-only text in a scrollable viewport.
func scrollLines(title string, lines []string) {
	if !rawOn() {
		if title != "" {
			fmt.Println(center(title))
			fmt.Println()
		}
		for _, l := range lines {
			fmt.Println(contentLead() + l)
		}
		return
	}
	defer rawOff()
	offset := 0
	for {
		rows, _ := termSize()
		h := rows - 5
		if h < 3 {
			h = 3
		}
		max := len(lines) - h
		if max < 0 {
			max = 0
		}
		if offset > max {
			offset = max
		}
		if offset < 0 {
			offset = 0
		}
		fmt.Print(clr)
		if title != "" {
			fmt.Print(center(title) + "\r\n\r\n")
		}
		end := offset + h
		if end > len(lines) {
			end = len(lines)
		}
		for i := offset; i < end; i++ {
			fmt.Print("\r" + contentLead() + lines[i] + "\r\n")
		}
		fmt.Print("\r\n" + center(style.Tint(style.Dim, "i k ↑↓ scroll · j l ←→ page · q back")))
		os.Stdout.Sync()
		switch listKeyRaw() {
		case "up":
			offset--
		case "down":
			offset++
		case "pageup":
			offset -= h
		case "pagedown":
			offset += h
		case "back", "select":
			return
		}
	}
}

// stripStyle removes ANSI so a highlighted line reads cleanly on the blue bar.
func stripStyle(s string) string { return ansiRe.ReplaceAllString(s, "") }

// listKeyRaw reads one navigation key for the list views.
// scroll: ↑↓ or i/k · page: ←→ or j/l · d/x remove · enter open · q/esc back.
func listKeyRaw() string {
	buf := make([]byte, 8)
	n, err := os.Stdin.Read(buf)
	if err != nil || n == 0 {
		return "back"
	}
	b := buf[:n]
	if b[0] == 0x1b && n == 1 {
		if m, err := os.Stdin.Read(buf[1:3]); err == nil {
			b = buf[:1+m]
		}
	}
	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' {
		switch b[2] {
		case 'A':
			return "up"
		case 'B':
			return "down"
		case 'C':
			return "pagedown"
		case 'D':
			return "pageup"
		}
		return ""
	}
	switch b[0] {
	case 13, 10:
		return "select"
	case 3:
		abortAction = true
		return "back"
	case 27:
		return "back"
	case 'i', 'I':
		return "up"
	case 'k', 'K':
		return "down"
	case 'j', 'J':
		return "pageup"
	case 'l', 'L':
		return "pagedown"
	case 'd', 'D', 'x', 'X':
		return "del"
	case 'q', 'Q':
		return "back"
	}
	return ""
}
