// Command codeshot turns a CodeGraph index into committed diagrams and CI checks.
package main

import (
	"os"

	"github.com/inth3shadows/codeshot/internal/codeshot"
)

func main() {
	os.Exit(codeshot.Main(os.Args[1:], os.Stdout, os.Stderr))
}
