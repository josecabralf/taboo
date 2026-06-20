package main

// workshopName derives the per-agent workshop name from a base name and an agent
// name: taboo provisions one workshop per distinct agent (reused across runs)
// rather than one per run. It mirrors the library's internal naming so the CLI's
// validate/list views report the same workshop the run path launches. The library
// keeps the canonical implementation internal (see internal/workshop.WorkshopName);
// this tiny local helper is the CLI's copy of the same one-line rule.
func workshopName(base, agent string) string { return base + "-" + agent }
