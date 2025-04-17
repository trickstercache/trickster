// msgp is a code generation tool for
// creating methods to serialize and de-serialize
// Go data structures to and from MessagePack.
//
// This package is targeted at the `go generate` tool.
// To use it, include the following directive in a
// go source file with types requiring source generation:
//
//	//go:generate msgp
//
// The go generate tool should set the proper environment variables for
// the generator to execute without any command-line flags. However, the
// following options are supported, if you need them:
//
//	-o = output file name (default is {input}_gen.go)
//	-file = input file name (or directory; default is $GOFILE, which is set by the `go generate` command)
//	-io = satisfy the `msgp.Decodable` and `msgp.Encodable` interfaces (default is true)
//	-marshal = satisfy the `msgp.Marshaler` and `msgp.Unmarshaler` interfaces (default is true)
//	-tests = generate tests and benchmarks (default is true)
//
// For more information, please read README.md, and the wiki at github.com/tinylib/msgp
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tinylib/msgp/gen"
	"github.com/tinylib/msgp/parse"
	"github.com/tinylib/msgp/printer"
)

var (
	out        = flag.String("o", "", "output file")
	file       = flag.String("file", "", "input file")
	encode     = flag.Bool("io", true, "create Encode and Decode methods")
	marshal    = flag.Bool("marshal", true, "create Marshal and Unmarshal methods")
	tests      = flag.Bool("tests", true, "create tests and benchmarks")
	unexported = flag.Bool("unexported", false, "also process unexported types")
	verbose    = flag.Bool("v", false, "verbose diagnostics")
)

func diagf(f string, args ...interface{}) {
	if !*verbose {
		return
	}
	if f[len(f)-1] != '\n' {
		f += "\n"
	}
	fmt.Fprintf(os.Stderr, f, args...)
}

func exitln(res string) {
	fmt.Fprintln(os.Stderr, res)
	os.Exit(1)
}

func main() {
	flag.Parse()

	if *verbose {
		printer.Logf = diagf
		parse.Logf = diagf
	}

	// GOFILE is set by go generate
	if *file == "" {
		*file = os.Getenv("GOFILE")
		if *file == "" {
			exitln("No file to parse.")
		}
	}

	var mode gen.Method
	if *encode {
		mode |= (gen.Encode | gen.Decode | gen.Size)
	}
	if *marshal {
		mode |= (gen.Marshal | gen.Unmarshal | gen.Size)
	}
	if *tests {
		mode |= gen.Test
	}

	if mode&^gen.Test == 0 {
		exitln("No methods to generate; -io=false && -marshal=false")
	}

	if err := Run(*file, mode, *unexported); err != nil {
		exitln(err.Error())
	}
}

// Run writes all methods using the associated file or path, e.g.
//
//	err := msgp.Run("path/to/myfile.go", gen.Size|gen.Marshal|gen.Unmarshal|gen.Test, false)
func Run(gofile string, mode gen.Method, unexported bool) error {
	if mode&^gen.Test == 0 {
		return nil
	}
	diagf("Input: \"%s\"\n", gofile)
	fs, err := parse.File(gofile, unexported)
	if err != nil {
		return err
	}

	if len(fs.Identities) == 0 {
		diagf("No types requiring code generation were found!")
	}

	return printer.PrintFile(newFilename(gofile, fs.Package), fs, mode)
}

// picks a new file name based on input flags and input filename(s).
func newFilename(old string, pkg string) string {
	if *out != "" {
		if pre := strings.TrimPrefix(*out, old); len(pre) > 0 &&
			!strings.HasSuffix(*out, ".go") {
			return filepath.Join(old, *out)
		}
		return *out
	}

	if fi, err := os.Stat(old); err == nil && fi.IsDir() {
		old = filepath.Join(old, pkg)
	}
	// new file name is old file name + _gen.go
	return strings.TrimSuffix(old, ".go") + "_gen.go"
}
