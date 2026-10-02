//go:build !windows

package process

import (
	"bufio"
	"bytes"
	"context"
	"testing"
	"time"
)

func TestExplicitEmptyEnvironmentDoesNotInheritParent(t *testing.T) {
	t.Setenv("AUDIT_PARENT_SECRET", "must-not-be-inherited")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Keep the fixture alive until its output has been consumed. A bare env
	// exits immediately, racing exec.Cmd.Wait's pipe closure under -race.
	child, err := Start(ctx, Spec{Command: "/bin/sh", Args: []string{"-c", "/usr/bin/env; printf '\\n__ENV_DONE__\\n'; read hold"}, Env: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	reader := bufio.NewReader(child.Reader())
	var output bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "__ENV_DONE__\n" {
			break
		}
		output.WriteString(line)
	}
	if bytes.Contains(output.Bytes(), []byte("AUDIT_PARENT_SECRET=")) {
		t.Fatal("explicitly empty child environment inherited a parent secret")
	}
}
