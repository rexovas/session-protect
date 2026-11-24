package guard

import "testing"

func TestParseCodexProcs(t *testing.T) {
	ps := ` 10862 codex resume
 74108 /usr/local/bin/codex resume 019f4aad-282c-7d53-8056-6b8b39a0f760
 90000 codexish something
 91000 vim codex-notes.txt
 47513 /Users/u/.codex-acme/packages/app-server-daemon/releases/0.171.0-aarch64-apple-darwin/bin/codex app-server --listen unix:// --managed-daemon
 41330 /Users/u/.codex/packages/app-server-daemon/releases/0.171.0-aarch64-apple-darwin/bin/codex app-server daemon pid-update-loop
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
