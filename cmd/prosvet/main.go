// Command prosvet is the console interface and background daemon of Просвет.
package main

import (
	"os"

	"prosvet/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:])) }
