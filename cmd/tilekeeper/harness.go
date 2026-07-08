package main

import (
	"fmt"
	"os"
)

// runHarness dispatches the `tilekeeper harness <subcommand>` group.
// Currently only the property-based fuzzer lives here.
func runHarness(args []string) {
	if len(args) == 0 {
		printHarnessUsage()
		os.Exit(0)
	}
	switch args[0] {
	case "fuzz":
		runHarnessFuzz(args[1:])
	case "-h", "--help":
		printHarnessUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown harness subcommand: %s\n", args[0])
		printHarnessUsage()
		os.Exit(1)
	}
}

func printHarnessUsage() {
	fmt.Println("tilekeeper harness — property-based testing tools")
	fmt.Println()
	fmt.Println("Subcommands:")
	fmt.Println("  fuzz    Property-based fuzzer for layout decision logic")
}
