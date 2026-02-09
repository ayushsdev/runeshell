package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"runeshell/internal/qr"
)

var errUsage = errors.New("usage: qrprint <url>")

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, errUsage) {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("qrprint", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() == 0 {
		return errUsage
	}
	return qr.RenderANSI(out, fs.Arg(0))
}
