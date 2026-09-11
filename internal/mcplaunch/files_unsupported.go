//go:build !linux && !darwin

package mcplaunch

func readProtected(string, int64) ([]byte, error) { return nil, errUnsupported }
func checkExecutable(string) error                { return errUnsupported }
func replaceProcess(string, []string) error       { return errUnsupported }
