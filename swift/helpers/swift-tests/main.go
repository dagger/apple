// swift-tests is the Swift module's helper, built once in a pinned image:
//
//	swift-tests scan    lists every package's tests from source, without SwiftPM
//	swift-tests report  splits one shared swift test run into per-check results
package main

import (
	"fmt"
	"os"
)

func main() {
	var err error
	switch command := ""; {
	case len(os.Args) < 2:
		err = fmt.Errorf("usage: swift-tests scan|report [flags]")
	default:
		command = os.Args[1]
		switch command {
		case "scan":
			err = runScan(os.Args[2:])
		case "report":
			err = runReport(os.Args[2:])
		default:
			err = fmt.Errorf("unknown command %q", command)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
