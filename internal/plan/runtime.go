package plan

import "runtime"

func runtimeGOOS() string {
	return runtime.GOOS
}
