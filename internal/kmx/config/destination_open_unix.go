//go:build !windows

package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
	"oras.land/oras-go/v2/registry"
)

// LoadDestinations distinguishes optional absence (nil, nil) from invalid state.
// The descriptor walk and bounded regular-file read never follow links or block
// on a substituted FIFO. Diagnostics do not expose YAML or filesystem content.
func LoadDestinations(root string) (*Destinations, error) {
	abs, err := filepath.Abs(root)
	if err != nil || !destinationText(abs) {
		return nil, ErrDestinationsInvalid
	}
	dir, err := openDestinationsRoot(abs)
	if err != nil {
		return nil, ErrDestinationsInvalid
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), "kmx.yaml", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrDestinationsInvalid
	}
	file := os.NewFile(uintptr(fd), "kmx.yaml")
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxDestinationsBytes {
		return nil, ErrDestinationsInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxDestinationsBytes+1))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || len(raw) > maxDestinationsBytes || int64(len(raw)) != before.Size() || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) || secretshapes.Match(string(raw)) != nil {
		return nil, ErrDestinationsInvalid
	}
	var node yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&node) != nil || len(node.Content) != 1 || !validDestinationNode(node.Content[0], 0, "") {
		return nil, ErrDestinationsInvalid
	}
	var extra yaml.Node
	if decoder.Decode(&extra) != io.EOF {
		return nil, ErrDestinationsInvalid
	}
	var doc Destinations
	decoder = yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if decoder.Decode(&doc) != nil || doc.Version != 1 || len(doc.Targets) > maxDestinationsEntries || len(doc.Registries) > maxDestinationsEntries {
		return nil, ErrDestinationsInvalid
	}
	if doc.EvalTarget != "" {
		if _, ok := doc.Targets[doc.EvalTarget]; !ok {
			return nil, ErrDestinationsInvalid
		}
	}
	if doc.PublishRepository != "" {
		ref, err := registry.ParseReference(doc.PublishRepository)
		if err != nil || ref.Reference != "" || !validDestinationHost(ref.Registry) {
			return nil, ErrDestinationsInvalid
		}
	}
	for host := range doc.Registries {
		if !validDestinationHost(host) {
			return nil, ErrDestinationsInvalid
		}
	}
	for name, target := range doc.Targets {
		if !destinationName.MatchString(name) || target.Context == "" || target.Runtime != "" && target.Runtime != "orka" && target.Runtime != "kagent" && target.Runtime != "agentsessions" || target.Inference != "" && target.Bindings != "" || target.Namespace != "" && scaffold.ValidateNamespace(target.Namespace) != nil {
			return nil, ErrDestinationsInvalid
		}
		if target.Inference != "" && (!strings.HasPrefix(target.Inference, "provider:") || scaffold.ValidateObjectName(strings.TrimPrefix(target.Inference, "provider:")) != nil) {
			return nil, ErrDestinationsInvalid
		}
		if target.Runtime == "kagent" && target.Inference != "" || target.Runtime == "orka" && target.Bindings != "" {
			return nil, ErrDestinationsInvalid
		}
		if target.EvaluationTarget != "" {
			if _, ok := doc.Targets[target.EvaluationTarget]; !ok {
				return nil, ErrDestinationsInvalid
			}
		}
		if target.Bindings != "" && !filepath.IsAbs(target.Bindings) {
			target.Bindings = filepath.Join(abs, target.Bindings)
		}
		if target.Bindings != "" && !destinationText(target.Bindings) {
			return nil, ErrDestinationsInvalid
		}
		doc.Targets[name] = target
	}
	return &doc, nil
}

func openDestinationsRoot(abs string) (*os.File, error) {
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), "/")
	for _, part := range strings.Split(strings.TrimPrefix(abs, "/"), "/") {
		if part == "" {
			continue
		}
		fd, err = unix.Openat(int(dir.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		dir.Close()
		if err != nil {
			return nil, err
		}
		dir = os.NewFile(uintptr(fd), part)
	}
	return dir, nil
}
