package tmpl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sample() Spec {
	visibility := "private"
	branch := "main"
	return Spec{
		Name:        "Captured base",
		Description: "read from group/demo",
		Repo: &Repo{
			Visibility:    &visibility,
			DefaultBranch: &branch,
			Topics:        []string{"go", "cli"},
			Features:      NewFeatures(map[string]bool{"issues": true, "wiki": false, "container_registry": true}),
		},
		BranchRules: &BranchRules{Rules: []Rule{{
			Name: "main", Patterns: []string{"main"}, MergeAccess: "maintainers",
		}}},
		TagRules:      &TagRules{Rules: []TagRule{{Name: "v", Patterns: []string{"v*"}}}},
		MergeRequests: &MergeRequests{MergeMethod: strptr("merge")},
	}
}

func strptr(v string) *string { return &v }

func TestEncodeRoundTripsThroughTheLoader(t *testing.T) {
	dir := t.TempDir()

	path, err := Save(sample(), "gitlab", dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "captured-base.yaml" {
		t.Errorf("path = %q, want a slug of the name", path)
	}

	templates, err := Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) == 0 {
		t.Fatal("the written template did not load back")
	}

	var found Template
	for _, tpl := range templates {
		if tpl.Name == "Captured base" {
			found = tpl
		}
	}
	if found.Name == "" {
		t.Fatal("template not found by name")
	}
	if found.Supports("github") {
		t.Error("a gitlab template must not be offered for github")
	}

	spec, err := found.For("gitlab")
	if err != nil {
		t.Fatal(err)
	}

	if on, ok := spec.Features().Get("container_registry"); !ok || !on {
		t.Error("container_registry did not survive the round trip")
	}
	if on, ok := spec.Features().Get("wiki"); !ok || on {
		t.Error("a declared false must survive as false, not as absence")
	}
	if len(spec.Rules()) != 1 || spec.Rules()[0].MergeAccess != "maintainers" {
		t.Errorf("branch rules = %+v", spec.Rules())
	}
	if len(spec.Tags()) != 1 {
		t.Errorf("tag rules = %+v", spec.Tags())
	}
	if spec.MergeRequests == nil || *spec.MergeRequests.MergeMethod != "merge" {
		t.Errorf("merge requests = %+v", spec.MergeRequests)
	}
}

func TestSaveRefusesToClobber(t *testing.T) {
	dir := t.TempDir()

	if _, err := Save(sample(), "gitlab", dir); err != nil {
		t.Fatal(err)
	}
	_, err := Save(sample(), "gitlab", dir)
	if err == nil {
		t.Fatal("a second save under the same name must not overwrite")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v, want it to say the file is there", err)
	}
}

func TestEncodeOmitsBlocksTheSpecDoesNotDeclare(t *testing.T) {
	body, err := Encode(Spec{Name: "bare"}, "github")
	if err != nil {
		t.Fatal(err)
	}

	text := string(body)
	for _, absent := range []string{"repo:", "branch_rules:", "tag_rules:", "merge_requests:", "pipelines:", "webhooks:"} {
		if strings.Contains(text, absent) {
			t.Errorf("output has %q for a spec that declares nothing:\n%s", absent, text)
		}
	}
	if !strings.Contains(text, "owner_type: user") {
		t.Errorf("output should pin the github context:\n%s", text)
	}
}

func TestFileNameSlugs(t *testing.T) {
	cases := map[string]string{
		"00 GitLab docker":  "00-gitlab-docker.yaml",
		"  Weird //Name!! ": "weird-name.yaml",
		"":                  "template.yaml",
	}
	for input, want := range cases {
		if got := FileName(input); got != want {
			t.Errorf("FileName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestWriteDirsAreUsable(t *testing.T) {
	dirs := WriteDirs()
	if len(dirs) == 0 {
		t.Fatal("no directory offered to save into")
	}
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) {
			t.Errorf("%q is not absolute", dir)
		}
	}
	if _, err := os.Stat(filepath.Dir(dirs[0])); err != nil && !os.IsNotExist(err) {
		t.Errorf("first directory is unusable: %v", err)
	}
}

func TestWriteDirsAreExactlyTheSearchDirs(t *testing.T) {
	t.Setenv("GITLIFT_TEMPLATES", "")

	read := SearchDirs()
	write := WriteDirs()

	if len(read) != len(write) {
		t.Fatalf("write dirs = %v, read dirs = %v, a template must only be saved where it is loaded from", write, read)
	}
	for i := range read {
		if read[i] != write[i] {
			t.Errorf("dir %d: write %q, read %q", i, write[i], read[i])
		}
	}
}

func TestPinnedDirBecomesTheSaveDefault(t *testing.T) {
	t.Setenv("GITLIFT_TEMPLATES", "/templates")

	write := WriteDirs()
	if len(write) == 0 || write[0] != "/templates" {
		t.Fatalf("write dirs = %v, want GITLIFT_TEMPLATES first so a container saves somewhere that persists", write)
	}

	seen := map[string]bool{}
	for _, dir := range write {
		if seen[dir] {
			t.Errorf("write dirs = %v, want no duplicates", write)
		}
		seen[dir] = true
	}
	for _, dir := range SearchDirs() {
		if !seen[dir] {
			t.Errorf("write dirs = %v, want every search dir still offered (%q missing)", write, dir)
		}
	}
}

func TestAbbrevAndExpandRoundTrip(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}

	full := filepath.Join(home, ".config", "gitlift", "templates")
	short := Abbrev(full)

	if !strings.HasPrefix(short, "~/") {
		t.Errorf("Abbrev(%q) = %q, want it to start with ~/", full, short)
	}
	if got := Expand(short); got != full {
		t.Errorf("Expand(%q) = %q, want %q", short, got, full)
	}

	outside := filepath.Join("/etc", "gitlift")
	if got := Abbrev(outside); got != outside {
		t.Errorf("Abbrev(%q) = %q, want it unchanged", outside, got)
	}
	if got := Expand(outside); got != outside {
		t.Errorf("Expand(%q) = %q, want it unchanged", outside, got)
	}
}

func TestSavingIntoASearchedDirMakesItLoadable(t *testing.T) {
	dir := t.TempDir()

	if _, err := Save(sample(), "gitlab", dir); err != nil {
		t.Fatal(err)
	}

	templates, err := Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, tpl := range templates {
		if tpl.Name == "Captured base" {
			found = true
		}
	}
	if !found {
		t.Error("a template saved into a searched directory must load from it")
	}
}

func TestFolderNameRejectsTraversal(t *testing.T) {
	cases := map[string]string{
		"work":          "work",
		"  Work Stuff ": "work-stuff",
		"..":            "",
		"../../etc":     "etc",
		"/etc/passwd":   "etc-passwd",
		"":              "",
		"///":           "",
	}
	for input, want := range cases {
		if got := FolderName(input); got != want {
			t.Errorf("FolderName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestScopesListsCustomFoldersOnly(t *testing.T) {
	templates := []Template{
		{Name: "a", Scope: "builtin", Builtin: true},
		{Name: "b", Scope: "custom"},
		{Name: "c", Scope: "work"},
		{Name: "d", Scope: "work"},
	}

	got := Scopes(templates)
	if len(got) != 2 || got[0] != "custom" || got[1] != "work" {
		t.Errorf("Scopes = %v, want [custom work] with builtin left out and no duplicates", got)
	}
}

func TestSavingIntoAFolderSetsTheScope(t *testing.T) {
	dir := t.TempDir()

	path, err := Save(sample(), "gitlab", filepath.Join(dir, FolderName("Work Stuff")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, filepath.Join("work-stuff", "captured-base.yaml")) {
		t.Errorf("path = %q, want it inside the folder", path)
	}

	templates, err := Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, tpl := range templates {
		if tpl.Name == "Captured base" {
			if tpl.Scope != "work-stuff" {
				t.Errorf("scope = %q, want the folder name", tpl.Scope)
			}
			return
		}
	}
	t.Error("the saved template did not load back")
}

func TestFileNameKeepsDotsOutOfTheSlug(t *testing.T) {
	if got := FileName("00 GitLab docker"); got != "00-gitlab-docker.yaml" {
		t.Errorf("FileName = %q", got)
	}
	if got := FileName("../evil"); got != "evil.yaml" {
		t.Errorf("FileName(%q) = %q, want the traversal stripped", "../evil", got)
	}
}
