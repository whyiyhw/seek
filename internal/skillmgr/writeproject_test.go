package skillmgr

// WriteProjectSkill tests (feature-memory-skill-bridge §9 M-B1). The
// five milestone states: happy / Parse 拒 / 同名拒 / 原子写 / name
// 不一致拒.
//
// Note on 同名拒 scope: the CROSS-layer check (user / builtin slots,
// via skills.Get on the cascade) is the TUI caller's contract per
// §2.3 and lands with M-B2; what's pinned here is everything visible
// at the skillmgr boundary — this entry never overwrites an occupied
// project slot, in either of its two on-disk forms.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/skill"
)

func bridgeMD(name string) []byte {
	return []byte("---\nname: " + name + "\ndescription: |\n  When " + name + " comes up, follow this.\nversion: 0.1.0\n---\n\n## When to use\nThe situation this skill covers.\n## Steps\n1. do the thing\n")
}

// writeProject is the one-line call every test below would otherwise
// repeat; pinned clock so a future timestamped artifact (if the
// approved-but-unread Now ever gains a reader) fails loudly here
// rather than silently flaking elsewhere.
func writeProject(t *testing.T, proj, name string, md []byte) (string, error) {
	t.Helper()
	return WriteProjectSkill(WriteProjectOptions{
		ProjectDir: proj,
		Name:       name,
		MD:         md,
		Now: func() time.Time {
			return time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
		},
	})
}

// skillsTree returns every entry under <proj>/.seek, flattened — the
// zero-residue oracle shared by the rejection tests.
func skillsTree(t *testing.T, proj string) []string {
	t.Helper()
	var out []string
	root := filepath.Join(proj, ".seek")
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // missing root → empty tree, which is the point
		}
		if p != root {
			out = append(out, strings.TrimPrefix(p, root+string(os.PathSeparator)))
		}
		return nil
	})
	return out
}

func TestWriteProjectSkill_HappyPath(t *testing.T) {
	proj := t.TempDir()
	md := bridgeMD("deploy-rollback")

	dir, err := writeProject(t, proj, "deploy-rollback", md)
	if err != nil {
		t.Fatalf("WriteProjectSkill: %v", err)
	}
	if want := filepath.Join(proj, ".seek", "skills", "deploy-rollback"); dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}

	// Byte-identical roundtrip.
	got, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if string(got) != string(md) {
		t.Errorf("SKILL.md mutated on write:\n got %q\nwant %q", got, md)
	}

	// Atomic-write residue: exactly one entry — no .install.json
	// (project lane), no sidecar, no leftover .tmp from the rename.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read skill dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "SKILL.md" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("skill dir entries = %v, want exactly [SKILL.md]", names)
	}

	// Loader-visible next session: a fresh Load over the same project
	// dir must serve the skill (the "takes effect next session"
	// promise this entry exists to keep). User slot pointed at an
	// absent temp dir so the machine's real ~/.seek can't leak in.
	set, stats, err := skill.Load(skill.LoadOptions{
		ProjectDir:    proj,
		UserSkillsDir: filepath.Join(t.TempDir(), "no-user-skills"),
	})
	if err != nil {
		t.Fatalf("Load: %v (stats errors: %v)", err, stats.Errors)
	}
	if sk := set.Get("deploy-rollback"); sk == nil {
		t.Fatal("written project skill invisible to a fresh skill.Load")
	} else if sk.Type != skill.TypePackage {
		t.Errorf("loaded type = %v, want TypePackage", sk.Type)
	}
	if sk := set.Get("deploy-rollback"); sk.InstallSource != nil {
		t.Errorf("InstallSource = %+v, want nil (no .install.json on this lane)", sk.InstallSource)
	}
}

func TestWriteProjectSkill_RejectsInvalidMD(t *testing.T) {
	cases := map[string][]byte{
		"missing description": []byte("---\nname: has-name\n---\nbody"),
		"non-kebab name":      []byte("---\nname: Not_Kebab\ndescription: d\n---\nbody"),
		"not frontmatter":     []byte("just some markdown, no fence"),
	}
	for label, md := range cases {
		t.Run(label, func(t *testing.T) {
			proj := t.TempDir()
			_, err := writeProject(t, proj, "has-name", md)
			if err == nil {
				t.Fatal("md that skill.Parse rejects must not land on disk")
			}
			if residue := skillsTree(t, proj); len(residue) != 0 {
				t.Errorf("rejected write left residue: %v", residue)
			}
		})
	}
}

func TestWriteProjectSkill_RefusesExistingName(t *testing.T) {
	occupy := map[string]func(t *testing.T, proj string){
		// A real prior skill: the refuse must leave its bytes alone.
		"prior skill": func(t *testing.T, proj string) {
			if _, err := writeProject(t, proj, "taken", bridgeMD("taken")); err != nil {
				t.Fatalf("seed first write: %v", err)
			}
		},
		// Garbage slot (empty dir, no SKILL.md): still occupied — the
		// name slot is the resource, not the validity of its content.
		"garbage dir": func(t *testing.T, proj string) {
			if err := os.MkdirAll(filepath.Join(proj, ".seek", "skills", "taken"), 0o755); err != nil {
				t.Fatalf("seed garbage dir: %v", err)
			}
		},
	}
	for label, seed := range occupy {
		t.Run(label, func(t *testing.T) {
			proj := t.TempDir()
			seed(t, proj)

			if _, err := writeProject(t, proj, "taken", bridgeMD("taken")); err == nil {
				t.Fatal("second write into an occupied name slot must refuse (never overwrite)")
			}
			// Whatever occupied the slot stays untouched.
			data, err := os.ReadFile(filepath.Join(proj, ".seek", "skills", "taken", "SKILL.md"))
			if label == "prior skill" {
				if err != nil || string(data) != string(bridgeMD("taken")) {
					t.Errorf("refused write disturbed the existing skill: %v", err)
				}
			}
		})
	}
}

func TestWriteProjectSkill_FailureLeavesNoPartialState(t *testing.T) {
	// A .seek that is a regular FILE makes any target path under it
	// unusable. Which internal branch fires (stat ENOTDIR vs mkdir)
	// is OS-dependent, so the test asserts the contract, not the
	// branch: error out, and nothing that looks like a skill tree.
	// (This is why the injection is a file and not a chmod 0500 dir:
	// Windows ignores directory read-only bits for mkdir, and CI runs
	// three OSes.)
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, ".seek"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("seed blocking file: %v", err)
	}

	if _, err := writeProject(t, proj, "blocked", bridgeMD("blocked")); err == nil {
		t.Fatal("write into an unusable .seek must fail")
	}
	// No SKILL.md, no tmp staging, no skill dir anywhere under proj.
	var leaked []string
	filepath.Walk(proj, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasSuffix(p, "SKILL.md") || strings.HasSuffix(p, ".tmp") {
			leaked = append(leaked, p)
		}
		return nil
	})
	if len(leaked) != 0 {
		t.Errorf("failed write left partial state: %v", leaked)
	}
}

func TestWriteProjectSkill_RejectsNameMismatch(t *testing.T) {
	cases := map[string]string{
		"different name": "other-name",
		"empty name":     "",
	}
	for label, name := range cases {
		t.Run(label, func(t *testing.T) {
			proj := t.TempDir()
			_, err := writeProject(t, proj, name, bridgeMD("frontend-name"))
			if err == nil {
				t.Fatalf("Name %q must not install under frontmatter name %q", name, "frontend-name")
			}
			if residue := skillsTree(t, proj); len(residue) != 0 {
				t.Errorf("rejected write left residue: %v", residue)
			}
		})
	}
}
