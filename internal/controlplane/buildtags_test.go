package controlplane

// Default-build proof of the D-F1 dependency isolation (PRD §3 D-F1,
// risk "frontend SDK 拖重核心二进制"): under DEFAULT tags the feishu
// package must compile to nothing but its doc file. This runs in
// ordinary CI — the tagged counterpart lives in
// internal/controlplane/feishu/frontend_feishu_test.go and only
// compiles under `-tags feishu`.
//
// The check is mechanical via go/build (stdlib): it evaluates the same
// //go:build constraints the toolchain does, per file, without
// spawning a build. If someone adds an untagged .go file to the feishu
// package — exactly how an SDK import would sneak into the default
// seek binary — GoFiles stops being [doc.go] and this test fails.

import (
	"go/build"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestBuildTags_FeishuExcludedFromDefaultBuild(t *testing.T) {
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Join(filepath.Dir(here), "feishu")

	def := build.Default // no feishu tag: the shipped-binary configuration
	pkg, err := def.ImportDir(dir, 0)
	if err != nil {
		t.Fatalf("import %s under default tags: %v", dir, err)
	}
	if len(pkg.GoFiles) != 1 || pkg.GoFiles[0] != "doc.go" {
		t.Errorf("default build of feishu must compile ONLY doc.go, got GoFiles=%v — an untagged file joined the package, which is how an SDK leaks into the default seek binary", pkg.GoFiles)
	}
	for _, mustIgnore := range []string{"frontend.go", "frontend_feishu_test.go"} {
		if !slices.Contains(pkg.IgnoredGoFiles, mustIgnore) {
			t.Errorf("%s must be recognized-but-ignored under default tags; IgnoredGoFiles=%v", mustIgnore, pkg.IgnoredGoFiles)
		}
	}

	// And the tag must be precisely what lets the code back in.
	tagged := build.Default
	tagged.BuildTags = []string{"feishu"}
	pkg2, err := tagged.ImportDir(dir, 0)
	if err != nil {
		t.Fatalf("import %s with -tags feishu: %v", dir, err)
	}
	if !slices.Contains(pkg2.GoFiles, "frontend.go") {
		t.Errorf("with -tags feishu the implementation must compile in; GoFiles=%v", pkg2.GoFiles)
	}
}
