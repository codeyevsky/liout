// Package tmpl renders message templates and applies the name/company formatting rules.
package tmpl

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"math/rand"
	"regexp"
	"strings"
	"text/template"

	"github.com/codeyevsky/liout/internal/model"
	"github.com/codeyevsky/liout/internal/textx"
)

// NameRule describes how a formatted name is produced.
//
//	source:    first non-empty field wins, e.g. formatted_name -> first_name -> full_name
//	style:     raw | title | upper | lower
//	max_words: 1 keeps only the first name
//	fallback:  used when everything is empty ("" skips the person entirely)
type NameRule struct {
	Source   []string `json:"source"`
	Style    string   `json:"style"`
	MaxWords int      `json:"max_words"`
	Suffix   string   `json:"suffix"`   // fixed suffix, e.g. " there"
	Fallback string   `json:"fallback"` // leave empty to skip people without a usable name
}

type Config struct {
	Name       NameRule          `json:"name"`
	Company    NameRule          `json:"company"`
	Required   []string          `json:"required"`  // empty value -> person is skipped (e.g. ["FormattedName"])
	Fallbacks  map[string]string `json:"fallbacks"` // field -> default value ("Company": "your company")
	MaxChars   int               `json:"max_chars"` // 0 = unlimited; LinkedIn InMail ~1900, invitation note 300
	CollapseWS bool              `json:"collapse_whitespace"`
}

func DefaultConfig() Config {
	return Config{
		Name:       NameRule{Source: []string{"formatted_name", "first_name", "full_name"}, Style: "title", MaxWords: 1},
		Company:    NameRule{Source: []string{"company", "headline"}, Style: "raw", MaxWords: 0},
		Required:   []string{"FormattedName"},
		Fallbacks:  map[string]string{}, // inline [field|fallback] / def control fallbacks
		MaxChars:   0,
		CollapseWS: true,
	}
}

type Renderer struct {
	cfg  Config
	tpl  *template.Template
	raw  string
	name string
}

func New(name, text string, cfg Config) (*Renderer, error) {
	text = expandPlaceholders(text)
	r := &Renderer{cfg: cfg, raw: text, name: name}
	t, err := template.New(name).Funcs(baseFuncs(rand.New(rand.NewSource(1)))).Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, fmt.Errorf("template: %w", err)
	}
	r.tpl = t
	return r, nil
}

// ErrSkip means a required field was empty, so the person must be skipped.
type ErrSkip struct{ Field string }

func (e ErrSkip) Error() string { return "required field is empty: " + e.Field }

func (r *Renderer) Render(p model.Person) (string, error) {
	data := r.Data(p)
	for _, req := range r.cfg.Required {
		if strings.TrimSpace(fmt.Sprint(data[req])) == "" {
			return "", ErrSkip{Field: req}
		}
	}
	// seed from the profile id so spin() is stable per person
	h := fnv.New64a()
	h.Write([]byte(p.Key()))
	rnd := rand.New(rand.NewSource(int64(h.Sum64() >> 1)))

	t, err := r.tpl.Clone()
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Funcs(baseFuncs(rnd)).Execute(&buf, data); err != nil {
		return "", err
	}
	out := buf.String()
	if r.cfg.CollapseWS {
		out = collapse(out)
	}
	if strings.Contains(out, "<no value>") {
		return "", fmt.Errorf("template produced an unresolved field (<no value>)")
	}
	if r.cfg.MaxChars > 0 && len([]rune(out)) > r.cfg.MaxChars {
		return "", fmt.Errorf("message is %d characters, limit is %d", len([]rune(out)), r.cfg.MaxChars)
	}
	return out, nil
}

// Data builds the template variables; every extra CSV column becomes a key too.
func (r *Renderer) Data(p model.Person) map[string]any {
	d := map[string]any{
		"ProfileID":     p.ProfileID,
		"URN":           p.URN,
		"FullName":      p.FullName,
		"FirstName":     p.FirstName,
		"LastName":      p.LastName,
		"Headline":      p.Headline,
		"Title":         p.Title,
		"Company":       p.Company,
		"Location":      p.Location,
		"ProfileURL":    p.ProfileURL,
		"FormattedName": apply(r.cfg.Name, p),
		"CompanyName":   apply(r.cfg.Company, p),
	}
	for k, v := range p.Extra {
		d[k] = v
		d[camel(k)] = v
	}
	if _, ok := d["about"]; !ok {
		d["about"] = ""
	}
	return d
}

func apply(rule NameRule, p model.Person) string {
	src := rule.Source
	if len(src) == 0 {
		src = []string{"full_name"}
	}
	val := ""
	for _, f := range src {
		if v := strings.TrimSpace(p.Field(f)); v != "" {
			val = v
			break
		}
	}
	if val == "" {
		return rule.Fallback
	}
	// a hand-written formatted_name column wins outright
	if len(src) > 0 && strings.EqualFold(src[0], "formatted_name") && strings.TrimSpace(p.FormattedName) != "" {
		return strings.TrimSpace(p.FormattedName) + rule.Suffix
	}
	val = textx.CleanName(val)
	if rule.MaxWords > 0 {
		f := strings.Fields(val)
		if len(f) > rule.MaxWords {
			f = f[:rule.MaxWords]
		}
		val = strings.Join(f, " ")
	}
	switch strings.ToLower(rule.Style) {
	case "title":
		val = textx.Title(val)
	case "upper":
		val = textx.Upper(val)
	case "lower":
		val = textx.Lower(val)
	}
	if val == "" {
		return rule.Fallback
	}
	return val + rule.Suffix
}

func baseFuncs(rnd *rand.Rand) template.FuncMap {
	return template.FuncMap{
		"upper":    textx.Upper,
		"lower":    textx.Lower,
		"title":    textx.Title,
		"clean":    textx.CleanName,
		"first":    textx.FirstToken,
		"last":     textx.LastToken,
		"initials": textx.Initials,
		"ek":       textx.Ek,  // Trendyol + "de" -> Trendyol'da (Turkish case suffix)
		"ekn":      textx.EkN, // yazılım + "la" -> yazılımla (no apostrophe)
		"suffix":   textx.Suffix,
		"def": func(v any, fallback string) string { // {{ def .Company "your company" }}
			s := strings.TrimSpace(fmt.Sprint(v))
			if s == "" || s == "<no value>" {
				return fallback
			}
			return s
		},
		"trunc": func(n int, s string) string {
			r := []rune(s)
			if len(r) <= n {
				return s
			}
			return strings.TrimSpace(string(r[:n])) + "…"
		},
		// spin "a|b|c" -> stable per person, varied across people
		"spin": func(s string) string {
			parts := strings.Split(s, "|")
			return strings.TrimSpace(parts[rnd.Intn(len(parts))])
		},
		"lines": func(s string) string { return strings.ReplaceAll(s, "\\n", "\n") },
	}
}

func collapse(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(strings.Join(strings.Fields(l), " "), " ")
	}
	out := strings.Join(lines, "\n")
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(out)
}

func camel(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' || r == ' ' })
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return b.String()
}

// expandPlaceholders turns friendly [name]/[spin ...] tokens into template
// actions so users don't write Go template syntax. {{ ... }} still works too.
func expandPlaceholders(text string) string {
	// strip note blocks: {{/* ... */}} (template form) and /* ... */ (plain)
	text = commentGo.ReplaceAllString(text, "")
	text = commentC.ReplaceAllString(text, "")
	// [spin a|b|c] -> {{ spin "a|b|c" }}
	text = spinToken.ReplaceAllStringFunc(text, func(m string) string {
		inner := spinToken.FindStringSubmatch(m)[1]
		return `{{ spin "` + inner + `" }}`
	})
	// [company|your own fallback] -> {{ def .Company "your own fallback" }}
	text = fallbackToken.ReplaceAllStringFunc(text, func(m string) string {
		g := fallbackToken.FindStringSubmatch(m)
		field, fb := fieldName(g[1]), g[2]
		return `{{ def .` + field + ` "` + fb + `" }}`
	})
	repl := strings.NewReplacer(
		"[name]", "{{ .FormattedName }}",
		"[firstname]", "{{ .FormattedName }}",
		"[fullname]", "{{ .FullName }}",
		"[company]", `{{ def .Company "your company" }}`,
		"[title]", `{{ def .Title "your role" }}`,
		"[role]", `{{ def .Title "your role" }}`,
		"[headline]", "{{ .Headline }}",
		"[about]", `{{ def .about "" }}`,
		"[location]", "{{ .Location }}",
	)
	return strings.TrimLeft(repl.Replace(text), "\n")
}

var (
	commentGo     = regexp.MustCompile(`(?s){{/\*.*?\*/}}`)
	commentC      = regexp.MustCompile(`(?s)/\*.*?\*/`)
	spinToken     = regexp.MustCompile(`\[spin ([^\]]+)\]`)
	fallbackToken = regexp.MustCompile(`\[(name|firstname|fullname|company|title|role|headline|location|about)\|([^\]]*)\]`)
)

// fieldName maps a friendly token to a template field name.
func fieldName(tok string) string {
	switch tok {
	case "name", "firstname":
		return "FormattedName"
	case "fullname":
		return "FullName"
	case "company":
		return "Company"
	case "title", "role":
		return "Title"
	case "headline":
		return "Headline"
	case "location":
		return "Location"
	case "about":
		return "about"
	}
	return "FormattedName"
}
