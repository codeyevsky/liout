// Package textx does Turkish-aware text cleanup, casing and case-suffix generation.
package textx

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	trToUpper = strings.NewReplacer("i", "İ", "ı", "I")
	trToLower = strings.NewReplacer("İ", "i", "I", "ı")
)

// Upper/Lower preserve the Turkish i/ı distinction.
func Upper(s string) string { return strings.ToUpper(trToUpper.Replace(s)) }
func Lower(s string) string { return strings.ToLower(trToLower.Replace(s)) }

// Title capitalizes each word and lowercases the rest (ÇAĞLA -> Çağla, ışıl -> Işıl).
func Title(s string) string {
	var b strings.Builder
	sep := true
	for _, r := range s {
		switch {
		case unicode.IsSpace(r) || r == '-':
			b.WriteRune(r)
			sep = true
		case sep:
			b.WriteString(Upper(string(r)))
			sep = false
		default:
			b.WriteString(Lower(string(r)))
		}
	}
	return b.String()
}

// NormalizeCase fixes names written in all caps or all lowercase and leaves
// mixed-case names (McDonald, van der Berg) alone.
func NormalizeCase(s string) string {
	if s == "" {
		return s
	}
	if s == Upper(s) || s == Lower(s) {
		return Title(s)
	}
	return s
}

var (
	reParen  = regexp.MustCompile(`\s*[\(\[][^\)\]]*[\)\]]`)
	reCreds  = regexp.MustCompile(`(?i)\s*[,|·•\-–—]\s*(mba|m\.b\.a\.?|ph\.?d\.?|m\.?sc\.?|b\.?sc\.?|md|cpa|pmp|cfa|ceng|acca|pmi|msa|llm)\b.*$`)
	reHonor  = regexp.MustCompile(`(?i)^\s*(dr|doç|doc|prof|op|av|uzm|mr|mrs|ms|miss|sn|sayın|eng|ing)\.?\s+`)
	reTail   = regexp.MustCompile(`\s*[|·•/]\s*.*$`)
	reSpaces = regexp.MustCompile(`\s+`)
	keepRune = "-'’.,()[]|&/·•"
)

// StripSymbols replaces emoji and decorative characters with spaces.
func StripSymbols(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), unicode.IsSpace(r), strings.ContainsRune(keepRune, r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return b.String()
}

// CleanName: "🚀 Dr. Ayşe YILMAZ (Aysha) | Hiring, MBA" -> "Ayşe Yılmaz"
func CleanName(s string) string {
	s = StripSymbols(s)
	s = reParen.ReplaceAllString(s, " ")
	s = reTail.ReplaceAllString(s, "")
	s = reCreds.ReplaceAllString(s, "")
	for reHonor.MatchString(s) {
		s = reHonor.ReplaceAllString(s, "")
	}
	s = strings.Trim(reSpaces.ReplaceAllString(strings.TrimSpace(s), " "), " ,.-")
	return NormalizeCase(s)
}

// FirstToken / LastToken return the first and last word of a cleaned name.
func FirstToken(s string) string {
	f := strings.Fields(CleanName(s))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func LastToken(s string) string {
	f := strings.Fields(CleanName(s))
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

func Initials(s string) string {
	var b strings.Builder
	for _, f := range strings.Fields(CleanName(s)) {
		r := []rune(f)
		b.WriteString(Upper(string(r[0])))
		b.WriteString(".")
	}
	return b.String()
}

const (
	vowels     = "aeıioöuü"
	backVowels = "aıou"
	roundVowel = "oöuü"
	hardCons   = "fstkçşhp"
)

// Suffix builds a Turkish case suffix following vowel harmony.
// kind: de|loc, den|abl, e|dat, i|acc, in|gen, ile, li
func Suffix(word, kind string) string {
	w := strings.TrimRight(strings.TrimSpace(Lower(word)), ".")
	if w == "" {
		return ""
	}
	r := []rune(w)
	var lastVowel rune = 'e'
	for i := len(r) - 1; i >= 0; i-- {
		if strings.ContainsRune(vowels, r[i]) {
			lastVowel = r[i]
			break
		}
	}
	back := strings.ContainsRune(backVowels, lastVowel)
	round := strings.ContainsRune(roundVowel, lastVowel)
	last := r[len(r)-1]
	endsVowel := strings.ContainsRune(vowels, last)
	hard := strings.ContainsRune(hardCons, last)

	pick := func(bi, bu, fi, fu string) string {
		switch {
		case back && !round:
			return bi
		case back && round:
			return bu
		case !back && !round:
			return fi
		default:
			return fu
		}
	}

	switch strings.ToLower(kind) {
	case "de", "loc", "da":
		d := "d"
		if hard {
			d = "t"
		}
		if back {
			return d + "a"
		}
		return d + "e"
	case "den", "abl", "dan":
		d := "d"
		if hard {
			d = "t"
		}
		if back {
			return d + "an"
		}
		return d + "en"
	case "e", "dat", "a":
		buf := ""
		if endsVowel {
			buf = "y"
		}
		if back {
			return buf + "a"
		}
		return buf + "e"
	case "i", "acc", "ı":
		buf := ""
		if endsVowel {
			buf = "y"
		}
		return buf + pick("ı", "u", "i", "ü")
	case "in", "gen", "ın":
		buf := ""
		if endsVowel {
			buf = "n"
		}
		return buf + pick("ın", "un", "in", "ün")
	case "ile", "with":
		if endsVowel {
			if back {
				return "yla"
			}
			return "yle"
		}
		if back {
			return "la"
		}
		return "le"
	case "li", "lı":
		return pick("lı", "lu", "li", "lü")
	}
	return ""
}

// Ek appends the suffix with an apostrophe for proper nouns, directly otherwise.
// "Trendyol" + "de" -> "Trendyol'da" ; "şirketiniz" + "de" -> "şirketinizde"
func Ek(word, kind string) string {
	w := strings.TrimSpace(word)
	s := Suffix(w, kind)
	if s == "" || w == "" {
		return word
	}
	if !isProper(w) {
		return w + s
	}
	return w + "'" + s
}

// isProper treats a leading capital as a proper noun, per the Turkish apostrophe rule.
func isProper(w string) bool {
	r := []rune(w)
	if len(r) == 0 {
		return false
	}
	f := string(r[0])
	return Upper(f) == f && Lower(f) != f
}

// EkN never uses an apostrophe -> "yazılımcıyla"
func EkN(word, kind string) string {
	s := Suffix(word, kind)
	if s == "" {
		return word
	}
	return strings.TrimSpace(word) + s
}
