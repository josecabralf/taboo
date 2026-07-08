// Command taboo is the taboo CLI entrypoint. This thin main only delegates to
// cli/internal/app so the command packages are not an accidental public surface.
package main

import "github.com/josecabralf/taboo/cli/internal/app"

func main() { app.Execute() }
