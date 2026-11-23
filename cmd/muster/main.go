// Command Muster is the forges' door into Orrery: it watches GitHub,
// GitLab, and Azure DevOps and writes repositories, pipelines, services,
// deployments, and issues to the catalog.
//
//	Muster serve                         run the service
//	Muster setup github [flags]          create the GitHub App, or read it from an old store
//	Muster setup gitlab [flags]          authorize the GitLab OAuth application
//	Muster import-history <old sqlite>   carry the old store's deployments to Orrery
package main

import (
	"fmt"
	"os"
	_ "time/tzdata"
)

// version is set at build time from VERSION.
var version = "dev"

const usage = `usage:
  muster serve
  muster setup github [--org <org>] [--port 8765] [--from-store <old sqlite>]
  muster setup gitlab [--port 8765] [--from-store <old sqlite>]
  muster setup azuredevops --from-store <old sqlite>
  muster import-history <old sqlite>`

func main() {
	args := os.Args[1:]
	var err error
	switch {
	case len(args) >= 1 && args[0] == "serve":
		err = serve()
	case len(args) >= 2 && args[0] == "setup" && args[1] == "github":
		err = setupGitHub(args[2:])
	case len(args) >= 2 && args[0] == "setup" && args[1] == "gitlab":
		err = setupGitLab(args[2:])
	case len(args) >= 2 && args[0] == "setup" && args[1] == "azuredevops":
		err = setupAzureDevOps(args[2:])
	case len(args) == 2 && args[0] == "import-history":
		err = importHistory(args[1])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "muster:", err)
		os.Exit(1)
	}
}
