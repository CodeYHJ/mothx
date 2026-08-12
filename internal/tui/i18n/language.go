package i18n

import (
	"fmt"
	"strings"
	"time"
)

// ConfiguredLanguage is the settings value. Auto is resolved once when a
// Translator is created; render-time languages are always zh or en.
type ConfiguredLanguage string

type Language string

const (
	ConfiguredAuto ConfiguredLanguage = "auto"
	ConfiguredZH   ConfiguredLanguage = "zh"
	ConfiguredEN   ConfiguredLanguage = "en"

	LanguageZH Language = "zh"
	LanguageEN Language = "en"
)

// ParseConfigured normalizes a settings value. Unknown values safely fall back
// to auto and return valid=false so callers can surface a diagnostic.
func ParseConfigured(value string) (configured ConfiguredLanguage, valid bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return ConfiguredAuto, true
	case "zh":
		return ConfiguredZH, true
	case "en":
		return ConfiguredEN, true
	default:
		return ConfiguredAuto, false
	}
}

// Resolve resolves a configured language using the current offset in loc.
// A nil location is treated as an unavailable timezone and falls back to en.
func Resolve(configured ConfiguredLanguage, now time.Time, loc *time.Location) Language {
	switch configured {
	case ConfiguredZH:
		return LanguageZH
	case ConfiguredEN:
		return LanguageEN
	}
	if loc == nil {
		return LanguageEN
	}
	_, offset := now.In(loc).Zone()
	if offset == 8*60*60 {
		return LanguageZH
	}
	return LanguageEN
}

func UTCOffset(now time.Time, loc *time.Location) string {
	if loc == nil {
		return "unknown"
	}
	_, offset := now.In(loc).Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, offset/3600, offset%3600/60)
}
