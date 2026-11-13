package targets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mapIndex is a FolderIndex over folder → home label → session count.
type mapIndex map[string]map[string]int

func (m mapIndex) HomesAt(dir string) map[string]int { return m[dir] }

func (m mapIndex) OtherUnder(dir string, label string) bool {
	for path, homes := range m {
		if !isUnder(path, dir) {
			continue
		}
		for other, n := range homes {
			if other != label && n > 0 {
				return true
			}
		}
	}
	return false
}

func TestResolveOwnerRules(t *testing.T) {
	homes := []ClaudeHome{{Label: "default"}, {Label: "acme"}, {Label: "globex"}}
	index := mapIndex{
		"/p":                {"default": 9},
		"/p/acme":           {"acme": 3, "default": 70}, // mixed: owned by acme
		"/p/acme/team":      {"acme": 1},
		"/p/both":           {"acme": 1, "globex": 1}, // contested
		"/p/mine":           {"default": 4},
		"/p/broad":          {"globex": 1}, // claim over others' work
		"/p/broad/personal": {"default": 2},
	}
	for _, tc := range []struct {
		folder, owner, claimedAt, broad string
		conflict                        bool
	}{
		{folder: "/p/acme", owner: "acme", claimedAt: "/p/acme"},
		{folder: "/p/acme/team", owner: "acme", claimedAt: "/p/acme/team"},
		{folder: "/p/acme/new-thing", owner: "acme", claimedAt: "/p/acme"}, // inherits: clean below /p/acme
		{folder: "/p/mine", owner: "default"},
		{folder: "/p/new", owner: "default"},
		{folder: "/p/both", owner: "default", claimedAt: "/p/both", conflict: true},
		{folder: "/p/both/sub", owner: "default", broad: "/p/both"},
		{folder: "/p/broad", owner: "globex", claimedAt: "/p/broad"}, // its own sessions: owned
		{folder: "/p/broad/elsewhere", owner: "default", broad: "/p/broad"},
	} {
		got := ResolveOwner(index, homes, tc.folder)
		if got.Owner.Label != tc.owner || got.ClaimedAt != tc.claimedAt || got.BroadClaim != tc.broad || (len(got.Conflict) > 0) != tc.conflict {
			t.Errorf("%s: %+v", tc.folder, got)
		}
	}
	// acme's own subfolder with default sessions blocks inheritance below
	// it only for its own subtree, not for /p/acme's other children.
	index["/p/acme/legacy"] = map[string]int{"default": 5}
	if got := ResolveOwner(index, homes, "/p/acme/new-thing"); got.Owned() {
		t.Errorf("inherited through an unclean subtree: %+v", got)
	}
}

// From disk: claims are verified against each transcript's real cwd, so a
// slug collision (a-b vs a/b) cannot hand a folder to the wrong home.
func TestFolderOwnerFromDisk(t *testing.T) {
	home := homesEnv(t)
	acme := mkdir(t, filepath.Join(home, "acme-config"))
	if _, _, err := RegisterClaudeDir(acme, "acme"); err != nil {
		t.Fatal(err)
	}
	write := func(dir, project string) {
		t.Helper()
		slugDir := mkdir(t, filepath.Join(dir, "projects", ClaudeSlug(project)))
		if err := os.WriteFile(filepath.Join(slugDir, "s.jsonl"), []byte(`{"type":"mode"}`+"\n"+`{"cwd":"`+project+`"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	work := filepath.Join(home, "work")
	write(acme, filepath.Join(work, "client"))
	write(filepath.Join(home, ".claude"), filepath.Join(work, "client-tools")) // a sibling, not beneath
	write(acme, filepath.Join(work, "a", "b"))                                 // same slug as work/a-b

	if got := FolderOwner(filepath.Join(work, "client", "new")); got.Owner.Label != "acme" || !got.Inherited(filepath.Join(work, "client", "new")) {
		t.Errorf("new subfolder = %+v", got)
	}
	if got := FolderOwner(filepath.Join(work, "a-b")); got.Owned() {
		t.Errorf("slug collision handed a-b to acme: %+v", got)
	}
	if got := FolderOwner(filepath.Join(work, "a", "b")); got.Owner.Label != "acme" {
		t.Errorf("a/b = %+v", got)
	}
	if !strings.HasPrefix(FolderOwner(work).Owner.Label, "default") {
		t.Error("an unclaimed parent was owned")
	}
}
