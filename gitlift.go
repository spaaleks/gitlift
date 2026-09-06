package gitlift

import (
	_ "embed"
	"strings"
)

var Version = "dev"

//go:embed README.md
var readme string

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
