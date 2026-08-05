package guard

import (
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// Codex has no live-session registry like claude's. Two process signals
// substitute: resumed sessions carry their uuid in the process args
// (codex resume <id>), and for plain codex processes the working
// directory names the project — the newest codex session there is the
// open one.

var uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// LiveCodex returns open session ids (with the owning pid) and the
// working directories (also with pid) of codex processes whose session id
// is not in their args.
func LiveCodex() (ids map[string]string, cwds map[string]string) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	out, err := exec.Command("ps", "axo", "pid=,args=").Output()
	if err != nil {
		return nil, nil
	}
	ids, pids := parseCodexProcs(string(out))
	cwds = map[string]string{}
	for _, pid := range pids {
		if cwd := processCwd(pid); cwd != "" {
			cwds[cwd] = pid
		}
	}
	return ids, cwds
}

// parseCodexProcs scans a ps listing for codex processes: id→pid for
// those with a uuid in their args, bare pids for those without.
func parseCodexProcs(psOutput string) (ids map[string]string, pids []string) {
	ids = map[string]string{}
	for _, line := range strings.Split(psOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		command := fields[1]
		if base := command[strings.LastIndexByte(command, '/')+1:]; base != "codex" {
			continue
		}
		if id := uuidPattern.FindString(strings.Join(fields[2:], " ")); id != "" {
			ids[id] = fields[0]
		} else {
			pids = append(pids, fields[0])
		}
	}
	return ids, pids
}

// processCwd resolves a process's working directory via lsof.
func processCwd(pid string) string {
	out, err := exec.Command("lsof", "-a", "-p", pid, "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return strings.TrimPrefix(line, "n")
		}
	}
	return ""
}
