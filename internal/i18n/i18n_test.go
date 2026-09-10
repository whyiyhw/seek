package i18n

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"en", "en"},
		{"EN", "en"},
		{"en_US", "en"},
		{"en_GB.UTF-8", "en"},
		{"zh", "zh"},
		{"zh_CN", "zh"},
		{"zh_CN.UTF-8", "zh"},
		{"zh-Hans", "zh"},
		{" zh ", "zh"},
		{"fr", ""},
		{"fr_FR", ""},
		{"ja_JP.UTF-8", ""},
		{"_weird", ""}, // empty primary tag → nothing before the separator
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolvePrecedence(t *testing.T) {
	t.Setenv("SEEK_LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "")

	if got := Resolve(""); got != LangEn {
		t.Errorf("Resolve with nothing set = %q, want en (English is the resting state)", got)
	}

	// LANG alone works — a zh_CN terminal gets zh with zero config.
	t.Setenv("LANG", "zh_CN.UTF-8")
	if got := Resolve(""); got != LangZh {
		t.Errorf("Resolve with LANG=zh_CN.UTF-8 = %q, want zh", got)
	}

	// LC_ALL beats LANG.
	t.Setenv("LC_ALL", "en_US")
	if got := Resolve(""); got != LangEn {
		t.Errorf("Resolve with LC_ALL=en_US LANG=zh_CN = %q, want en", got)
	}

	// Config beats both locale variables.
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	if got := Resolve("en"); got != LangEn {
		t.Errorf("Resolve(config=en) over zh locale = %q, want en", got)
	}
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "en_US")
	if got := Resolve("zh"); got != LangZh {
		t.Errorf("Resolve(config=zh) over en locale = %q, want zh", got)
	}

	// SEEK_LANG beats config.
	t.Setenv("SEEK_LANG", "zh")
	if got := Resolve("en"); got != LangZh {
		t.Errorf("Resolve with SEEK_LANG=zh config=en = %q, want zh", got)
	}
	// An unsupported SEEK_LANG falls through rather than winning.
	t.Setenv("SEEK_LANG", "fr")
	if got := Resolve("zh"); got != LangZh {
		t.Errorf("Resolve with SEEK_LANG=fr config=zh = %q, want zh (unsupported env falls through)", got)
	}
	// SEEK_LANG is normalised, not compared verbatim.
	t.Setenv("SEEK_LANG", "zh_CN.UTF-8")
	if got := Resolve("en"); got != LangZh {
		t.Errorf("Resolve with SEEK_LANG=zh_CN.UTF-8 = %q, want zh", got)
	}
}

func TestNewUnknownLanguageFallsBackToEnglish(t *testing.T) {
	for _, lang := range []string{"", "fr", "japanese"} {
		b := New(lang)
		if b.Lang() != LangEn {
			t.Errorf("New(%q).Lang() = %q, want en", lang, b.Lang())
		}
	}
	// Tags are case-insensitive — "ZH" is just zh.
	if b := New("ZH"); b.Lang() != LangZh {
		t.Errorf("New(ZH).Lang() = %q, want zh", b.Lang())
	}
	if b := New("zh"); b.Lang() != LangZh {
		t.Errorf("New(zh).Lang() = %q, want zh", b.Lang())
	}
}

func TestTFallbackChain(t *testing.T) {
	en := New("en")
	zh := New("zh")

	if got := zh.T("view.starting"); got != "启动中…" {
		t.Errorf("zh bundle returned %q, want the zh catalogue entry", got)
	}
	if got := en.T("view.starting"); got != "starting…" {
		t.Errorf("en bundle returned %q, want the en catalogue entry", got)
	}

	// A zh bundle missing a key falls back to English (parity makes
	// this unreachable via New, so construct the hole directly).
	sparse := &Bundle{lang: LangZh, msgs: map[string]string{}}
	if got := sparse.T("view.starting"); got != "starting…" {
		t.Errorf("sparse zh bundle returned %q, want English fallback", got)
	}

	// Missing everywhere degrades to the key, never an empty string.
	if got := en.T("no.such.key"); got != "no.such.key" {
		t.Errorf("missing key returned %q, want the key itself", got)
	}
	if got := zh.T("no.such.key"); got != "no.such.key" {
		t.Errorf("missing key (zh) returned %q, want the key itself", got)
	}
}

func TestTArgs(t *testing.T) {
	zh := New("zh")
	got := zh.T("view.thinking_s", 7)
	if got != "思考中… 7 秒" {
		t.Errorf("zh formatted = %q, want 思考中… 7 秒", got)
	}
	got = zh.T("view.thinking_ms", 2, 5)
	if got != "思考中… 2 分 5 秒" {
		t.Errorf("zh formatted = %q, want 思考中… 2 分 5 秒", got)
	}
	// %q quoting is language-neutral but still formats.
	got = zh.T("lang.unsupported", "fr")
	if !strings.Contains(got, `"fr"`) {
		t.Errorf("lang.unsupported = %q, want quoted fr inside", got)
	}
	// No args with a placeholder-bearing format: Sprintf is skipped,
	// the raw template comes through (call sites own their arity).
	if got := New("en").T("cmd.unknown"); got != "unknown command %s — try /help" {
		t.Errorf("no-arg T on placeholder key = %q, want raw template", got)
	}
}

// TestCatalogueParity is the drift guard: every catalogue carries
// exactly the English key set — no missing translations, no orphaned
// ones — and every value is non-empty. A key that exists only in zh
// would be unreachable dead text; one missing from zh silently renders
// English, which is exactly the drift this test makes loud.
func TestCatalogueParity(t *testing.T) {
	for name, msgs := range map[string]map[string]string{"zh": messagesZh} {
		for k, v := range msgs {
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s catalogue: key %s has an empty value", name, k)
			}
			if _, ok := messagesEn[k]; !ok {
				t.Errorf("%s catalogue: key %s absent from en (orphaned translation)", name, k)
			}
		}
		for k, v := range messagesEn {
			if _, ok := msgs[k]; !ok {
				t.Errorf("%s catalogue: en key %s has no translation", name, k)
			} else if strings.TrimSpace(v) == "" {
				t.Errorf("en catalogue: key %s has an empty value", k)
			}
		}
	}
}

// TestCataloguePlaceholderParity keeps format verbs in step across
// catalogues: a translation that drops or adds a %s/%d breaks only at
// runtime, deep inside some /lang path. Compare the per-key verb
// multisets instead.
func TestCataloguePlaceholderParity(t *testing.T) {
	verbs := func(s string) string {
		var out []string
		for i := 0; i < len(s); i++ {
			if s[i] == '%' && i+1 < len(s) {
				out = append(out, s[i:i+2])
				i++
			}
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	for k, en := range messagesEn {
		if verbs(en) != verbs(messagesZh[k]) {
			t.Errorf("key %s: en verbs [%s] != zh verbs [%s]", k, verbs(en), verbs(messagesZh[k]))
		}
	}
}

func TestDefaultBundle(t *testing.T) {
	t.Cleanup(func() { SetDefault(nil) })

	if Lang() != LangEn {
		t.Fatalf("package starts in %q, want en before any SetDefault", Lang())
	}
	SetDefault(New("zh"))
	if Lang() != LangZh {
		t.Errorf("Lang() after SetDefault(zh) = %q, want zh", Lang())
	}
	if got := T("view.thinking"); got != "思考中…" {
		t.Errorf("package T under zh = %q, want 思考中…", got)
	}
	SetDefault(nil)
	if Lang() != LangEn {
		t.Errorf("Lang() after SetDefault(nil) = %q, want en", Lang())
	}
}

// TestConcurrentSwapAndRead exercises the atomic-pointer swap under
// -race: readers must never observe a torn bundle while /lang swaps
// the process default mid-render.
func TestConcurrentSwapAndRead(t *testing.T) {
	t.Cleanup(func() { SetDefault(nil) })

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if s := T("view.thinking"); s == "" {
					panic("T returned empty string under swap")
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		SetDefault(New("zh"))
		SetDefault(New("en"))
	}
	close(stop)
	wg.Wait()
}

// TestTranslationSmoke renders every catalogued key through both
// bundles with synthetic args where the template has verbs, so a bad
// format string (e.g. a translator writing %d where English has %s)
// fails here rather than at some UI call site.
func TestTranslationSmoke(t *testing.T) {
	for _, b := range []*Bundle{New("en"), New("zh")} {
		for k, template := range messagesEn {
			args := []any{}
			for i := 0; i < len(template); i++ {
				if template[i] == '%' && i+1 < len(template) && template[i+1] != '%' {
					switch template[i+1] {
					case 'd':
						args = append(args, 3)
					case 'q':
						args = append(args, "x")
					default:
						args = append(args, "s")
					}
				}
			}
			got := b.T(k, args...)
			if got == "" || strings.Contains(got, "%!") {
				t.Errorf("%s bundle: key %s rendered %q (format verb mismatch?)", b.Lang(), k, got)
			}
			_ = fmt.Sprint(got)
		}
	}
}
