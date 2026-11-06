package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const configCommandUsage = `usage: neoproxy config convert --to toml [--force] [input] [output]

Imports a legacy YAML configuration as TOML without changing the source file.
Startup also imports legacy YAML automatically when no sibling TOML exists.

  input   defaults to config.yaml in the working directory
  output  defaults to input with the .toml extension; "-" writes to stdout
`

// runConfigCommand runs `neoproxy config <subcommand>` and returns the exit code.
func runConfigCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "convert" {
		fmt.Fprint(stderr, configCommandUsage)
		return 2
	}
	flags := flag.NewFlagSet("config convert", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, configCommandUsage) }
	to := flags.String("to", "", "Target format: toml")
	force := flags.Bool("force", false, "Overwrite an existing output file")
	if errParse := flags.Parse(args[1:]); errParse != nil {
		return 2
	}
	target, errFormat := config.ParseConfigFormat(*to)
	if errFormat != nil || target != config.ConfigFormatTOML || flags.NArg() > 2 {
		fmt.Fprint(stderr, configCommandUsage)
		return 2
	}
	input, output := flags.Arg(0), flags.Arg(1)
	if input == "" {
		wd, errGetwd := os.Getwd()
		if errGetwd != nil {
			fmt.Fprintf(stderr, "config convert: %v\n", errGetwd)
			return 1
		}
		input = filepath.Join(wd, "config.yaml")
	}
	if output == "" {
		output = strings.TrimSuffix(input, filepath.Ext(input)) + target.Extension()
	}
	if errConvert := convertConfigFile(input, output, target, *force, stdout); errConvert != nil {
		fmt.Fprintf(stderr, "config convert: %v\n", errConvert)
		return 1
	}
	return 0
}

func convertConfigFile(input, output string, target config.ConfigFormat, force bool, stdout io.Writer) error {
	raw, errRead := os.ReadFile(input)
	if errRead != nil {
		return errRead
	}
	source := config.DetectConfigFormat(input, raw)
	if source == target {
		return fmt.Errorf("%s is already %s", input, target)
	}
	converted, errConvert := config.ConvertConfigFile(raw, source, target, filepath.Base(input))
	if errConvert != nil {
		return fmt.Errorf("convert %s: %w", input, errConvert)
	}
	if output == "-" {
		_, errWrite := stdout.Write(converted)
		return errWrite
	}
	if !force {
		if _, errStat := os.Stat(output); errStat == nil {
			return fmt.Errorf("%s exists; pass --force to overwrite it", output)
		} else if !errors.Is(errStat, fs.ErrNotExist) {
			return errStat
		}
	}
	if errWrite := os.WriteFile(output, converted, 0o600); errWrite != nil {
		return errWrite
	}
	fmt.Fprintf(stdout, "Wrote %s (%s, converted from %s %s). Start with --config %s to use it.\n", output, target, source, input, output)
	return nil
}
