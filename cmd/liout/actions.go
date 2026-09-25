package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/codeyevsky/liout/internal/browser"
	"github.com/codeyevsky/liout/internal/linkedin"
	"github.com/codeyevsky/liout/internal/match"
	"github.com/codeyevsky/liout/internal/store"
	"github.com/codeyevsky/liout/internal/style"
	"github.com/codeyevsky/liout/internal/tmpl"
)

// ---------- session ----------

func (a *app) actSession() error {
	sess := loadSession()
	liAt, _ := a.cfg.credentials()
	if liAt == "" {
		cprintln(style.Tint(style.Yellow, "Not signed in yet."))
		fmt.Println()
		return a.actLogin()
	}
	if sess.Name != "" {
		cprintln(style.Tint(style.Green, "● ") + style.Tint(style.Bold, sess.Name))
	} else {
		cprintln(style.Tint(style.Green, "● signed in"))
	}
	if sess.PublicID != "" {
		cprintln(style.Tint(style.Dim, "linkedin.com/in/"+sess.PublicID))
	}
	fmt.Println()
	cprintln(style.Tint(style.Dim, "liout signs in through your browser and stays logged in there,"))
	cprintln(style.Tint(style.Dim, "so Search and Send just work. Sign in again only if they stop."))
	fmt.Println()
	switch pick("action", []string{"stay", "sign in again", "sign out"}, 0) {
	case "sign in again":
		return a.actLogin()
	case "sign out":
		if err := clearSession(); err != nil {
			return err
		}
		cprintln(style.Tint(style.Green, "signed out"))
	}
	return nil
}

func (a *app) actLogin() error {
	if pick("how to sign in", []string{"open browser", "paste cookies"}, 0) == "paste cookies" {
		return a.loginPaste()
	}
	return a.loginBrowser()
}

func (a *app) loginBrowser() error {
	avail := browser.Available()
	br := avail[0]
	if len(avail) > 1 {
		br = pick("browser", avail, 0)
	}
	fmt.Println()
	for _, l := range []string{
		"A browser window will open at LinkedIn.",
		"If you're already logged in there, liout grabs your session instantly.",
		"Otherwise just log in normally · liout reads the cookies for you.",
	} {
		cprintln(style.Tint(style.Dim, l))
	}
	if browser.NeedsDownload(br) {
		cprintln(style.Tint(style.Dim, "first run only: liout downloads its own "+br+" (~100 MB) · one minute or so."))
	}
	fmt.Println()
	cprintln(style.Tint(style.Blue, "launching ") + br + "…")
	fmt.Println()

	sess, err := browser.Open(browser.Opts{Browser: br})
	if err != nil {
		return fmt.Errorf("%w\n  (first run downloads the browser once · this can take a minute)", err)
	}
	defer sess.Close()

	spin := newSpinner("waiting for LinkedIn login")
	creds, err := sess.Grab(a.ctx, 5*time.Minute, spin.setLabel)
	spin.stop()
	if err != nil {
		return err
	}
	if !creds.OK() {
		return fmt.Errorf("could not read li_at / JSESSIONID from the browser")
	}
	name, pub := creds.Name, creds.PublicID
	// best-effort: enrich the display name from the API, but never fail on it ·
	// the browser login is the source of truth.
	if cli, err := linkedin.New(creds.LiAt, creds.JSession, a.cfg.Auth.UserAgent, a.cfg.Auth.Lang); err == nil {
		if _, n, pb, err := cli.MeInfo(a.ctx); err == nil {
			if n != "" {
				name = n
			}
			if pb != "" {
				pub = pb
			}
		}
	}
	if name == "" {
		name = "your account"
	}
	if err := saveSession(Session{LiAt: creds.LiAt, JSession: creds.JSession, Name: name, PublicID: pub}); err != nil {
		return err
	}
	fmt.Println()
	cprintln(style.Tint(style.Green, "✓ signed in as ") + style.Tint(style.Bold, name))
	return nil
}

func (a *app) loginPaste() error {
	fmt.Println()
	cprintln(style.Tint(style.Dim, "F12 → Application/Storage → Cookies → www.linkedin.com"))
	cprintln(style.Tint(style.Dim, "copy the li_at value, then JSESSIONID (starts with ajax:)"))
	fmt.Println()
	liAt := strings.Trim(ask("li_at", ""), `"' `)
	if liAt == "" {
		return fmt.Errorf("li_at is empty")
	}
	js := strings.Trim(ask("JSESSIONID", ""), `"' `)
	if js == "" {
		return fmt.Errorf("JSESSIONID is empty")
	}
	cli, err := linkedin.New(liAt, js, a.cfg.Auth.UserAgent, a.cfg.Auth.Lang)
	if err != nil {
		return err
	}
	id, name, pub, err := cli.MeInfo(a.ctx)
	if err != nil {
		return err
	}
	cprintln(style.Tint(style.Green, "ok") + " · " + name)
	return saveSession(Session{LiAt: liAt, JSession: js, Name: name, PublicID: pub, ProfileID: id})
}

// browserChoice returns the browser to drive.
func browserChoice(a *app) string { return browser.DefaultBrowser() }

// refreshCookies re-reads cookies from the live browser and updates session.json.
func (a *app) refreshCookies(sess *browser.Session) {
	c := sess.Cookies()
	if !c.OK() {
		return
	}
	old := loadSession()
	if c.LiAt == old.LiAt && c.JSession == old.JSession {
		return
	}
	old.LiAt, old.JSession = c.LiAt, c.JSession
	saveSession(old)
}

func atoiDefault(s string, def int) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return def
	}
	return n
}

// askCount reads a positive number, or blank for "all"; re-asks on junk.
func askCount(prompt string, all int) int {
	for {
		v := ask(prompt, "")
		if v == "" {
			return all
		}
		n := 0
		ok := len(v) > 0
		for _, r := range v {
			if r < '0' || r > '9' {
				ok = false
				break
			}
			n = n*10 + int(r-'0')
		}
		if ok && n > 0 {
			return n
		}
		cprintln(style.Tint(style.Dim, "type a number, or leave blank for all"))
	}
}

// askRequired prompts without a default · Enter on an empty line re-asks.
func askRequired(prompt string) string {
	for {
		fmt.Print(contentLead() + prompt + " " + style.Tint(style.Blue, "❯") + " ")
		os.Stdout.Sync()
		line, _ := stdin.ReadString('\n')
		if v := strings.TrimSpace(line); v != "" {
			return v
		}
	}
}

const queryFile = "data/last_query.txt"

func (a *app) saveQuery(q string) {
	os.MkdirAll("data", 0o755)
	os.WriteFile(queryFile, []byte(q), 0o644)
}

func loadQuery() string {
	b, _ := os.ReadFile(queryFile)
	return strings.TrimSpace(string(b))
}

func maskStr(s string) string {
	r := []rune(s)
	if len(r) <= 12 {
		return strings.Repeat("•", len(r))
	}
	return string(r[:6]) + strings.Repeat("•", 10) + string(r[len(r)-4:])
}

// ---------- find the right people ----------

func (a *app) actSearch() error {
	cprintln(style.Tint(style.Dim, "Add searches one by one. Type a keyword; leave blank when done."))
	fmt.Println()
	var terms []string
	for {
		label := "keyword"
		if len(terms) > 0 {
			label = "another keyword (blank = done)"
		}
		k := ask(label, "")
		if k == "" {
			break
		}
		terms = append(terms, k)
	}
	if len(terms) == 0 {
		return fmt.Errorf("type at least one keyword")
	}
	n := atoiDefault(askRequired("how many people each (e.g. 100)"), 0)
	if n <= 0 {
		return fmt.Errorf("type a number, e.g. 100")
	}
	connections := pick("search in", []string{"everyone", "my connections"}, 0) == "my connections"
	if abortAction {
		return nil
	}
	fmt.Println()

	sess, err := browser.Open(browser.Opts{Browser: browserChoice(a), Headless: false})
	if err != nil {
		return err
	}
	defer sess.Close()
	ex, err := match.NewExtractor(a.cfg.Extract.HeadlinePattern, a.cfg.Extract.CompanyStrip)
	if err != nil {
		return err
	}

	saved := 0
	for _, term := range terms {
		spin := newSpinner("searching " + term + "…")
		found, err := sess.SearchPeople(a.ctx, term, "", n, connections, spin.setLabel)
		spin.stop()
		if err != nil && len(found) == 0 {
			cprintln(style.Tint(style.Yellow, term+": "+err.Error()))
			continue
		}
		for i := range found {
			ex.Apply(&found[i])
		}
		label := term
		if connections {
			label = term + " (connections)"
		}
		entry, err := addSearch(label, found, time.Now(), connections)
		if err != nil {
			return err
		}
		a.leads = entry.File
		saved++
		cprintln(style.Tint(style.Green, fmt.Sprintf("✓ %s · %d people", term, len(found))))
	}
	a.refreshCookies(sess)
	if saved == 0 {
		return fmt.Errorf("no people found · try different keywords")
	}
	return nil
}

func (a *app) actList() error {
	s, ok := pickSearch(style.Tint(style.Bold, "your searches"))
	if !ok {
		return nil
	}
	a.leads = s.File
	browsePeople(s)
	return nil
}

// ---------- messages (local library) ----------

func (a *app) messagesDir() string {
	d := a.cfg.Send.MessagesDir
	if d == "" {
		d = "messages"
	}
	return d
}

func (a *app) listMessages() []string {
	files, _ := filepath.Glob(filepath.Join(a.messagesDir(), "*.tmpl"))
	sort.Strings(files)
	return files
}

func (a *app) actMessages() error {
	for {
		files := a.listMessages()
		active := a.cfg.Send.TemplateFile
		fmt.Print(clr)
		fmt.Print(center(style.Tint(style.Blue, "❯ ")+style.Tint(style.Bold, "Messages")) + "\r\n\r\n")
		if len(files) == 0 {
			cprintln(style.Tint(style.Dim, "none yet · create one"))
		}
		for i, f := range files {
			mark := "  "
			if f == active {
				mark = style.Tint(style.Green, "● ")
			}
			cprintln(fmt.Sprintf("%s%d  %s", mark, i+1, msgName(f)))
		}
		fmt.Println()
		choice := pick("action", []string{"choose active", "edit", "new", "preview", "delete", "back"}, 0)
		if abortAction {
			return nil
		}
		switch choice {
		case "back":
			return nil
		case "choose active":
			if len(files) == 0 {
				continue
			}
			sel := pick("active message", names(files), indexOfStr(files, active))
			if abortAction {
				return nil
			}
			a.setActive(pathFor(files, sel))
			cprintln(style.Tint(style.Green, "active → "+sel))
		case "edit":
			if len(files) == 0 {
				continue
			}
			sel := pick("edit which", names(files), indexOfStr(files, active))
			if abortAction {
				return nil
			}
			if err := edit(pathFor(files, sel)); err != nil {
				return err
			}
		case "new":
			name := ask("new message name (e.g. founders)", "")
			if name == "" {
				continue
			}
			path := filepath.Join(a.messagesDir(), sanitizeName(name)+".tmpl")
			if !fileExists(path) {
				os.MkdirAll(a.messagesDir(), 0o755)
				os.WriteFile(path, []byte(sampleTemplate), 0o644)
			}
			a.setActive(path)
			if err := edit(path); err != nil {
				return err
			}
			cprintln(style.Tint(style.Green, "created and set active → "+msgName(path)))
		case "preview":
			if len(files) == 0 {
				continue
			}
			sel := pick("preview which", names(files), indexOfStr(files, active))
			if abortAction {
				return nil
			}
			ss, ok := pickSearch(style.Tint(style.Bold, "preview against which search"))
			if !ok {
				continue
			}
			return a.previewPaged(pathFor(files, sel), ss.File, ss.Keyword)
		case "delete":
			if len(files) == 0 {
				continue
			}
			sel := pick("delete which", names(files), 0)
			if abortAction {
				return nil
			}
			if askYN("delete "+sel+"?", false) {
				os.Remove(pathFor(files, sel))
				cprintln(style.Tint(style.Yellow, "deleted "+sel))
			}
		}
	}
}

// setActive persists the chosen message into config.json.
func (a *app) setActive(path string) {
	a.cfg.Send.TemplateFile = path
	b, err := os.ReadFile(a.cfgPath)
	if err != nil {
		saveConfig(a.cfgPath, a.cfg)
		return
	}
	// rewrite just template_file to avoid disturbing formatting the user may have
	_ = b
	saveConfig(a.cfgPath, a.cfg)
}

func (a *app) actPreview() error {
	if a.cfg.Send.TemplateFile == "" || !fileExists(a.cfg.Send.TemplateFile) {
		return fmt.Errorf("no message yet · create one in Messages (new)")
	}
	sel, ok := pickSearch(style.Tint(style.Bold, "preview which search"))
	if !ok {
		return nil
	}
	return a.previewPaged(a.cfg.Send.TemplateFile, sel.File, sel.Keyword)
}

// previewPaged renders the message for every person and shows them in a fixed,
// scrollable pager (nothing is sent).
func (a *app) previewPaged(tplPath, leadsFile, title string) error {
	b, err := os.ReadFile(tplPath)
	if err != nil {
		return err
	}
	r, err := tmpl.New(tplPath, string(b), a.cfg.Template)
	if err != nil {
		return err
	}
	ex, err := match.NewExtractor(a.cfg.Extract.HeadlinePattern, a.cfg.Extract.CompanyStrip)
	if err != nil {
		return err
	}
	people, err := readCSVQuiet(leadsFile)
	if err != nil {
		return err
	}
	if len(people) == 0 {
		return fmt.Errorf("that search has no people")
	}
	var lines []string
	for i := range people {
		ex.Apply(&people[i])
		lines = append(lines, style.Tint(style.Blue, fmt.Sprintf("%d  ", i+1))+style.Tint(style.Bold, people[i].FullName))
		msg, rerr := r.Render(people[i])
		if rerr != nil {
			lines = append(lines, style.Tint(style.Dim, "  (skipped: "+rerr.Error()+")"))
		} else {
			for _, ml := range strings.Split(msg, "\n") {
				for _, w := range wrapText(ml, contentWidth()-2) {
					lines = append(lines, "  "+w)
				}
			}
		}
		lines = append(lines, "")
	}
	header := fmt.Sprintf("%s   %d people   %s", title, len(people), msgName(tplPath))
	scrollLines(header, lines)
	return nil
}

// ---------- send ----------

func (a *app) actSend() error {
	sel, ok := pickSearch(style.Tint(style.Bold, "send to which search"))
	if !ok {
		return nil
	}
	a.leads = sel.File
	fmt.Print(clr)
	people, _ := readCSVQuiet(a.leads)
	if len(people) == 0 {
		return fmt.Errorf("that search has no people")
	}
	if !fileExists(a.cfg.Send.TemplateFile) {
		return fmt.Errorf("no active message · set one in Messages")
	}

	// build the renderer + extractor
	tb, err := os.ReadFile(a.cfg.Send.TemplateFile)
	if err != nil {
		return err
	}
	r, err := tmpl.New(a.cfg.Send.TemplateFile, string(tb), a.cfg.Template)
	if err != nil {
		return err
	}
	ex, err := match.NewExtractor(a.cfg.Extract.HeadlinePattern, a.cfg.Extract.CompanyStrip)
	if err != nil {
		return err
	}
	bl, _ := match.LoadBlacklist(a.cfg.Blacklist)
	st, err := store.Open(a.cfg.Store.Path)
	if err != nil {
		return err
	}
	defer st.Close()

	cprintln(fmt.Sprintf("%d people   message: %s", len(people), msgName(a.cfg.Send.TemplateFile)))
	fmt.Println()
	limit := askCount("send to how many (blank = all)", len(people))
	preferInvite := false
	if !sel.Connections { // connections are already 1st-degree -> always message
		preferInvite = pick("send as", []string{"message (fallback: connect)", "connection request"}, 0) == "connection request"
		if abortAction {
			return nil
		}
	}
	skipExisting := askYN("skip people you've already talked to?", true)
	fmt.Println()
	cprintln(style.Tint(style.Yellow, "Sends real messages from your account, in a visible browser."))
	if !confirm("confirm") {
		cprintln("cancelled.")
		return nil
	}
	fmt.Println()

	sess, err := browser.Open(browser.Opts{Browser: browserChoice(a), Headless: false})
	if err != nil {
		return err
	}
	defer sess.Close()
	a.refreshCookies(sess)

	rnd := time.Now().UnixNano()
	pauseSec := func() int {
		lo, hi := a.cfg.Send.DelayMin, a.cfg.Send.DelayMax
		if hi <= lo {
			return lo
		}
		rnd = rnd*1103515245 + 12345
		d := int((rnd >> 16) % int64(hi-lo))
		if d < 0 {
			d = -d
		}
		return lo + d
	}

	sent, skipped, failed, attempts := 0, 0, 0, 0
	for i := range people {
		if attempts >= limit {
			break
		}
		p := &people[i]
		ex.Apply(p)
		if p.ProfileID == "" {
			skipped++
			continue
		}
		if bl != nil {
			if ok, why := bl.Blocked(*p); ok {
				skipped++
				cprintln(style.Tint(style.Dim, "⛔ "+p.FullName+" · "+why))
				continue
			}
		}
		if st.Seen(p.Key()) {
			skipped++
			cprintln(style.Tint(style.Dim, "· "+p.FullName+" · already messaged"))
			continue
		}
		msg, err := r.Render(*p)
		if err != nil {
			skipped++
			cprintln(style.Tint(style.Dim, "· "+p.FullName+" · "+err.Error()))
			continue
		}

		attempts++
		cprintln(style.Tint(style.Dim, "→ "+p.FullName+" …"))
		res, err := sess.SendTo(a.ctx, p.ProfileID, msg, preferInvite, skipExisting)
		fmt.Print("\x1b[1A\r\x1b[2K") // erase the "…" line, replace with result
		rec := store.Record{ProfileID: p.Key(), Name: p.FullName, Company: p.Company, Message: msg, Step: 1}
		if err != nil {
			failed++
			rec.Status, rec.Error = store.StatusFailed, err.Error()
			cprintln(style.Tint(style.Red, "✗ ") + cut(p.FullName+"  "+err.Error(), contentWidth()-3))
			st.Append(rec)
		} else if res.Action == "already" {
			skipped++
			cprintln(style.Tint(style.Dim, "· "+p.FullName+"  already in a conversation"))
			// remember so we don't re-open it next run
			rec.Status = store.StatusSkipped
			st.Append(rec)
		} else {
			sent++
			rec.Status = store.StatusSent
			if res.Action == "invite" {
				rec.Status = store.StatusInvited
			}
			cprintln(style.Tint(style.Green, "✓ ") + cut(p.FullName+"  ("+res.Action+")", contentWidth()-3))
			st.Append(rec)
		}

		if abortAction {
			cprintln("stopped.")
			return nil
		}
		select {
		case <-a.ctx.Done():
			cprintln("stopped.")
			return nil
		case <-time.After(time.Duration(pauseSec()) * time.Second):
		}
	}
	fmt.Println()
	cprintln(style.Tint(style.Green, fmt.Sprintf("done · sent %d · skipped %d · failed %d", sent, skipped, failed)))
	return nil
}

// actSent shows everyone already messaged (sent history), newest first.
func (a *app) actSent() error {
	type rec struct {
		ID, Name string
		At       time.Time
	}
	load := func() []rec {
		f, err := os.Open(a.cfg.Store.Path)
		if err != nil {
			return nil
		}
		defer f.Close()
		seen := map[string]bool{}
		var out []rec
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			var r struct {
				ID     string    `json:"profile_id"`
				Name   string    `json:"name"`
				Status string    `json:"status"`
				SentAt time.Time `json:"sent_at"`
			}
			if json.Unmarshal(sc.Bytes(), &r) != nil {
				continue
			}
			if r.Status == "sent" || r.Status == "invited" {
				if !seen[r.ID] {
					seen[r.ID] = true
					out = append(out, rec{r.ID, r.Name, r.SentAt})
				}
			}
		}
		// newest first
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
		return out
	}

	people := load()
	if len(people) == 0 {
		cprintln("nothing sent yet")
		return nil
	}

	const cols, rows = 3, 10
	perPage := cols * rows
	cursor := 0
	if !rawOn() {
		for _, r := range people {
			cprintln(r.Name)
		}
		return nil
	}
	defer rawOff()

	for {
		if len(people) == 0 {
			return nil
		}
		if cursor >= len(people) {
			cursor = len(people) - 1
		}
		if cursor < 0 {
			cursor = 0
		}
		page := cursor / perPage
		start := page * perPage
		end := start + perPage
		if end > len(people) {
			end = len(people)
		}
		cw := (contentWidth() - 2) / cols
		if cw < 12 {
			cw = 12
		}
		fmt.Print(clr)
		head := style.Tint(style.Bold, "sent") + style.Tint(style.Dim, fmt.Sprintf("   %d people", len(people)))
		fmt.Print(center(head) + "\r\n\r\n")
		lead := blockLead(cw * cols)
		for row := 0; row < rows; row++ {
			idx0 := start + row
			if idx0 >= end {
				break
			}
			fmt.Print("\r" + lead)
			for c := 0; c < cols; c++ {
				idx := start + c*rows + row
				if idx >= end {
					fmt.Print(strings.Repeat(" ", cw))
					continue
				}
				name := cut(people[idx].Name, cw-2)
				cell := name + strings.Repeat(" ", cw-len([]rune(name)))
				if idx == cursor {
					fmt.Print(style.Hl(cut(people[idx].Name, cw-2)) + strings.Repeat(" ", cw-len([]rune(name))-2))
				} else {
					fmt.Print(cell)
				}
			}
			fmt.Print("\r\n")
		}
		pages := (len(people) + perPage - 1) / perPage
		fmt.Printf("\r\n%s\r\n", center(style.Tint(style.Dim, fmt.Sprintf("page %d/%d   i k ← → move · d remove · q back", page+1, pages))))
		os.Stdout.Sync()

		switch gridKey() {
		case "up":
			if cursor%rows > 0 {
				cursor--
			}
		case "down":
			if cursor%rows < rows-1 && cursor+1 < len(people) {
				cursor++
			}
		case "left":
			if cursor-rows >= 0 {
				cursor -= rows
			}
		case "right":
			if cursor+rows < len(people) {
				cursor += rows
			}
		case "pageup":
			cursor -= perPage
		case "pagedown":
			cursor += perPage
		case "del":
			id := people[cursor].ID
			if removeFromLedger(a.cfg.Store.Path, id) == nil {
				people = load()
			}
		case "back":
			return nil
		}
	}
}

// removeFromLedger rewrites the send ledger without any records for profileID,
// so that person no longer counts as "sent" (can be messaged again).
func removeFromLedger(path, profileID string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	var kept []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		var r struct {
			ID string `json:"profile_id"`
		}
		if json.Unmarshal([]byte(line), &r) == nil && r.ID == profileID {
			continue // drop
		}
		kept = append(kept, line)
	}
	f.Close()
	out := strings.Join(kept, "\n")
	if out != "" {
		out += "\n"
	}
	return os.WriteFile(path, []byte(out), 0o644)
}

// gridKey reads one navigation key for the grid: arrows or i/k/j/l, d remove, q back.
func gridKey() string {
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
			return "right"
		case 'D':
			return "left"
		}
		return ""
	}
	switch b[0] {
	case 3, 27, 'q', 'Q':
		return "back"
	case 'i', 'I':
		return "up"
	case 'k', 'K':
		return "down"
	case 'j', 'J':
		return "left"
	case 'l', 'L':
		return "right"
	case 'd', 'D', 'x', 'X':
		return "del"
	}
	return ""
}

// ---------- helpers ----------

func edit(path string) error {
	ed := os.Getenv("EDITOR")
	if ed == "" {
		for _, c := range []string{"nano", "vim", "vi"} {
			if _, err := exec.LookPath(c); err == nil {
				ed = c
				break
			}
		}
	}
	if ed == "" {
		return fmt.Errorf("no editor found · set the EDITOR variable")
	}
	if dir := filepath.Dir(path); dir != "" {
		os.MkdirAll(dir, 0o755)
	}
	rawOff()
	exitAlt()
	exec.Command("stty", "-F", "/dev/tty", "sane").Run()
	cmd := exec.Command(ed, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	enterAlt()
	return err
}

// wrapText soft-wraps s to width columns on word boundaries.
func wrapText(s string, width int) []string {
	if width < 10 {
		width = 10
	}
	if len([]rune(s)) <= width {
		return []string{s}
	}
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		if line == "" {
			line = word
		} else if len([]rune(line))+1+len([]rune(word)) <= width {
			line += " " + word
		} else {
			out = append(out, line)
			line = word
		}
	}
	if line != "" {
		out = append(out, line)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func msgName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".tmpl")
}

func names(files []string) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = msgName(f)
	}
	return out
}

func pathFor(files []string, name string) string {
	for _, f := range files {
		if msgName(f) == name {
			return f
		}
	}
	return name
}

func indexOfStr(files []string, active string) int {
	for i, f := range files {
		if f == active {
			return i
		}
	}
	return 0
}

func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "message"
	}
	return out
}

// saveConfig writes config.json (pretty).
func saveConfig(path string, c Config) error {
	return os.WriteFile(path, []byte(mustJSON(c)), 0o644)
}

// ---------- spinner ----------

type spinner struct {
	label chan string
	done  chan struct{}
}

func newSpinner(label string) *spinner {
	s := &spinner{label: make(chan string, 8), done: make(chan struct{})}
	go func() {
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		cur, i := label, 0
		t := time.NewTicker(90 * time.Millisecond)
		defer t.Stop()
		fmt.Print(hideCu)
		defer fmt.Print(showCu)
		for {
			select {
			case <-s.done:
				fmt.Print("\r\x1b[2K")
				os.Stdout.Sync()
				return
			case l := <-s.label:
				cur = l
			case <-t.C:
				fmt.Print("\r\x1b[2K" + center(style.Tint(style.Blue, frames[i%len(frames)])+" "+cur))
				os.Stdout.Sync()
				i++
			}
		}
	}()
	return s
}

func (s *spinner) setLabel(l string) {
	select {
	case s.label <- l:
	default:
	}
}

func (s *spinner) stop() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// countCSV returns how many contacts are in the leads file.
func countCSV(path string) int {
	people, err := readCSVQuiet(path)
	if err != nil {
		return 0
	}
	return len(people)
}

// sentTotal counts successfully sent messages in the ledger.
func sentTotal(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var r struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(sc.Bytes(), &r) == nil && (r.Status == "sent" || r.Status == "invited") {
			n++
		}
	}
	return n
}

// pad returns n spaces (0 for negative n).
func pad(n int) string {
	if n < 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

// visLen is the visible width of s, ignoring ANSI escape sequences.
func visLen(s string) int {
	return len([]rune(ansiRe.ReplaceAllString(s, "")))
}
