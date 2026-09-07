package main

import (
	"fmt"
	"os"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcplaunch"
)

func main() {
	if err := mcplaunch.Run(os.Args[1:], os.Environ(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
