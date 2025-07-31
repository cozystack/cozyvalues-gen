// values-gen: Generate Go structs, CRDs and Helm-compatible JSON Schemas
//
//	from “## @param / @field” comments inside a values.yaml file.
//
// Controller-gen ≥ v0.16 is required for CRD generation.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cozystack/cozyvalues-gen/internal/openapi"
	"github.com/cozystack/cozyvalues-gen/internal/readme"
	"github.com/spf13/pflag"
	sigyaml "sigs.k8s.io/yaml"
)

var Version = "dev"

var (
	inValues  string
	module    string
	outGo     string
	outCRD    string
	outSchema string
	outReadme string
)

func init() {
	pflag.BoolP("version", "V", false, "print version and exit")
	pflag.StringVarP(&inValues, "values", "v", "values.yaml", "annotated Helm values.yaml")
	pflag.StringVarP(&module, "module", "m", "values", "package name")
	pflag.StringVarP(&outGo, "debug-go", "g", "", "output *.go file")
	pflag.StringVarP(&outCRD, "debug-crd", "c", "", "output CRD YAML")
	pflag.StringVarP(&outSchema, "schema", "s", "", "output values.schema.json")
	pflag.StringVarP(&outReadme, "readme", "r", "", "update README.md Parameters section")
}

func main() {
	pflag.Parse()

	if v, _ := pflag.CommandLine.GetBool("version"); v {
		fmt.Println("Version:", Version)
		os.Exit(0)
	}

	rows, err := openapi.Parse(inValues)
	if err != nil {
		fmt.Printf("parse: %v\n", err)
		os.Exit(1)
	}
	tree := openapi.Build(rows)

	// Pull defaults directly from YAML.
	yamlRaw, _ := os.ReadFile(inValues)
	var yamlRoot map[string]interface{}
	_ = sigyaml.Unmarshal(yamlRaw, &yamlRoot)
	openapi.PopulateDefaults(tree, yamlRoot, tree.Child)

	//if undef := openapi.CollectUndefined(tree); len(undef) > 0 {
	//	fmt.Printf("unknown (empty) types: %s\n", strings.Join(undef, ", "))
	//	os.Exit(1)
	//}

	var (
		tmpdir     string
		goFilePath string
	)

	// Generate Go files only if required
	if outGo != "" || outCRD != "" || outSchema != "" {
		var genErr error
		tmpdir, goFilePath, genErr = openapi.WriteGeneratedGoAndStub(tree, module)
		if genErr != nil {
			fmt.Printf("write generated: %v\n", genErr)
		}
		defer os.RemoveAll(tmpdir)
	}

	if outGo != "" {
		code, _ := os.ReadFile(goFilePath)
		_ = os.MkdirAll(filepath.Dir(outGo), 0o755)
		_ = os.WriteFile(outGo, code, 0o644)
		fmt.Printf("write Go structs (possibly unformatted): %s\n", outGo)
	}

	var crdBytes []byte
	if outCRD != "" || outSchema != "" {
		crdBytes, err = openapi.CG(filepath.Dir(goFilePath))
		if err != nil {
			fmt.Printf("controller-gen: %v\n", err)
			os.Exit(1)
		}
	}

	if outCRD != "" {
		_ = os.MkdirAll(filepath.Dir(outCRD), 0o755)
		_ = os.WriteFile(outCRD, crdBytes, 0o644)
		fmt.Printf("write CRD resource: %s\n", outCRD)
	}

	if outSchema != "" {
		if err := openapi.WriteValuesSchema(crdBytes, outSchema); err != nil {
			fmt.Printf("values schema: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("write JSON schema: %s\n", outSchema)
	}

	if outReadme != "" {
		if err := readme.UpdateParametersSection(inValues, outReadme); err != nil {
			fmt.Printf("README: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("update README parameters: %s\n", outReadme)
	}
}
