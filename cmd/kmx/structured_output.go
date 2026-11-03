package main

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// YAML is a presentation of the exact existing JSON report. The underlying
// operation still owns selection, preflight, privacy and exit semantics.
type yamlOutput struct {
	out          io.Writer
	buffer       bytes.Buffer
	enabled      bool
	err          error
	selectOutput func(bool)
}

func (w *yamlOutput) Write(b []byte) (int, error) {
	if !w.enabled {
		return w.out.Write(b)
	}
	if w.err != nil {
		return 0, w.err
	}
	if w.buffer.Len()+len(b) > 16<<20 {
		w.err = errors.New("structured output exceeds 16 MiB")
		return 0, w.err
	}
	return w.buffer.Write(b)
}
func (w *yamlOutput) finish(err error) error {
	if !w.enabled {
		return err
	}
	if w.err != nil {
		return errors.Join(err, w.err)
	}
	if w.buffer.Len() == 0 {
		return err
	}
	return errors.Join(err, cliui.WriteJSONAsYAML(w.out, w.buffer.Bytes()))
}

type yamlFormatValue struct {
	pflag.Value
	output *yamlOutput
}

func (v *yamlFormatValue) Set(value string) error {
	v.output.enabled = value == "yaml"
	if v.output.selectOutput != nil {
		v.output.selectOutput(v.output.enabled)
	}
	if value == "yaml" {
		value = "json"
	}
	return v.Value.Set(value)
}
func (v *yamlFormatValue) String() string { return v.Value.String() }
func enableYAMLOutput(root *cobra.Command, out *yamlOutput) {
	seen := map[*pflag.Flag]bool{}
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, set := range []*pflag.FlagSet{cmd.Flags(), cmd.PersistentFlags()} {
			flag := set.Lookup("output")
			if flag == nil || seen[flag] || !strings.Contains(flag.Usage, "json") {
				continue
			}
			seen[flag] = true
			flag.Value = &yamlFormatValue{Value: flag.Value, output: out}
			if cmd.Name() == "run" {
				flag.Usage = "output: text|json|yaml"
			} else {
				flag.Usage += "; YAML available"
			}
			if _, ok := cmd.GetFlagCompletionFunc("output"); !ok {
				_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{flag.DefValue, "json", "yaml"}))
			}
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
}
