package openapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestExamplesGeneration tests that all example values.yaml files can be processed
func TestExamplesGeneration(t *testing.T) {
	examplesDir := "../../examples"
	files, err := os.ReadDir(examplesDir)
	require.NoError(t, err)

	for _, file := range files {
		if filepath.Ext(file.Name()) != ".yaml" {
			continue
		}

		t.Run(file.Name(), func(t *testing.T) {
			fullPath := filepath.Join(examplesDir, file.Name())
			rows, err := Parse(fullPath)
			require.NoError(t, err, "failed to parse %s", file.Name())

			root := Build(rows)
			require.NotNil(t, root, "failed to build tree for %s", file.Name())

			tmpdir, goFile, err := WriteGeneratedGoAndStub(root, "values")
			require.NoError(t, err, "failed to generate Go code for %s", file.Name())
			defer os.RemoveAll(tmpdir)

			crdBytes, err := CG(filepath.Dir(goFile))
			require.NoError(t, err, "failed to generate CRD for %s", file.Name())

			schemaPath := filepath.Join(tmpdir, "values.schema.json")
			err = WriteValuesSchema(crdBytes, schemaPath)
			require.NoError(t, err, "failed to write schema for %s", file.Name())

			// Verify schema file exists and is not empty
			schemaData, err := os.ReadFile(schemaPath)
			require.NoError(t, err, "failed to read schema for %s", file.Name())
			require.NotEmpty(t, schemaData, "schema should not be empty for %s", file.Name())
		})
	}
}
