package firewall

import "os"

func isRootInNS() bool { return os.Geteuid() == 0 }
