package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCLIGenerationWorkflow tests the complete CLI workflow
func TestCLIGenerationWorkflow(t *testing.T) {
	// Build the binary in a temporary directory
	testDir := t.TempDir()
	binaryPath := filepath.Join(testDir, "cozyvalues-gen-test")

	buildCmd := exec.Command("go", "build", "-o", binaryPath)
	buildCmd.Dir = "."
	output, err := buildCmd.CombinedOutput()
	require.NoError(t, err, "failed to build: %s", string(output))

	// Test on each example
	examples := []string{"monitoring.yaml", "postgres.yaml", "virtual-machine.yaml"}

	for _, example := range examples {
		t.Run(example, func(t *testing.T) {
			examplePath := filepath.Join("examples", example)
			tmpDir := t.TempDir()

			goOut := filepath.Join(tmpDir, "types.go")
			schemaOut := filepath.Join(tmpDir, "values.schema.json")

			cmd := exec.Command(
				binaryPath,
				"-v", examplePath,
				"-g", goOut,
				"-s", schemaOut,
			)
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "generator failed for %s: %s", example, string(output))

			// Verify outputs exist and are not empty
			goData, err := os.ReadFile(goOut)
			require.NoError(t, err, "failed to read generated Go file")
			require.NotEmpty(t, goData, "generated Go file should not be empty")
			require.Contains(t, string(goData), "package values", "should contain package declaration")
			require.Contains(t, string(goData), "type Config struct", "should contain Config type")

			schemaData, err := os.ReadFile(schemaOut)
			require.NoError(t, err, "failed to read generated schema file")
			require.NotEmpty(t, schemaData, "generated schema should not be empty")
			require.Contains(t, string(schemaData), `"title": "Chart Values"`, "schema should have title")
			require.Contains(t, string(schemaData), `"properties"`, "schema should have properties")
		})
	}
}
