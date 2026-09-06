package gitlift

import (
	_ "embed"
	"runtime/debug"
	"strings"
)

var Version = ""

//go:embed README.md
var readme string

func Release() string {
	if Version != "" {
		return Version
	}

	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return "dev"
	}
	return info.Main.Version
}

func Help() string {
	var out []string
	for _, line := range strings.Split(readme, "\n") {
		if strings.HasPrefix(line, "![") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
