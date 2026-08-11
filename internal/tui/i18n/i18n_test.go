package i18n

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		name       string
		configured ConfiguredLanguage
		loc        *time.Location
		want       Language
	}{
		{"auto zh", ConfiguredAuto, time.FixedZone("+8", 8*60*60), LanguageZH},
		{"auto seven", ConfiguredAuto, time.FixedZone("+7", 7*60*60), LanguageEN},
		{"auto nine", ConfiguredAuto, time.FixedZone("+9", 9*60*60), LanguageEN},
		{"auto utc", ConfiguredAuto, time.UTC, LanguageEN},
		{"auto negative", ConfiguredAuto, time.FixedZone("-5", -5*60*60), LanguageEN},
		{"forced zh", ConfiguredZH, time.UTC, LanguageZH},
		{"forced en", ConfiguredEN, time.FixedZone("+8", 8*60*60), LanguageEN},
		{"missing zone", ConfiguredAuto, nil, LanguageEN},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.loc == nil {
				if got := Resolve(tc.configured, time.Now(), tc.loc); got != tc.want {
					t.Fatalf("Resolve() = %q, want %q", got, tc.want)
				}
				return
			}
			if got := Resolve(tc.configured, time.Date(2026, 8, 10, 12, 0, 0, 0, tc.loc), tc.loc); got != tc.want {
				t.Fatalf("Resolve() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCatalogsComplete(t *testing.T) {
	catalogs := Catalogs()
	if len(catalogs[LanguageEN]) == 0 || len(catalogs[LanguageZH]) == 0 {
		t.Fatal("both catalogs must be non-empty")
	}
	for id, text := range catalogs[LanguageEN] {
		if text == "" || catalogs[LanguageZH][id] == "" {
			t.Fatalf("missing translation for %q", id)
		}
	}
	for id, text := range catalogs[LanguageZH] {
		if text == "" || catalogs[LanguageEN][id] == "" {
			t.Fatalf("missing English translation for %q", id)
		}
	}
}

func TestCatalogPlaceholdersMatch(t *testing.T) {
	catalogs := Catalogs()
	for id, english := range catalogs[LanguageEN] {
		zh := catalogs[LanguageZH][id]
		if got, want := formatVerbs(zh), formatVerbs(english); !reflect.DeepEqual(got, want) {
			t.Errorf("placeholder mismatch for %q: zh=%v en=%v", id, got, want)
		}
	}
}

func TestTranslatorFormatting(t *testing.T) {
	tr := New(LanguageZH)
	got := tr.Text(MsgApprovalSavedPrefix, "go test ")
	if strings.Contains(got, "%!") || !strings.Contains(got, "go test ") {
		t.Fatalf("formatted message = %q", got)
	}
	if got := tr.Text(MessageID("missing.message")); got != "missing.message" {
		t.Fatalf("missing message fallback = %q", got)
	}
}

var printfVerbPattern = regexp.MustCompile(`%(?:\[[0-9]+\])?[+#0 '^-]*(?:[0-9]+|\*)?(?:\.(?:[0-9]+|\*))?[vTtbcdoOqxXUeEfgGsxp]`)

func formatVerbs(s string) []string {
	matches := printfVerbPattern.FindAllString(s, -1)
	for i, match := range matches {
		matches[i] = fmt.Sprintf("%%%c", match[len(match)-1])
	}
	return matches
}
func TestParseConfigured(t *testing.T) {
	for _, input := range []string{"", " auto ", "ZH", "en"} {
		if _, ok := ParseConfigured(input); !ok {
			t.Fatalf("ParseConfigured(%q) marked valid", input)
		}
	}
	if got, ok := ParseConfigured("fr"); ok || got != ConfiguredAuto {
		t.Fatalf("invalid language = %q, valid=%v", got, ok)
	}
}
