package textx

import "testing"

func TestCleanName(t *testing.T) {
	cases := map[string]string{
		"🚀 Dr. AYŞE YILMAZ (Aysha) | Hiring!": "Ayşe Yılmaz",
		"mehmet öz":              "Mehmet Öz",
		"Zeynep Kılıç, MBA":      "Zeynep Kılıç",
		"ILGIN IŞIK":             "Ilgın Işık",
		"Prof. Dr. Cem Çınar 🇹🇷": "Cem Çınar",
		"van der Berg":           "van der Berg",
	}
	for in, want := range cases {
		if got := CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, istenen %q", in, got, want)
		}
	}
}

func TestEk(t *testing.T) {
	cases := []struct{ word, kind, want string }{
		{"Trendyol", "de", "Trendyol'da"},
		{"Getir", "de", "Getir'de"},
		{"Peak", "de", "Peak'ta"}, // yazıma göre uyum; yabancı okunuşlu isimler için CSV'de elle override edilebilir
		{"şirketiniz", "de", "şirketinizde"},
		{"Ahmet", "e", "Ahmet'e"},
		{"Zeynep", "in", "Zeynep'in"},
		{"Ayşe", "e", "Ayşe'ye"},
		{"Bursa", "den", "Bursa'dan"},
		{"ekip", "ile", "ekiple"},
	}
	for _, c := range cases {
		if got := Ek(c.word, c.kind); got != c.want {
			t.Errorf("Ek(%q,%q) = %q, istenen %q", c.word, c.kind, got, c.want)
		}
	}
}

func TestTitleTurkish(t *testing.T) {
	if got := Title("ışıl irmak"); got != "Işıl İrmak" {
		t.Errorf("Title = %q", got)
	}
	if got := Upper("ilgi"); got != "İLGİ" {
		t.Errorf("Upper = %q", got)
	}
}
