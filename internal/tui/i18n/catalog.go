package i18n

import (
	"fmt"
	"time"
)

type MessageID string

// Translator is immutable and safe to share across TUI components.
type Translator struct {
	language Language
}

func New(language Language) Translator {
	if language != LanguageZH {
		language = LanguageEN
	}
	return Translator{language: language}
}

func NewFromConfig(value string, now func() time.Time, loc *time.Location) (Translator, ConfiguredLanguage, bool) {
	configured, valid := ParseConfigured(value)
	if now == nil {
		now = time.Now
	}
	return New(Resolve(configured, now(), loc)), configured, valid
}

func (t Translator) Language() Language { return t.language }

func (t Translator) Text(id MessageID, args ...any) string {
	catalog := catalogs[t.language]
	text, ok := catalog[id]
	if !ok || text == "" {
		text = catalogs[LanguageEN][id]
	}
	if text == "" {
		text = string(id)
	}
	if len(args) == 0 {
		return text
	}
	return fmt.Sprintf(text, args...)
}

func Catalogs() map[Language]map[MessageID]string {
	out := make(map[Language]map[MessageID]string, len(catalogs))
	for lang, catalog := range catalogs {
		copyCatalog := make(map[MessageID]string, len(catalog))
		for id, text := range catalog {
			copyCatalog[id] = text
		}
		out[lang] = copyCatalog
	}
	return out
}
