package browse

import (
	"fmt"
	"io"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rexovas/session-protect/internal/config"
)

func Run(args []string, stdout io.Writer, stderr io.Writer) int {
	once := false
	for _, arg := range args {
		switch arg {
		case "--once":
			once = true
		case "help", "-h", "--help":
			fmt.Fprintln(stdout, "Usage:")
			fmt.Fprintln(stdout, "  session-protect browse [--once]")
			return 0
		default:
			fmt.Fprintf(stderr, "unexpected argument: %s\n", arg)
			return 2
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "browse failed: %v\n", err)
		return 1
	}

	m := newModel(cfg)
	if once {
		// Render a single frame without the alternate screen, for scripts
		// and verification.
		fmt.Fprintln(stdout, m.View())
		return 0
	}

	program := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithOutput(stderr))
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(stderr, "browse failed: %v\n", err)
		return 1
	}
	return 0
}
