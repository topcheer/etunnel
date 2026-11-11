package server

// User-site internationalization: 10+ mainstream languages. One dictionary
// per language; the English table is the reference - every other language
// must cover exactly the same keys (enforced by TestI18nCompleteness), and T
// falls back to English for any miss at runtime.
//
// Language selection: the `et_lang` cookie wins (the switcher sets it), then
// Accept-Language with q-values, then English. `ar` renders RTL (langDir);
// page templates set <html dir> from it.
//
// Translators' contract: keys are stable identifiers; values may contain
// inline HTML (<b>, <span>, <code>, <br>) preserved verbatim; {placeholders}
// are substituted by the renderer after lookup.

import (
	"net/http"
	"strconv"
	"strings"
)

// Supported UI languages, in switcher order. en is the fallback.
var uiLangs = []string{"en", "zh-CN", "ja", "ko", "de", "fr", "es", "pt-BR", "it", "ru", "ar"}

// uiLangNames is the endonym shown in the switcher (native spelling is the
// convention users expect, e.g. 日本語 not "Japanese").
var uiLangNames = map[string]string{
	"en": "English", "zh-CN": "简体中文", "ja": "日本語", "ko": "한국어",
	"de": "Deutsch", "fr": "Français", "es": "Español", "pt-BR": "Português",
	"it": "Italiano", "ru": "Русский", "ar": "العربية",
}

// langDir reports the text direction for <html dir=...>.
func langDir(lang string) string {
	if lang == "ar" {
		return "rtl"
	}
	return "ltr"
}

// pickLang resolves the UI language for a request: the et_lang cookie, then
// Accept-Language with q-values, then en. A country variant (pt-BR) beats its
// base (pt) at equal q because it is the more specific match; a bare base
// maps to its primary variant (pt -> pt-BR).
func pickLang(r *http.Request) string {
	if c, err := r.Cookie("et_lang"); err == nil {
		if norm := normLang(c.Value); norm != "" {
			return norm
		}
	}
	best, bestQ := "", -1.0
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tag, q := part, 1.0
		if i := strings.IndexByte(part, ';'); i >= 0 {
			tag = strings.TrimSpace(part[:i])
			for _, kv := range strings.Split(part[i+1:], ";") {
				kv = strings.TrimSpace(kv)
				if strings.HasPrefix(kv, "q=") {
					if v, err := strconv.ParseFloat(kv[2:], 64); err == nil {
						q = v
					}
				}
			}
		}
		if q <= 0 {
			continue
		}
		if norm := normLang(tag); norm != "" && q > bestQ {
			best, bestQ = norm, q
		} else if base := normLang(baseTag(tag)); base != "" && q > bestQ {
			best, bestQ = base, q
		}
	}
	if best != "" {
		return best
	}
	return "en"
}

func baseTag(tag string) string {
	if i := strings.IndexByte(tag, '-'); i > 0 {
		return tag[:i]
	}
	return tag
}

// normLang maps a BCP-47-ish tag to a supported UI language ("" if none).
func normLang(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	switch tag {
	case "en", "en-us", "en-gb":
		return "en"
	case "zh", "zh-cn", "zh-hans", "zh-sg":
		return "zh-CN"
	case "ja":
		return "ja"
	case "ko":
		return "ko"
	case "de", "de-de", "de-at":
		return "de"
	case "fr", "fr-fr":
		return "fr"
	case "es", "es-es", "es-419", "es-mx", "es-ar":
		return "es"
	case "pt", "pt-br":
		return "pt-BR"
	case "it", "it-it":
		return "it"
	case "ru":
		return "ru"
	case "ar":
		return "ar"
	}
	return ""
}

// dicts holds every registered language table (filled by each i18n_*.go
// file's init via registerDict).
var dicts = map[string]map[string]string{}

func registerDict(lang string, d map[string]string) {
	// Overlay the legal-page chrome keys (i18n_legal.go) at registration
	// time: independent of Go's file-order init sequencing, and visible to
	// TestI18nCompleteness like every other key.
	if lk := legalKeys[lang]; lk != nil {
		for k, v := range lk {
			d[k] = v
		}
	}
	if dk := domKeys[lang]; dk != nil {
		for k, v := range dk {
			d[k] = v
		}
	}
	dicts[lang] = d
}

// T looks up key in lang's dictionary, falling back to English, then to the
// key itself (a missing English key is a bug the completeness test catches;
// the runtime fallback keeps pages renderable regardless).
func T(lang, key string) string {
	if d := dicts[lang]; d != nil {
		if v, ok := d[key]; ok {
			return v
		}
	}
	if v, ok := dicts["en"][key]; ok {
		return v
	}
	return key
}

// langSwitchHTML renders the compact language switcher shared by the pages:
// the current language bold, the rest as cookie-setting links.
func langSwitchHTML(current string) string {
	var b strings.Builder
	b.WriteString(`<div class="langsw" title="language">`)
	sep := ""
	for _, l := range uiLangs {
		if l == current {
			b.WriteString(sep + `<b>` + uiLangNames[l] + `</b>`)
		} else {
			b.WriteString(sep + `<a href="/setlang?l=` + l + `">` + uiLangNames[l] + `</a>`)
		}
		sep = ` `
	}
	b.WriteString(`</div>`)
	return b.String()
}

// handleSetLang sets the et_lang cookie and returns to the referer (only
// same-site targets: relative or our own domain - never an open redirect).
func (s *Server) handleSetLang(w http.ResponseWriter, r *http.Request) {
	l := normLang(r.URL.Query().Get("l"))
	if l == "" {
		l = "en"
	}
	http.SetCookie(w, &http.Cookie{
		Name: "et_lang", Value: l, Path: "/", MaxAge: 365 * 24 * 3600,
		HttpOnly: false, SameSite: http.SameSiteLaxMode,
	})
	back := r.Header.Get("Referer")
	if back == "" {
		back = "/"
	}
	if strings.Contains(back, "://") {
		u := back
		if i := strings.Index(u, "://") + 3; i > 3 {
			host := u[i:]
			if j := strings.IndexByte(host, '/'); j >= 0 {
				host = host[:j]
			}
			host = strings.ToLower(strings.Split(host, ":")[0])
			want := strings.ToLower(s.cfg.Domain)
			if host != want && !strings.HasSuffix(host, "."+want) {
				back = "/" // foreign referer: no open redirect
			} else if host == want {
				// keep absolute same-site URL
			}
		}
	}
	if back == "" {
		back = "/"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
