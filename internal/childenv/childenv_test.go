package childenv

import (
	"slices"
	"strings"
	"testing"
)

func TestIsSensitive_CredentialBearingNames(t *testing.T) {
	// The names that actually live in a seek user's environment. If any
	// of these regress to false, a real credential starts reaching
	// model-chosen subprocesses.
	sensitive := []string{
		"DEEPSEEK_API_KEY",
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"GH_TOKEN",
		"GITHUB_TOKEN",
		"NPM_TOKEN",
		"NPM_AUTH_TOKEN",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_ACCESS_KEY_ID",
		"PGPASSWORD",
		"MYSQL_PWD_PASSWORD",
		"DOCKER_PASSWORD",
		"CLOUDFLARE_API_TOKEN",
		"MY_SERVICE_CREDENTIALS",
		"SSH_PRIVATE_KEY",
	}
	for _, name := range sensitive {
		if !IsSensitive(name) {
			t.Errorf("IsSensitive(%q) = false, want true", name)
		}
	}
}

func TestIsSensitive_SeekNamespace(t *testing.T) {
	for _, name := range []string{"SEEK_HOME", "SEEK_SESSIONS_DIR", "SEEK_STYLE", "SEEK_SB_CHILD"} {
		if !IsSensitive(name) {
			t.Errorf("IsSensitive(%q) = false, want true (SEEK_ namespace is withheld)", name)
		}
	}
}

func TestIsSensitive_OrdinaryNamesSurvive(t *testing.T) {
	// Regressions here are just as bad as leaks: scrubbing PATH or
	// SSH_AUTH_SOCK breaks the model's ability to run anything.
	benign := []string{
		"PATH",
		"HOME",
		"SHELL",
		"LANG",
		"LC_ALL",
		"TERM",
		"TMPDIR",
		"USER",
		"PWD",
		"GOPATH",
		"GOCACHE",
		"NODE_ENV",
		"SSH_AUTH_SOCK", // socket path, not a credential — see sensitiveSubstrings
		"GPG_TTY",
		"EDITOR",
		"COLORTERM",
	}
	for _, name := range benign {
		if IsSensitive(name) {
			t.Errorf("IsSensitive(%q) = true, want false", name)
		}
	}
}

func TestIsSensitive_CaseInsensitive(t *testing.T) {
	for _, name := range []string{"deepseek_api_key", "Gh_Token", "seek_home", "pgpassword"} {
		if !IsSensitive(name) {
			t.Errorf("IsSensitive(%q) = false, want true (matching must be case-insensitive)", name)
		}
	}
}

func TestIsSensitive_EmptyName(t *testing.T) {
	if IsSensitive("") {
		t.Error("IsSensitive(\"\") = true, want false")
	}
}

func TestScrub_DropsSensitiveKeepsOrder(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"DEEPSEEK_API_KEY=sk-live-abc",
		"HOME=/home/u",
		"GH_TOKEN=ghp_xyz",
		"LANG=en_US.UTF-8",
		"SEEK_HOME=/home/u/.seek",
	}
	got := Scrub(env)
	want := []string{"PATH=/usr/bin", "HOME=/home/u", "LANG=en_US.UTF-8"}
	if !slices.Equal(got, want) {
		t.Errorf("Scrub() = %v, want %v", got, want)
	}
}

func TestScrub_NeverLeaksSecretValue(t *testing.T) {
	// The blunt assertion: no matter the shape of the result, the secret
	// byte sequence must not appear anywhere in it.
	const secret = "sk-live-do-not-leak-me"
	env := []string{"PATH=/usr/bin", "DEEPSEEK_API_KEY=" + secret}
	for _, e := range Scrub(env) {
		if strings.Contains(e, secret) {
			t.Fatalf("scrubbed env still contains the secret value: %q", e)
		}
	}
}

func TestScrub_KeepListRetainsNamedVar(t *testing.T) {
	env := []string{"PATH=/usr/bin", "GH_TOKEN=ghp_xyz", "DEEPSEEK_API_KEY=sk-live"}
	got := Scrub(env, "GH_TOKEN")
	want := []string{"PATH=/usr/bin", "GH_TOKEN=ghp_xyz"}
	if !slices.Equal(got, want) {
		t.Errorf("Scrub(keep=GH_TOKEN) = %v, want %v", got, want)
	}
}

func TestScrub_KeepListIsCaseInsensitive(t *testing.T) {
	env := []string{"GH_TOKEN=ghp_xyz"}
	if got := Scrub(env, "gh_token"); !slices.Equal(got, env) {
		t.Errorf("Scrub(keep=gh_token) = %v, want %v", got, env)
	}
}

func TestScrub_KeepListIsExactNotSubstring(t *testing.T) {
	// A fuzzy escape hatch would re-leak neighbours: keeping "TOKEN"
	// must not resurrect GH_TOKEN and NPM_TOKEN.
	env := []string{"GH_TOKEN=a", "NPM_TOKEN=b", "TOKEN=c"}
	got := Scrub(env, "TOKEN")
	want := []string{"TOKEN=c"}
	if !slices.Equal(got, want) {
		t.Errorf("Scrub(keep=TOKEN) = %v, want %v", got, want)
	}
}

func TestScrub_KeepListIgnoresBlankEntries(t *testing.T) {
	env := []string{"PATH=/usr/bin", "GH_TOKEN=ghp"}
	got := Scrub(env, "", "   ", "GH_TOKEN")
	want := []string{"PATH=/usr/bin", "GH_TOKEN=ghp"}
	if !slices.Equal(got, want) {
		t.Errorf("Scrub() with blank keep entries = %v, want %v", got, want)
	}
}

func TestScrub_MalformedEntriesDropped(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"NO_EQUALS_SIGN", // not a valid env entry
		"=orphan-value",  // empty name
		"OK=1",
	}
	got := Scrub(env)
	want := []string{"PATH=/usr/bin", "OK=1"}
	if !slices.Equal(got, want) {
		t.Errorf("Scrub() = %v, want %v", got, want)
	}
}

func TestScrub_ValueContainingEqualsIsPreserved(t *testing.T) {
	// Only the FIRST "=" separates name from value; a base64 value with
	// padding must survive byte-identical.
	env := []string{"DATA=a=b==", "PATH=/usr/bin"}
	got := Scrub(env)
	if !slices.Equal(got, env) {
		t.Errorf("Scrub() = %v, want %v", got, env)
	}
}

func TestScrub_EmptyAndNilInput(t *testing.T) {
	if got := Scrub(nil); len(got) != 0 {
		t.Errorf("Scrub(nil) = %v, want empty", got)
	}
	if got := Scrub([]string{}); len(got) != 0 {
		t.Errorf("Scrub([]) = %v, want empty", got)
	}
}

func TestScrub_DoesNotMutateInput(t *testing.T) {
	env := []string{"PATH=/usr/bin", "GH_TOKEN=ghp", "HOME=/h"}
	orig := slices.Clone(env)
	_ = Scrub(env)
	if !slices.Equal(env, orig) {
		t.Errorf("Scrub mutated its input: %v, want %v", env, orig)
	}
}

func TestSanitized_ReadsProcessEnv(t *testing.T) {
	t.Setenv("CHILDENV_TEST_BENIGN", "visible")
	t.Setenv("CHILDENV_TEST_API_KEY", "sk-should-vanish")

	got := Sanitized()

	var sawBenign bool
	for _, e := range got {
		if e == "CHILDENV_TEST_BENIGN=visible" {
			sawBenign = true
		}
		if strings.HasPrefix(e, "CHILDENV_TEST_API_KEY=") {
			t.Error("Sanitized() leaked CHILDENV_TEST_API_KEY")
		}
	}
	if !sawBenign {
		t.Error("Sanitized() dropped the benign CHILDENV_TEST_BENIGN entry")
	}
}

func TestSanitized_KeepListReachesProcessEnv(t *testing.T) {
	t.Setenv("CHILDENV_TEST_API_KEY", "sk-explicitly-allowed")

	var found bool
	for _, e := range Sanitized("CHILDENV_TEST_API_KEY") {
		if e == "CHILDENV_TEST_API_KEY=sk-explicitly-allowed" {
			found = true
		}
	}
	if !found {
		t.Error("Sanitized(keep) did not retain the explicitly allowed variable")
	}
}

func TestWithheld_ReturnsNamesNotValues(t *testing.T) {
	env := []string{"PATH=/usr/bin", "GH_TOKEN=ghp_secret", "DEEPSEEK_API_KEY=sk-secret"}
	got := Withheld(env)
	want := []string{"GH_TOKEN", "DEEPSEEK_API_KEY"}
	if !slices.Equal(got, want) {
		t.Fatalf("Withheld() = %v, want %v", got, want)
	}
	for _, name := range got {
		if strings.ContainsAny(name, "=") {
			t.Errorf("Withheld() returned an entry with a value: %q", name)
		}
		if strings.Contains(name, "secret") {
			t.Errorf("Withheld() leaked a value: %q", name)
		}
	}
}

func TestWithheld_RespectsKeepList(t *testing.T) {
	env := []string{"GH_TOKEN=a", "DEEPSEEK_API_KEY=b"}
	got := Withheld(env, "GH_TOKEN")
	want := []string{"DEEPSEEK_API_KEY"}
	if !slices.Equal(got, want) {
		t.Errorf("Withheld(keep=GH_TOKEN) = %v, want %v", got, want)
	}
}

func TestWithheld_EmptyWhenNothingSensitive(t *testing.T) {
	if got := Withheld([]string{"PATH=/usr/bin", "HOME=/h"}); len(got) != 0 {
		t.Errorf("Withheld() = %v, want empty", got)
	}
}
