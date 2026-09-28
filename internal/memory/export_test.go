package memory

// SetSupersedeFault installs f to run inside Supersede's transaction,
// between writing the new record and invalidating the old one, and
// returns a function that removes it. Tests only.
func SetSupersedeFault(f func() error) (restore func()) {
	prev := supersedeFault
	supersedeFault = f
	return func() { supersedeFault = prev }
}
