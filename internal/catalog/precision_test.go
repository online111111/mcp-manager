package catalog

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCloneToolKeepsExactSchemaNumbers(t *testing.T) {
	tool := &mcp.Tool{Name: "exact", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","const":9007199254740993}}}`)}
	clone, _, err := CloneAndAssignPublicName(tool, "srv__exact")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(clone.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `9007199254740993`) {
		t.Fatalf("catalog clone rounded schema number: %s", data)
	}
}
