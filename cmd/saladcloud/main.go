// Command saladcloud is a thin CLI wrapper over the SaladCloud Public API.
package main

import (
	"os"

	"github.com/saladtechnologies/salad-cloud-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
