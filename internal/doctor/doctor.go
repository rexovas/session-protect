package doctor

import (
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/session-protect/session-protect/internal/targets"
)

type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	Warning string `json:"warning,omitempty"`
}

func Run(out io.Writer) int {
	checks := Checks()
	for _, check := range checks {
		line := fmt.Sprintf("%-6s  %-16s", strings.ToUpper(check.Status), check.Name)
		if check.Detail != "" {
			line += "  " + check.Detail
		}
		if check.Warning != "" {
			line += "  " + check.Warning
		}
		fmt.Fprintln(out, line)
	}

	for _, check := range checks {
		if check.Status == "fail" {
			return 1
		}
	}
	return 0
}

func Checks() []Check {
	var checks []Check
	checks = append(checks, commandCheck("git", "git --version"))
	checks = append(checks, commandCheck("git-crypt", "git-crypt --version"))

	for _, target := range targets.DetectAll() {
		status := "ok"
		detail := target.Source
		warning := ""
		if !target.Detected {
			status = "warn"
			warning = "not found; target would be skipped unless configured"
		}
		checks = append(checks, Check{
			Name:    target.Name + " source",
			Status:  status,
			Detail:  detail,
			Warning: warning,
		})
	}

	return checks
}

func commandCheck(name string, versionCommand string) Check {
	path, err := exec.LookPath(name)
	if err != nil {
		return Check{Name: name, Status: "fail", Warning: "not found in PATH"}
	}

	parts := strings.Fields(versionCommand)
	cmd := exec.Command(parts[0], parts[1:]...)
	output, err := cmd.Output()
	detail := path
	if err == nil {
		detail += "  " + strings.TrimSpace(string(output))
	}

	return Check{Name: name, Status: "ok", Detail: detail}
}
