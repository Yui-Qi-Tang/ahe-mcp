//go:build !darwin && !linux

package pending

import "os"

const checkpointPlatformSupported = false
const checkpointOpenFlags = 0

func checkpointOwner(os.FileInfo) bool      { return false }
func checkpointSingleLink(os.FileInfo) bool { return false }
func lockCheckpoint(*os.File) error         { return ErrUnsupported }
func unlockCheckpoint(*os.File) error       { return ErrUnsupported }
