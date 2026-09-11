// Command poweur-tasks is the PCP-0007 reference application: a task manager
// that stores everything in a Poweur home under /apps/net.poweur.tasks/ and
// talks to the relay over nothing but HTTP/WebDAV with a scoped token.
//
//	poweur dav token --scope dav:rw:/apps/net.poweur.tasks/ --json
//	export POWEUR_TASKS_RELAY=https://relay.poweur.net
//	export POWEUR_TASKS_OWNER=alice.poweur.net
//	export POWEUR_TASKS_TOKEN=...
//	poweur-tasks init && poweur-tasks project new "Kitchen renovation"
package main

import (
	"os"

	"github.com/poweur/tasks/pkg/tasks"
)

func main() {
	os.Exit(tasks.Run(os.Args[1:], os.Stdout, os.Stderr))
}
