package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"runeshell/internal/devutil"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("pickport", flag.ExitOnError)
	preferred := fs.Int("preferred", 0, "preferred port")
	fs.Parse(args)
	port, err := devutil.PickFreePort(*preferred)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, port)
	return err
}
