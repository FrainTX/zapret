package cli

// restartSelf on Windows: the service stops with an error and the service
// manager's recovery action starts it again — from the new prosvet.exe.
func restartSelf() int { return 3 }
