package server

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

func TestDumpEnDict(t *testing.T) {
	keys := make([]string, 0, len(enDict))
	for k := range enDict {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = enDict[k]
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile("/tmp/endict.json", b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log("dumped", len(keys), "keys")
}
