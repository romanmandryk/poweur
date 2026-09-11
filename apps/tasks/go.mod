// poweur-tasks — the PCP-0007 reference application.
//
// Deliberately depends on nothing: not on github.com/poweur/identity, not on
// the CLI's internals, not on a third-party HTTP or DAV library. A convention
// that can only be implemented from inside this repo is not a convention, so
// the reference implementation is held to what an outside author has: the
// published document, HTTP, and a scoped token the user minted for it.
module github.com/poweur/tasks

go 1.25.0
