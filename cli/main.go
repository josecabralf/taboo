// Command taboo is the taboo CLI entrypoint. The application lives in
// cli/internal/app; this thin main only delegates so the CLI's command
// packages are not an accidental public surface of the cli module.
package main

import "github.com/josecabralf/taboo/cli/internal/app"

func main() { app.Execute() }
