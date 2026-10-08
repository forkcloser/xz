// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command gxz compresses and decompresses files in the xz and the classic
// LZMA (.lzma) formats, with a command line modelled on xz.
//
// Use gxz -h to get information about supported flags.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"text/template"

	"github.com/forkcloser/xz/internal/gflag"
	"github.com/forkcloser/xz/internal/term"
)

const (
	usageStr = `Usage: gxz [OPTION]... [FILE]...
Compress or uncompress FILEs in the .xz format (by default, compress FILES
in place).

  -c, --stdout      write to standard output and don't delete input files
  -d, --decompress  force decompression
  -f, --force       force overwrite of output file and compress links
  -F, --format <format>
                    Specify the file format to compress or decompress.
    auto            Default format for compression is xz. For decompression
	            the file content is used to identify the format.
    xz              The xz file format.
    lzma, alone     Compress to the .lzma file format.
  -h, --help        give this help
  -k, --keep        keep (don't delete) input files
  -L, --license     display software license
  -q, --quiet       suppress all warnings
  -v, --verbose     accepted for compatibility; has no effect
  -V, --version     display version string
  -z, --compress    force compression
  -0 ... -9         compression preset; default is 6
  --cpuprofile <file>
                    create a cpuprofile that can be used with go tool pprof

With no file, or when FILE is -, read standard input.

Report bugs using <https://github.com/forkcloser/xz/issues>.
`
)

func usage(w io.Writer) {
	fmt.Fprint(w, usageStr)
}

func licenses(w io.Writer) error {
	out := `
github.com/forkcloser/xz -- xz for Go
====================================

{{.xz}}

Go Programming Language
=======================

The gxz program contains the package gflag that is an extension of
a package from the Go standard library. The package may contain code
from that package.

{{.go}}
`
	out = strings.TrimLeft(out, " \n")

	tmpl, err := template.New("licenses").Parse(out)
	if err != nil {
		panic(fmt.Sprintf("error %s parsing licenses template", err))
	}

	lmap := map[string]string{
		"xz": strings.TrimSpace(xzLicense),
		"go": strings.TrimSpace(goLicense),
	}
	if err = tmpl.Execute(w, lmap); err != nil {
		return fmt.Errorf("error %w writing licenses template", err)
	}

	return nil
}

type options struct {
	help       bool
	stdout     bool
	decompress bool
	force      bool
	format     string
	keep       bool
	license    bool
	version    bool
	quiet      int
	verbose    int
	preset     int
	cpuprofile string
}

func (o *options) Init() {
	if o.preset != 0 {
		panic("options are already initialized")
	}

	gflag.BoolVarP(&o.help, "help", "h", false, "")
	gflag.BoolVarP(&o.stdout, "stdout", "c", false, "")
	gflag.BoolVarP(&o.decompress, "decompress", "d", false, "")
	gflag.BoolVarP(&o.force, "force", "f", false, "")
	gflag.StringVarP(&o.format, "format", "F", formatAuto, "")
	gflag.BoolVarP(&o.keep, "keep", "k", false, "")
	gflag.BoolVarP(&o.license, "license", "L", false, "")
	gflag.BoolVarP(&o.version, "version", "V", false, "")
	gflag.CounterVarP(&o.quiet, "quiet", "q", 0, "")
	gflag.CounterVarP(&o.verbose, "verbose", "v", 0, "")
	gflag.PresetVar(&o.preset, 0, 9, 6, "")
	gflag.StringVarP(&o.cpuprofile, "cpuprofile", "", "", "")
}

// The values of the format option once normalizeFormat has accepted it.
const (
	formatXZ   = "xz"
	formatLZMA = "lzma"
	formatAuto = "auto"
)

// normalizeFormat normalizes the format field of options. If the
// function completes without error the format field will be formatXZ,
// formatLZMA or formatAuto. The latter only if the option decompress is
// true.
func normalizeFormat(o *options) error {
	switch o.format {
	case formatXZ, formatLZMA:
	case formatAuto:
		if !o.decompress {
			o.format = formatXZ
		}
	case "alone":
		o.format = formatLZMA
	default:
		return fmt.Errorf("%w: %q", errFormat, o.format)
	}

	return nil
}

// defaultsFor sets the options the name gxz was invoked as implies: lzcat,
// unxz and the like.
func (o *options) defaultsFor(cmdName string) {
	switch cmdName {
	case "lzma", "glzma":
		o.format = formatLZMA
	case "lzcat", "glzcat":
		o.format = formatLZMA
		fallthrough
	case "xzcat", "gxzcat":
		o.stdout = true
		o.decompress = true
	case "unlzma", "unglzma":
		o.format = formatLZMA
		fallthrough
	case "unxz", "ungxz":
		o.decompress = true
	}
}

// printInfo prints what -h, -L or -V asked for, and reports whether it
// printed anything: those options end the program.
func (o *options) printInfo(cmdName string) (bool, error) {
	switch {
	case o.help:
		usage(os.Stdout)
	case o.license:
		if err := licenses(os.Stdout); err != nil {
			return true, err
		}
	case o.version:
		fmt.Fprintf(os.Stdout, "%s version %s\n", cmdName, version())
	default:
		return false, nil
	}

	return true, nil
}

// reporter prints gxz's messages to standard error, each on a line of its
// own after the command name, and holds back what -q asks, as xz does:
// given once, the warnings (gxz has none today); given twice, the errors
// too.
type reporter struct {
	cmdName string
	quiet   int
}

// fail prints an error unless -q was given twice. It returns: the caller
// ends the program.
func (r *reporter) fail(v any) {
	if r.quiet >= 2 { // -qq, the second level xz defines
		return
	}

	// Standard error is where a failure would be reported; there is no
	// better place for one of its own.
	_, _ = fmt.Fprintf(os.Stderr, "%s: %v\n", r.cmdName, v)
}

// startCPUProfile starts writing a CPU profile to path.
func startCPUProfile(path string) error {
	// #nosec G304 -- the path the user gave -cpuprofile
	f, err := os.Create(path)
	if err != nil {
		return err
	}

	if err = pprof.StartCPUProfile(f); err != nil {
		return fmt.Errorf("starting the CPU profile: %w", err)
	}

	return nil
}

func main() {
	cmdName := filepath.Base(os.Args[0])
	rep := reporter{cmdName: cmdName}

	// initialize flags
	gflag.CommandLine.Init(cmdName, gflag.ExitOnError)
	gflag.CommandLine.Usage = func() { usage(os.Stderr); os.Exit(1) }
	opts := options{}
	opts.Init()
	opts.defaultsFor(cmdName)
	gflag.Parse()

	done, err := opts.printInfo(cmdName)
	if err != nil {
		rep.fail(err)
		os.Exit(1)
	}

	if done {
		os.Exit(0)
	}

	rep.quiet = opts.quiet

	if opts.cpuprofile != "" {
		if err := startCPUProfile(opts.cpuprofile); err != nil {
			rep.fail(err)
			os.Exit(1)
		}
	}

	if err := normalizeFormat(&opts); err != nil {
		pprof.StopCPUProfile()
		rep.fail(err)
		os.Exit(1)
	}

	var args []string

	if gflag.NArg() == 0 {
		opts.stdout = true
		args = []string{"-"}
	} else {
		args = gflag.Args()
	}

	if opts.stdout && !opts.decompress && !opts.force &&
		term.IsTerminal(os.Stdout.Fd()) {
		pprof.StopCPUProfile()
		rep.fail(`Compressed data will not be written to a terminal
Use -f to force compression. For help type gxz -h.`)
		os.Exit(1)
	}

	exit := 0

	for _, arg := range args {
		if err := processFile(arg, &opts); err != nil {
			// An error, not a warning: the file was not processed and the
			// exit status says so, so -q alone does not hide it.
			rep.fail(userError(err))

			exit = 1
		}
	}

	pprof.StopCPUProfile()
	os.Exit(exit)
}
