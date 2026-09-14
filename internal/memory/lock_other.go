//go:build windows

package memory

func lockRole(string, string) (func(), error) { return func() {}, nil }
