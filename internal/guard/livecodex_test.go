package guard

import "testing"

func TestParseCodexProcs(t *testing.T) {
	ps := ` 10862 codex resume
 74108 /usr/local/bin/codex resume 019f4aad-282c-7d53-8056-6b8b39a0f760
 90000 codexish something
 91000 vim codex-notes.txt
`
	ids, pids := parseCodexProcs(ps)
	if ids["019f4aad-282c-7d53-8056-6b8b39a0f760"] != "74108" || len(ids) != 1 {
		t.Fatalf("ids = %v", ids)
	}
	if len(pids) != 1 || pids[0] != "10862" {
		t.Fatalf("pids = %v", pids)
	}
}

func TestProcessCwdCaches(t *testing.T) {
	cwdCacheMu.Lock()
	cwdCache["424242"] = "/cached/path"
	cwdCacheMu.Unlock()
	if got := processCwd("424242"); got != "/cached/path" {
		t.Fatalf("cache miss: %q", got)
	}
}
