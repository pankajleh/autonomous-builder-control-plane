package main

import (
	"fmt"
	"os"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
)

const version = "0.1.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "version":
		fmt.Println(version)
	case "validate-transition":
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: abcp validate-transition <from> <to>")
			os.Exit(2)
		}
		if err := domain.ValidateTransition(domain.State(os.Args[2]), domain.State(os.Args[3])); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("VALID")
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: abcp <version|validate-transition>")
}
