// liout · LinkedIn outreach automation: regex-driven people search + templated bulk messaging.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/codeyevsky/liout/internal/linkedin"
	"github.com/codeyevsky/liout/internal/match"
	"github.com/codeyevsky/liout/internal/model"
	"github.com/codeyevsky/liout/internal/store"
	"github.com/codeyevsky/liout/internal/tmpl"
)

type Config struct {
	Auth struct {
		LiAt        string `json:"li_at"`      // falls back to the environment variable when empty
		JSession    string `json:"jsessionid"` // falls back to the environment variable when empty
		LiAtEnv     string `json:"li_at_env"`
		JSessionEnv string `json:"jsessionid_env"`
		UserAgent   string `json:"user_agent"`
		Lang        string `json:"lang"`
	} `json:"auth"`
	Search struct {
		URLTemplate string `json:"url_template"`
		Count       int    `json:"count"`
		Filters     string `json:"filters"`
	} `json:"search"`
	Extract struct {
		HeadlinePattern string `json:"headline_pattern"`
		CompanyStrip    string `json:"company_strip"`
	} `json:"extract"`
	Template tmpl.Config `json:"template"`
	Send     struct {
		Mode         string `json:"mode"`
		TemplateFile string `json:"template_file"` // the active message
		MessagesDir  string `json:"messages_dir"`  // local library of saved messages
		DelayMin     int    `json:"delay_min_sec"` // small human-like gap between sends
		DelayMax     int    `json:"delay_max_sec"`
	} `json:"send"`
	Blacklist string `json:"blacklist"`
	Store     struct {
		Path string `json:"path"`
	} `json:"store"`
}

func defaultConfig() Config {
	var c Config
	c.Auth.LiAtEnv = "LI_AT"
	c.Auth.JSessionEnv = "LI_JSESSIONID"
	c.Auth.UserAgent = linkedin.DefaultUA
	c.Auth.Lang = "en_US"
	c.Search.URLTemplate = linkedin.DefaultSearchURL
	c.Search.Count = 30
	c.Extract.HeadlinePattern = match.DefaultHeadlinePattern
	c.Extract.CompanyStrip = match.DefaultCompanyStrip
	c.Template = tmpl.DefaultConfig()
	c.Send.Mode = linkedin.ModeAuto
	c.Send.TemplateFile = ""
	c.Send.MessagesDir = "messages"
	c.Send.DelayMin = 3
	c.Send.DelayMax = 8
	c.Blacklist = "blacklist.txt"
	c.Store.Path = "data/sent.jsonl"
	return c
}

func loadConfig(path string) (Config, error) {
	c := defaultConfig()
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil // fall back to defaults
		}
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// SessionPath: where cookies entered in the TUI are stored (owner-readable only).
const SessionPath = "session.json"

type Session struct {
	LiAt      string    `json:"li_at"`
	JSession  string    `json:"jsessionid"`
	Name      string    `json:"name,omitempty"`
	PublicID  string    `json:"public_id,omitempty"`
	ProfileID string    `json:"profile_id,omitempty"`
	SavedAt   time.Time `json:"saved_at"`
}

func loadSession() Session {
	var s Session
	b, err := os.ReadFile(SessionPath)
	if err != nil {
		return s
	}
	json.Unmarshal(b, &s)
	return s
}

func saveSession(s Session) error {
	s.SavedAt = time.Now()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(SessionPath, append(b, '\n'), 0o600)
}

func clearSession() error {
	err := os.Remove(SessionPath)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// credentials: config.json -> environment variable -> session.json, in that order.
func (c Config) credentials() (liAt, js string) {
	liAt, js = c.Auth.LiAt, c.Auth.JSession
	if liAt == "" {
		liAt = os.Getenv(orDefault(c.Auth.LiAtEnv, "LI_AT"))
	}
	if js == "" {
		js = os.Getenv(orDefault(c.Auth.JSessionEnv, "LI_JSESSIONID"))
	}
	if liAt == "" || js == "" {
		s := loadSession()
		if liAt == "" {
			liAt = s.LiAt
		}
		if js == "" {
			js = s.JSession
		}
	}
	return liAt, js
}

func (c Config) client() (*linkedin.Client, error) {
	liAt, js := c.credentials()
	if liAt == "" || js == "" {
		return nil, errors.New("not signed in · run ./liout and use the Session screen")
	}
	return linkedin.New(liAt, js, c.Auth.UserAgent, c.Auth.Lang)
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// First Ctrl+C cancels the running work, the second one quits the process.
	// (signal.NotifyContext on its own swallows the second Ctrl+C too, which
	// leaves the process impossible to kill from the keyboard.)
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
		restoreTerminal()
		fmt.Fprintf(os.Stderr, "\ninterrupted · press Ctrl+C again to quit\n")
		<-sigCh
		restoreTerminal()
		os.Exit(130)
	}()

	if len(os.Args) < 2 { // no arguments -> interactive menu
		if err := runTUI(ctx, "config.json"); err != nil {
			restoreTerminal()
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	var err error
	switch os.Args[1] {
	case "tui", "ui":
		err = runTUI(ctx, "config.json")
	case "init":
		err = cmdInit(os.Args[2:])
	case "whoami":
		err = cmdWhoami(ctx, os.Args[2:])
	case "search":
		err = cmdSearch(ctx, os.Args[2:])
	case "filter":
		err = cmdFilter(os.Args[2:])
	case "extract":
		err = cmdExtract(os.Args[2:])
	case "preview":
		err = cmdPreview(os.Args[2:])
	case "send":
		err = cmdSend(ctx, os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		restoreTerminal()
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`liout · send a custom first message to the right people on LinkedIn

Run with no arguments to open the interactive menu (everything lives there).

Commands:
  init                     create config and a starter message
  whoami                   verify the saved session
  search   -q "..."        find people, apply regex rules, write a CSV
  search   -url "..."      paginate a search request copied from the browser
  extract  -in dump.json   pull people out of a saved response
  filter   -in leads.csv   narrow a CSV with regex rules
  preview  -in leads.csv   render the active message without sending
  send     -in leads.csv   send the message to everyone on the list

Example:
  liout search -q "backend engineer" -geo 102105699 -max 100 -out leads.csv
  liout preview -in leads.csv
  liout send -in leads.csv
`)
}

func addCommon(fs *flag.FlagSet) *string {
	return fs.String("config", "config.json", "config file")
}

// ---------- init ----------

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite existing files")
	fs.Parse(args)

	files := map[string]string{
		"config.json":   mustJSON(defaultConfig()),
		"blacklist.txt": sampleBlacklist,
	}
	for path, content := range files {
		if _, err := os.Stat(path); err == nil && !*force {
			fmt.Printf("skipped (already exists): %s\n", path)
			continue
		}
		if dir := dirOf(path); dir != "" {
			os.MkdirAll(dir, 0o755)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
		fmt.Printf("created: %s\n", path)
	}
	fmt.Println("\nNext: run ./liout · it opens a browser so you can sign in.")
	return nil
}

func dirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return ""
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b) + "\n"
}

// ---------- whoami ----------

func cmdWhoami(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ExitOnError)
	cfgPath := addCommon(fs)
	fs.Parse(args)
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	cli, err := cfg.client()
	if err != nil {
		return err
	}
	id, err := cli.Me(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("session valid · profile id: %s\n", id)
	return nil
}

// ---------- search ----------

func cmdSearch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	cfgPath := addCommon(fs)
	q := fs.String("q", "", "search keywords")
	out := fs.String("out", "leads.csv", "output CSV")
	maxN := fs.Int("max", 100, "maximum number of people")
	rulesPath := fs.String("rules", "", "regex rules file (rules.json)")
	filters := fs.String("filters", "", "raw Voyager filter, e.g. ,(key:geoUrn,value:List(102105699))")
	geo := fs.String("geo", "", "geoUrn id (e.g. 102105699 = Türkiye)")
	network := fs.String("network", "", "connection degree: F,S,O (comma separated)")
	rawURL := fs.String("url", "", "search request URL copied from the browser")
	fs.Parse(args)

	if strings.TrimSpace(*q) == "" && strings.TrimSpace(*rawURL) == "" {
		return errors.New("-q or -url is required")
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	cli, err := cfg.client()
	if err != nil {
		return err
	}
	ex, err := match.NewExtractor(cfg.Extract.HeadlinePattern, cfg.Extract.CompanyStrip)
	if err != nil {
		return err
	}
	var rules *match.Rules
	if *rulesPath != "" {
		if rules, err = match.Load(*rulesPath); err != nil {
			return err
		}
	}

	f := cfg.Search.Filters + *filters
	if *geo != "" {
		f += ",(key:geoUrn,value:List(" + *geo + "))"
	}
	if *network != "" {
		f += ",(key:network,value:List(" + strings.ReplaceAll(*network, ",", ",") + "))"
	}

	var kept []model.Person
	onPage := func(start int, page []model.Person) {
		for i := range page {
			ex.Apply(&page[i])
			if rules.Match(page[i]) {
				kept = append(kept, page[i])
			}
		}
		fmt.Printf("page %-4d raw=%-3d kept=%d\n", start, len(page), len(kept))
	}

	if *rawURL != "" {
		_, err = cli.SearchByURL(ctx, *rawURL, *maxN, cfg.Search.Count, onPage)
	} else {
		opts := linkedin.SearchOpts{Keywords: *q, URLTmpl: cfg.Search.URLTemplate, Filters: f, Count: cfg.Search.Count, MaxResult: *maxN}
		_, err = cli.SearchPeople(ctx, opts, onPage)
		if err != nil && len(kept) == 0 {
			// if the graphql endpoint is closed, fall back to the legacy REST one
			fmt.Fprintln(os.Stderr, "note: primary search endpoint failed, trying the legacy one…")
			opts.URLTmpl = linkedin.LegacySearchURL
			_, err = cli.SearchPeople(ctx, opts, onPage)
		}
	}
	if err != nil && len(kept) == 0 {
		return fmt.Errorf("%w\n\nHint: LinkedIn may have changed its search endpoint.\n"+
			"Run the search in your browser → F12 → Network → filter for 'graphql' → right-click the request →\n"+
			"Copy → Copy URL, then: ./liout search -url '<paste>' -out leads.csv\n"+
			"(in the TUI: \"Search by pasted URL\")", err)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	if err := store.WriteCSV(*out, kept); err != nil {
		return err
	}
	fmt.Printf("\n%d contacts written -> %s\n", len(kept), *out)
	return nil
}

// ---------- extract ----------

func cmdExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	cfgPath := addCommon(fs)
	in := fs.String("in", "", "saved Voyager JSON/HTML file")
	out := fs.String("out", "leads.csv", "output CSV")
	rulesPath := fs.String("rules", "", "regex rules file")
	fs.Parse(args)
	if *in == "" {
		return errors.New("-in is required")
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	people, err := linkedin.ExtractFromBytes(b)
	if err != nil {
		return err
	}
	ex, err := match.NewExtractor(cfg.Extract.HeadlinePattern, cfg.Extract.CompanyStrip)
	if err != nil {
		return err
	}
	var rules *match.Rules
	if *rulesPath != "" {
		if rules, err = match.Load(*rulesPath); err != nil {
			return err
		}
	}
	var kept []model.Person
	for i := range people {
		ex.Apply(&people[i])
		if rules.Match(people[i]) {
			kept = append(kept, people[i])
		}
	}
	if err := store.WriteCSV(*out, kept); err != nil {
		return err
	}
	fmt.Printf("%d/%d contacts written -> %s\n", len(kept), len(people), *out)
	return nil
}

// ---------- filter ----------

func cmdFilter(args []string) error {
	fs := flag.NewFlagSet("filter", flag.ExitOnError)
	cfgPath := addCommon(fs)
	in := fs.String("in", "leads.csv", "input CSV")
	out := fs.String("out", "filtered.csv", "output CSV")
	rulesPath := fs.String("rules", "rules.json", "regex rules file")
	fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	people, err := store.ReadCSV(*in)
	if err != nil {
		return err
	}
	rules, err := match.Load(*rulesPath)
	if err != nil {
		return err
	}
	ex, err := match.NewExtractor(cfg.Extract.HeadlinePattern, cfg.Extract.CompanyStrip)
	if err != nil {
		return err
	}
	var kept []model.Person
	for i := range people {
		ex.Apply(&people[i])
		if rules.Match(people[i]) {
			kept = append(kept, people[i])
		}
	}
	if err := store.WriteCSV(*out, kept); err != nil {
		return err
	}
	fmt.Printf("%d/%d contacts matched -> %s\n", len(kept), len(people), *out)
	return nil
}

// ---------- preview ----------

func cmdPreview(args []string) error {
	fs := flag.NewFlagSet("preview", flag.ExitOnError)
	cfgPath := addCommon(fs)
	in := fs.String("in", "leads.csv", "input CSV")
	tplPath := fs.String("tpl", "", "message file (defaults to the active one)")
	n := fs.Int("n", 5, "how many samples (0 = all)")
	fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	people, r, ex, err := prepare(cfg, *in, *tplPath)
	if err != nil {
		return err
	}
	shown, skipped := 0, 0
	for i := range people {
		ex.Apply(&people[i])
		msg, err := r.Render(people[i])
		if err != nil {
			skipped++
			fmt.Printf("· [%d] %s SKIPPED: %v\n", i+1, people[i].ProfileID, err)
			continue
		}
		if *n > 0 && shown >= *n {
			continue
		}
		shown++
		fmt.Printf("\n─── [%d] %s (%s / %s) ───\n%s\n", i+1, people[i].FullName, people[i].Title, people[i].Company, msg)
	}
	fmt.Printf("\n%d contacts, %d will be skipped\n", len(people), skipped)
	return nil
}

func prepare(cfg Config, in, tplPath string) ([]model.Person, *tmpl.Renderer, *match.Extractor, error) {
	people, err := store.ReadCSV(in)
	if err != nil {
		return nil, nil, nil, err
	}
	path := tplPath
	if path == "" {
		path = cfg.Send.TemplateFile
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("no active message (%s): %w", path, err)
	}
	r, err := tmpl.New(path, string(b), cfg.Template)
	if err != nil {
		return nil, nil, nil, err
	}
	ex, err := match.NewExtractor(cfg.Extract.HeadlinePattern, cfg.Extract.CompanyStrip)
	if err != nil {
		return nil, nil, nil, err
	}
	return people, r, ex, nil
}

// ---------- send ----------

func cmdSend(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	cfgPath := addCommon(fs)
	in := fs.String("in", "leads.csv", "input CSV")
	tplPath := fs.String("tpl", "", "message file (defaults to the active one)")
	maxSend := fs.Int("limit", 0, "stop after N messages (0 = the whole list)")
	invite := fs.Bool("invite", false, "send a connection invitation with a note instead of a message")
	fs.Parse(args)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	people, r, ex, err := prepare(cfg, *in, *tplPath)
	if err != nil {
		return err
	}
	bl, err := match.LoadBlacklist(cfg.Blacklist)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.Store.Path)
	if err != nil {
		return err
	}
	defer st.Close()

	cli, err := cfg.client()
	if err != nil {
		return err
	}
	if _, err := cli.Me(ctx); err != nil {
		return fmt.Errorf("session check: %w", err)
	}

	fmt.Printf("%d contacts · message: %s\n\n", len(people), cfg.Send.TemplateFile)

	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	pause := func() time.Duration {
		lo, hi := cfg.Send.DelayMin, cfg.Send.DelayMax
		if hi <= lo {
			return time.Duration(lo) * time.Second
		}
		return time.Duration(lo+rnd.Intn(hi-lo)) * time.Second
	}

	sent, skipped, failed, blocked := 0, 0, 0, 0
	for i := range people {
		p := &people[i]
		ex.Apply(p)

		if p.ProfileID == "" && p.URN == "" {
			skipped++
			continue
		}
		if ok, why := bl.Blocked(*p); ok {
			blocked++
			fmt.Printf("⛔ %-28s %s\n", p.ProfileID, why)
			continue
		}
		if st.Seen(p.Key()) {
			skipped++
			fmt.Printf("· %-28s already messaged, skipped\n", p.ProfileID)
			continue
		}
		msg, err := r.Render(*p)
		if err != nil {
			skipped++
			fmt.Printf("· %-28s skipped: %v\n", p.ProfileID, err)
			continue
		}
		if *maxSend > 0 && sent >= *maxSend {
			fmt.Printf("\nreached limit %d, stopping.\n", *maxSend)
			break
		}

		rec := store.Record{ProfileID: p.Key(), URN: p.URN, Name: p.FullName, Company: p.Company, Message: msg, Step: 1}
		if *invite {
			err = cli.SendInvite(ctx, p, msg)
			rec.Status = store.StatusInvited
		} else {
			err = cli.SendMessage(ctx, p, msg, cfg.Send.Mode)
			rec.Status = store.StatusSent
		}
		rec.URN = p.URN
		if err != nil {
			failed++
			rec.Status, rec.Error = store.StatusFailed, err.Error()
			fmt.Printf("✗ %-28s %v\n", p.ProfileID, err)
			st.Append(rec)
			if errors.Is(err, linkedin.ErrAuth) {
				return err
			}
		} else {
			sent++
			fmt.Printf("✓ %-28s %s\n", p.ProfileID, firstLine(msg))
			st.Append(rec)
		}

		select {
		case <-ctx.Done():
			fmt.Println("\ncancelled.")
			return nil
		case <-time.After(pause()):
		}
	}
	fmt.Printf("\ndone · sent: %d · skipped: %d · blacklisted: %d · failed: %d\n", sent, skipped, blocked, failed)
	return nil
}

func readCSVQuiet(path string) ([]model.Person, error) {
	return store.ReadCSV(path)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len([]rune(s)) > 60 {
		s = string([]rune(s)[:60]) + "…"
	}
	return s
}

const sampleBlacklist = `# Blacklist · these people are never messaged.
# One rule per line:
#   ayse-yilmaz-123          profile id (a full profile URL also works)
#   @Trendyol                company name contains this
#   re:headline:(recruiter)  regex on a field
#   fields: full_name, headline, title, company, location, any

re:headline:(open to work|looking for a job|seeking|laid off)
re:full_name:^(test|demo)\b
`

const sampleTemplate = `/*
  liout message

  Drop these in, they fill per person:

    [name]       first name        e.g. Ayse
    [company]    their company     (auto: "your company" if empty)
    [title]      their role
    [headline]   their LinkedIn headline
    [location]   their city / country
    [about]      one line about their current role, if found

  Vary it so every message differs a little:
    [spin Hi|Hey|Hello]        picks one per person

  Your own fallback when a field may be empty:
    [company|our team]         uses "our team" if company is missing
    [title|what you do]

  Anything in this note block is NOT sent.
  Delete this and write your own; it is only a starting point.
*/

[spin Hi|Hey] [name],

Came across your profile and saw your work at [company|your company]. [spin Would love to connect|Thought I would reach out].

Best,
`
