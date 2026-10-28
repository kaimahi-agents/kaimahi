package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"reflect"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

// One invocation input, copied onto each new Task; never an Agent property.
// Policy is opaque decoded bytes. JSON uses Orka's base64 byte representation.
type orkaTaskEnforcement struct {
	Format               string   `json:"format"`
	Policy               []byte   `json:"policy"`
	RequiredCapabilities []string `json:"requiredCapabilities"`
	Mode                 string   `json:"mode"`
}

func readOrkaTaskEnforcement(ctx context.Context, filename string) (*orkaTaskEnforcement, error) {
	if filename == "" {
		return nil, nil
	}
	path, err := filepath.Abs(filename)
	if err != nil {
		return nil, fmt.Errorf("resolve enforcement file: %w", err)
	}
	parent, err := fileaccess.OpenDirectory(ctx, filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open enforcement file directory: %w", err)
	}
	defer parent.Close()
	file, err := fileaccess.OpenRegularAt(ctx, parent, filepath.Base(path))
	if err != nil {
		return nil, fmt.Errorf("open enforcement file: %w", err)
	}
	defer file.Close()
	const bound = 1 << 20
	body, err := io.ReadAll(io.LimitReader(file, bound+1))
	if err != nil {
		return nil, fmt.Errorf("read enforcement file: %w", err)
	}
	if len(body) > bound {
		return nil, fmt.Errorf("enforcement file exceeds 1 MiB")
	}
	var requirement orkaTaskEnforcement
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&requirement) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("enforcement file must contain one complete Orka requirement JSON object, with base64 policy bytes")
	}
	if requirement.Format == "" || len(requirement.Format) > 256 ||
		len(requirement.Policy) == 0 || len(requirement.Policy) > 512<<10 ||
		len(requirement.RequiredCapabilities) == 0 || len(requirement.RequiredCapabilities) > 32 ||
		requirement.Mode != "enforce" {
		return nil, fmt.Errorf("invalid enforcement requirement: require a bounded format, 1..524288 policy bytes, 1..32 capabilities and mode enforce")
	}
	seen := make(map[string]bool, len(requirement.RequiredCapabilities))
	for _, capability := range requirement.RequiredCapabilities {
		if capability == "" || len(capability) > 256 || seen[capability] {
			return nil, fmt.Errorf("enforcement capabilities must be unique nonempty strings of at most 256 bytes")
		}
		seen[capability] = true
	}
	return &requirement, ctx.Err()
}

func (a *App) prepareOrkaTaskEnforcement(ctx context.Context, namespace string, task map[string]any, session *orkaResultSession, failureScope string) error {
	if a.taskEnforcement == nil {
		return session.probeBeforeCreate(ctx, namespace, orkaObjectName(task), failureScope)
	}
	task["spec"].(map[string]any)["enforcement"] = a.taskEnforcement
	if err := session.probeTaskBeforeCreate(ctx, namespace, orkaObjectName(task), failureScope, true); err != nil {
		return err
	}
	body, err := json.Marshal(task)
	if err != nil {
		return err
	}
	admitted, err := a.orkaCapture(ctx, body, "-n", namespace, "create", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json")
	if err != nil {
		return fmt.Errorf("enforcement admission preflight failed; no Task created: %w", err)
	}
	if !orkaTaskEnforcementMatches(admitted, a.taskEnforcement) {
		return fmt.Errorf("enforcement admission preflight changed or dropped the complete requirement; no Task created")
	}
	return nil
}

func orkaTaskEnforcementMatches(raw []byte, expected *orkaTaskEnforcement) bool {
	var task struct {
		Spec struct {
			Enforcement *orkaTaskEnforcement `json:"enforcement"`
		} `json:"spec"`
	}

	if json.Unmarshal(raw, &task) != nil || task.Spec.Enforcement == nil {
		return false
	}
	return reflect.DeepEqual(task.Spec.Enforcement, expected)
}

func (a *App) noteOrkaEnforcement(task *orkaObject, last *string) {
	if task.Enforcement == nil {
		return
	}
	line := fmt.Sprintf("Task %s/%s UID %s: enforcement %s",
		task.Metadata.Namespace, task.Metadata.Name, task.Metadata.UID, task.Enforcement.Summary())
	if line != *last {
		if a.taskEnforcementNote != nil {
			a.taskEnforcementNote(line)
		} else {
			a.notef("%s", line)
		}
		*last = line
	}
}
