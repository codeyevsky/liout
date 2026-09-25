package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/codeyevsky/liout/internal/style"

	"golang.org/x/term"
)

// ---- ANSI shorthands (also used by extra.go / command output) ----
const (
	clr    = "\x1b[H\x1b[2J\x1b[3J"
	hideCu = "\x1b[?25l"
	showCu = "\x1b[?25h"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	rst    = "\x1b[0m"
	red    = "\x1b[38;5;203m"
	green  = "\x1b[38;5;114m"
	yellow = "\x1b[38;5;221m"
	blue   = "\x1b[38;5;39m"
	cyan   = "\x1b[38;5;80m"
	gray   = "\x1b[38;5;245m"
)

// ---- LIOUT block banner (pagga style, matches githubFlex) ----
var banner = []string{
	"░█░░░▀█▀░█▀█░█░█░▀█▀░",
	"░█░░░░█░░█░█░█░█░░█░░",
	"░▀▀▀░▀▀▀░▀▀▀░▀▀▀░░▀░░",
}
var bannerColors = []string{style.Blue, style.Lilac, style.Deep}
var bannerShown bool

const revealPause = 45 * time.Millisecond

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z~]|\x1b.`)

// stdin: one shared reader · a fresh one per call would drop buffered bytes.
var stdin = bufio.NewReader(os.Stdin)

// rawState remembers the terminal mode so restoreTerminal can undo it.
var rawState *term.State

// abortAction is set when Ctrl+C is pressed inside a raw-mode reader (where the
// OS delivers 0x03 as a byte instead of a signal). Action loops check it to bail.
var abortAction bool

// altActive tracks whether we're on the alternate screen buffer.
var altActive bool

func enterAlt() {
	fmt.Print("\x1b[?1049h\x1b[H\x1b[2J\x1b[3J")
	altActive = true
	os.Stdout.Sync()
}

func exitAlt() {
	if altActive {
		fmt.Print("\x1b[?1049l")
		altActive = false
		os.Stdout.Sync()
	}
}

func restoreTerminal() {
	fmt.Print(showCu)
	if rawState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), rawState)
		rawState = nil
	}
	exec.Command("stty", "-F", "/dev/tty", "sane").Run()
	exitAlt()
	os.Stdout.Sync()
}

// ---------- model ----------

type app struct {
	ctx     context.Context
	cfgPath string
	cfg     Config
	leads   string
	tpl     string
	rules   string
}

type menuItem struct {
	group string // non-empty starts a new section
	label string
	desc  string
	key   string
	run   func(*app) error
}

func menu() []menuItem {
	return []menuItem{
		{group: "SETUP", label: "Session", desc: "sign in with your browser", key: "s", run: (*app).actSession},

		{group: "PEOPLE", label: "Search", desc: "find the right people (keywords or URL)", key: "f", run: (*app).actSearch},
		{label: "View list", desc: "browse the current leads file", key: "l", run: (*app).actList},

		{group: "MESSAGE", label: "Messages", desc: "your saved messages · add, edit, choose", key: "m", run: (*app).actMessages},
		{label: "Preview", desc: "see the active message rendered", key: "p", run: (*app).actPreview},

		{group: "SEND", label: "Send", desc: "send the message to everyone on the list", key: "!", run: (*app).actSend},
		{label: "Sent", desc: "who you've already messaged", key: "h", run: (*app).actSent},
	}
}

// ---------- main loop ----------

func runTUI(ctx context.Context, cfgPath string) error {
	if !fileExists(cfgPath) {
		fmt.Print(clr)
		fmt.Printf("%sFirst run · creating configuration files…%s\n\n", dim, rst)
		if err := cmdInit(nil); err != nil {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	a := &app{ctx: ctx, cfgPath: cfgPath, cfg: cfg, rules: "rules.json", leads: "data/leads.csv"}
	if list := loadSearches(); len(list) > 0 {
		a.leads = list[len(list)-1].File // newest search
	}

	enterAlt()
	defer exitAlt()
	items := menu()
	first := true
	for {
		fmt.Print(clr)
		printBanner()
		a.panel()
		sel, ok := selectMenu(items, first)
		first = false
		if !ok {
			restoreTerminal()
			fmt.Print(clr)
			printBanner()
			lead, blockW := layout()
			fmt.Printf("%s%s\n\n", lead+centerIn(4, blockW), style.Tint(style.Dim, "bye."))
			return nil
		}
		fmt.Print(clr)
		lead, blockW := layout()
		fmt.Printf("%s%s%s%s%s\n\n", lead+centerIn(len(items[sel].label)+2, blockW),
			style.Tint(style.Blue, "❯ "), bold, items[sel].label, rst)
		os.Stdout.Sync()
		if err := a.runAction(items[sel]); err != nil {
			fmt.Printf("\n  %s\n", style.Tint(style.Red, "✗ "+err.Error()))
		}
		os.Stdout.Sync()
		waitEnter()
		a.cfg, _ = loadConfig(a.cfgPath)
	}
}

// runAction runs one action under its own cancellable context so Ctrl+C stops
// only that action and never the whole menu.
func (a *app) runAction(it menuItem) error {
	base := a.ctx
	ctx, cancel := context.WithCancel(base)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		select {
		case <-sig:
			fmt.Printf("\n  %s\n", style.Tint(style.Yellow, "interrupted · wrapping up…"))
			os.Stdout.Sync()
			cancel()
		case <-ctx.Done():
		}
	}()
	a.ctx = ctx
	err := it.run(a)
	a.ctx = base
	signal.Stop(sig)
	cancel()
	return err
}

// ---------- banner + panel ----------

func animateBanner(pad string) {
	rows := make([][]rune, len(banner))
	width := 0
	for i, l := range banner {
		rows[i] = []rune(l)
		if len(rows[i]) > width {
			width = len(rows[i])
		}
	}
	fmt.Print(hideCu)
	defer fmt.Print(showCu)
	for range banner {
		fmt.Println()
	}
	for k := 2; ; k += 2 {
		if k > width {
			k = width
		}
		fmt.Printf("\x1b[%dA", len(banner))
		for i, r := range rows {
			end := k
			if end > len(r) {
				end = len(r)
			}
			fmt.Printf("\r\x1b[2K%s\x1b[%sm%s\x1b[0m\n", pad, bannerColors[i%len(bannerColors)], string(r[:end]))
		}
		if k == width {
			return
		}
		time.Sleep(16 * time.Millisecond)
	}
}

func printBanner() {
	bw := len([]rune(banner[0]))
	pad := blockLead(bw)
	fmt.Println()
	if style.On && !bannerShown {
		animateBanner(pad)
		bannerShown = true
	} else {
		for i, l := range banner {
			fmt.Printf("%s%s\n", pad, style.Tint(bannerColors[i%len(bannerColors)], l))
		}
	}
	_, cols := termSize()
	full := "find the right people · send a custom first message"
	if visLen(full) <= cols {
		cprintln(style.Tint(style.Gray, "find the right people") + style.Tint(style.Dim, " · send a custom first message"))
	} else {
		cprintln(style.Tint(style.Gray, "linkedin outreach"))
	}
}

func (a *app) panel() {
	lines := a.statusLines()
	_, cols := termSize()

	// widest content decides the box width, capped to the terminal
	content := 0
	for _, l := range lines {
		if v := visLen(l); v > content {
			content = v
		}
	}
	w := content + 4 // padding inside the box
	if w > cols-2 {
		w = cols - 2
	}
	// too narrow for a box → just center the status lines
	if cols < 44 {
		fmt.Println()
		for _, l := range lines {
			cprintln(l)
		}
		return
	}

	lead := blockLead(w + 2)
	inner := w - 2
	rule := strings.Repeat("─", w)
	fmt.Printf("\n%s%s%s%s\n", lead, style.Tint(style.Deep, "┌"), style.Tint(style.Deep, rule), style.Tint(style.Deep, "┐"))
	for _, l := range lines {
		gap := (inner - visLen(l)) / 2
		if gap < 0 {
			gap = 0
		}
		body := pad(gap) + l
		fmt.Printf("%s%s %s%s %s\n", lead, style.Tint(style.Deep, "│"), body, pad(inner-visLen(body)), style.Tint(style.Deep, "│"))
	}
	fmt.Printf("%s%s%s%s\n", lead, style.Tint(style.Deep, "└"), style.Tint(style.Deep, rule), style.Tint(style.Deep, "┘"))
}

func (a *app) statusLines() []string {
	var who string
	if liAt, js := a.cfg.credentials(); liAt != "" && js != "" {
		name := loadSession().Name
		if name == "" {
			name = "signed in"
		}
		who = style.Tint(style.Green, "●") + " " + name
	} else {
		who = style.Tint(style.Red, "○ not signed in") + style.Tint(style.Dim, " · press s")
	}
	n := countCSV(a.leads)
	sent := sentTotal(a.cfg.Store.Path)
	msg := "none"
	if a.cfg.Send.TemplateFile != "" {
		msg = msgName(a.cfg.Send.TemplateFile)
	}
	return []string{
		who + "    " + style.Tint(style.Bold, fmt.Sprintf("%d people", n)),
		fmt.Sprintf("%s %s    %s %d", style.Tint(style.Dim, "message"), msg, style.Tint(style.Dim, "sent"), sent),
	}
}

// ---------- menu widget ----------

func rawOn() bool {
	st, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return false
	}
	rawState = st
	fmt.Print(hideCu)
	return true
}

func rawOff() {
	fmt.Print(showCu)
	if rawState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), rawState)
		rawState = nil
	}
}

func menuLine(it menuItem, num int) string {
	return fmt.Sprintf("%2d  %-22s %s", num, it.label, style.Tint(style.Dim, it.desc))
}

func renderMenu(items []menuItem, sel int, redraw bool) {
	if redraw {
		fmt.Printf("\x1b[%dA", menuHeight(items))
	}
	lead, blockW := layout()
	// center the menu block on its widest row
	inner := lead + centerIn(menuWidth(items), blockW)
	for i, it := range items {
		if it.group != "" {
			fmt.Printf("\r\x1b[2K\r\n\r\x1b[2K%s%s\r\n", inner, style.Tint(style.Lilac+";1", it.group))
		}
		if i == sel {
			fmt.Printf("\r\x1b[2K%s%s %s\r\n", inner, style.Tint(style.Blue, "❯"), style.Hl(fmt.Sprintf("%2d  %-22s %s", i+1, it.label, it.desc)))
		} else {
			fmt.Printf("\r\x1b[2K%s  %s\r\n", inner, menuLine(it, i+1))
		}
	}
}

// menuWidth is the visible width of the widest menu row, for centering.
func menuWidth(items []menuItem) int {
	max := 0
	for i, it := range items {
		w := visLen(fmt.Sprintf("  %s", menuLine(it, i+1)))
		if w > max {
			max = w
		}
	}
	return max
}

func menuHeight(items []menuItem) int {
	h := 0
	for _, it := range items {
		if it.group != "" {
			h += 2 // blank + heading
		}
		h++
	}
	return h
}

func selectMenu(items []menuItem, reveal bool) (int, bool) {
	if !rawOn() {
		return selectMenuPlain(items)
	}
	defer rawOff()

	sel := 0
	hintStr := "↑↓ / j k move · number or letter jump · enter run · q quit · ? help"
	lead, blockW := layout()
	hintPad := lead + centerIn(len(hintStr), blockW)
	inner := lead + centerIn(menuWidth(items), blockW)
	fmt.Printf("\r\n%s%s\r\n", hintPad, style.Tint(style.Dim, hintStr))
	if reveal {
		for i, it := range items {
			if it.group != "" {
				fmt.Printf("\r\n%s%s\r\n", inner, style.Tint(style.Lilac+";1", it.group))
			}
			fmt.Printf("%s  %s\r\n", inner, menuLine(it, i+1))
			time.Sleep(revealPause / 2)
		}
		renderMenu(items, sel, true)
	} else {
		renderMenu(items, sel, false)
	}

	buf := make([]byte, 8)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return 0, false
		}
		b := buf[:n]
		if b[0] == 0x1b && n == 1 {
			if m, err := os.Stdin.Read(buf[1:3]); err == nil {
				b = buf[:1+m]
			}
		}
		switch {
		case b[0] == 3, b[0] == 'q', (b[0] == 0x1b && n == 1):
			return 0, false
		case b[0] == '\r' || b[0] == '\n':
			return sel, true
		case b[0] == '?':
			rawOff()
			helpScreen(items)
			waitEnter()
			rawOn()
			fmt.Print(clr)
			printBanner()
			lead2, blockW2 := layout()
			h := "↑↓ / j k move · number or letter jump · enter run · q quit · ? help"
			fmt.Printf("\r\n%s%s\r\n", lead2+centerIn(len(h), blockW2), style.Tint(style.Dim, h))
			renderMenu(items, sel, false)
		case b[0] == 'k' || (len(b) >= 3 && b[0] == 0x1b && b[1] == '[' && b[2] == 'A'):
			sel = prevItem(items, sel)
			renderMenu(items, sel, true)
		case b[0] == 'j' || (len(b) >= 3 && b[0] == 0x1b && b[1] == '[' && b[2] == 'B'):
			sel = nextItem(items, sel)
			renderMenu(items, sel, true)
		case b[0] == 'g' && false:
		default:
			if d := int(b[0]); d >= '1' && d <= '9' {
				if idx := int(d-'0') - 1; idx < len(items) {
					sel = idx
				}
				if bytesHaveEnter(b[1:]) {
					return sel, true
				}
				renderMenu(items, sel, true)
				continue
			}
			for i, it := range items {
				if it.key != "" && it.key == strings.ToLower(string(b[0])) {
					sel = i
					break
				}
			}
			if bytesHaveEnter(b[1:]) {
				return sel, true
			}
			renderMenu(items, sel, true)
		}
	}
}

func selectMenuPlain(items []menuItem) (int, bool) {
	for i, it := range items {
		if it.group != "" {
			fmt.Printf("\n  %s\n", it.group)
		}
		fmt.Printf("     %s\n", menuLine(it, i+1))
	}
	fmt.Print("\n  choice (number or q): ")
	line, err := stdin.ReadString('\n')
	if err != nil {
		return 0, false
	}
	line = strings.TrimSpace(line)
	if line == "q" || line == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(items) {
		return n - 1, true
	}
	for i, it := range items {
		if it.key == line {
			return i, true
		}
	}
	return 0, false
}

func bytesHaveEnter(b []byte) bool {
	for _, c := range b {
		if c == '\r' || c == '\n' {
			return true
		}
	}
	return false
}

func prevItem(items []menuItem, sel int) int {
	if sel > 0 {
		return sel - 1
	}
	return len(items) - 1
}
func nextItem(items []menuItem, sel int) int {
	if sel < len(items)-1 {
		return sel + 1
	}
	return 0
}
func indexOf(items []menuItem, it menuItem) int {
	for i := range items {
		if items[i].label == it.label {
			return i
		}
	}
	return 0
}

// pick is a horizontal option selector (arrow keys), like githubFlex.
func pick(label string, options []string, defIdx int) string {
	if defIdx < 0 || defIdx >= len(options) {
		defIdx = 0
	}
	if !rawOn() {
		fmt.Print(center(fmt.Sprintf("%s [%s]: ", label, options[defIdx])))
		line, _ := stdin.ReadString('\n')
		line = strings.TrimSpace(line)
		for _, o := range options {
			if strings.EqualFold(line, o) {
				return o
			}
		}
		return options[defIdx]
	}
	defer rawOff()

	sel := defIdx
	// stable centering width computed from the plain layout
	plainWidth := visLen(label) + 1
	for _, o := range options {
		plainWidth += visLen(o) + 3
	}
	render := func() {
		fmt.Printf("\r\x1b[2K%s%s:", blockLead(plainWidth), label)
		for i, o := range options {
			if i == sel {
				fmt.Printf(" %s", style.Hl(o))
			} else {
				fmt.Printf("  %s ", o)
			}
		}
	}
	render()
	buf := make([]byte, 8)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			fmt.Print("\r\n")
			return options[sel]
		}
		b := buf[:n]
		if b[0] == 0x1b && n == 1 {
			if m, err := os.Stdin.Read(buf[1:3]); err == nil {
				b = buf[:1+m]
			}
		}
		switch {
		case b[0] == 3: // Ctrl+C in raw mode
			abortAction = true
			fmt.Print("\r\n")
			return options[sel]
		case b[0] == '\r' || b[0] == '\n':
			fmt.Printf("\r\x1b[2K%s%s: %s\r\n", blockLead(plainWidth), label, style.Tint(style.Blue, options[sel]))
			return options[sel]
		case b[0] == 'h' || (len(b) >= 3 && b[0] == 0x1b && b[1] == '[' && b[2] == 'D'):
			if sel > 0 {
				sel--
			}
		case b[0] == 'l' || (len(b) >= 3 && b[0] == 0x1b && b[1] == '[' && b[2] == 'C'):
			if sel < len(options)-1 {
				sel++
			}
		}
		render()
	}
}

func helpScreen(items []menuItem) {
	fmt.Print(clr)
	fmt.Printf("  %s%sliout · help%s\n\n", bold, style.Tint(style.Blue, ""), rst)
	fmt.Printf("  %s\n", style.Tint(style.Lilac+";1", "KEYS"))
	fmt.Printf("    ↑ ↓ / j k   move        enter    run selected\n")
	fmt.Printf("    1-9         jump        letter   jump by shortcut\n")
	fmt.Printf("    ?           this help   q / esc  quit\n\n")
	fmt.Printf("  %s\n", style.Tint(style.Lilac+";1", "TYPICAL RUN"))
	fmt.Printf("    Session → Search → Messages → Preview → Send\n\n")
	fmt.Printf("  %s\n", style.Tint(style.Lilac+";1", "SHORTCUTS"))
	col := 0
	for _, it := range items {
		if it.key == "" {
			continue
		}
		fmt.Printf("    %s %-18s", style.Tint(style.Blue, it.key), it.label)
		if col++; col%2 == 0 {
			fmt.Println()
		}
	}
	fmt.Println()
}

// ---------- input helpers ----------

func waitEnter() {
	fmt.Printf("\n  %s ", style.Tint(style.Dim, "[enter] back to menu"))
	os.Stdout.Sync()
	if !rawOn() {
		stdin.ReadString('\n')
		return
	}
	defer rawOff()
	buf := make([]byte, 8)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		for i := 0; i < n; i++ {
			if buf[i] == '\r' || buf[i] == '\n' || buf[i] == 3 {
				fmt.Print("\r\n")
				return
			}
		}
	}
}

func ask(prompt, def string) string {
	if def != "" {
		fmt.Printf("  %s %s %s ", prompt, style.Tint(style.Dim, "["+def+"]"), style.Tint(style.Blue, "❯"))
	} else {
		fmt.Printf("  %s %s ", prompt, style.Tint(style.Blue, "❯"))
	}
	os.Stdout.Sync()
	line, _ := stdin.ReadString('\n')
	v := strings.TrimSpace(line)
	if v == "" {
		return def
	}
	return v
}

func askYN(prompt string, def bool) bool {
	opts := []string{"no", "yes"}
	d := 0
	if def {
		d = 1
	}
	return pick(prompt, opts, d) == "yes"
}

func confirm(prompt string) bool {
	return ask(prompt+` · type "SEND"`, "") == "SEND"
}

// uiWidth is the target content column width; the whole UI is centered to it
// and it shrinks to fit narrow terminals so nothing wraps.
func uiWidth() int {
	_, cols := termSize()
	w := 72
	if w > cols {
		w = cols
	}
	if w < 20 {
		w = cols
	}
	return w
}

// center left-pads a single visible line so it sits in the middle of the screen.
func center(s string) string {
	_, cols := termSize()
	pad := (cols - visLen(s)) / 2
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + s
}

// centerBlock left-pads by the same amount for every line so a multi-line block
// (like the menu) stays aligned, centered on its widest row.
func blockLead(widest int) string {
	_, cols := termSize()
	pad := (cols - widest) / 2
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad)
}

// contentWidth is the left-aligned centered column width for action output.
func contentWidth() int {
	_, cols := termSize()
	w := 76
	if w > cols-2 {
		w = cols - 2
	}
	if w < 20 {
		w = cols
	}
	return w
}

// contentLead centers that column on screen; lines inside are LEFT-aligned.
func contentLead() string { return blockLead(contentWidth()) }

// cline / cprintln: one line, left-aligned inside the centered content column.
func cline(s string) string { return contentLead() + s }
func cprintln(s string)     { fmt.Println(contentLead() + s) }

// layout / centerIn: compatibility shims over the responsive model.
// blockW = the centered column width (shrinks on narrow terminals);
// lead centers that column; centerIn centers content inside it.
func layout() (lead string, blockW int) {
	blockW = uiWidth()
	return blockLead(blockW), blockW
}

func centerIn(width, blockW int) string {
	n := (blockW - width) / 2
	if n < 0 {
		n = 0
	}
	return strings.Repeat(" ", n)
}

func termSize() (rows, cols int) {
	rows, cols = 24, 80
	if c, r, err := term.GetSize(int(os.Stdout.Fd())); err == nil && c > 0 {
		return r, c
	}
	return
}
