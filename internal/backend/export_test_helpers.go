package backend

// FlagSetForTest converts a map into the internal flag set so guard tests can
// drive BuildArgs without spawning the CLI.
func FlagSetForTest(m map[string]bool) flagSet { return flagSet(m) }
