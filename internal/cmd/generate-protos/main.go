// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:generate go run . -execute

package main

import (
	"flag"
	"fmt"
	"go/format"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	gengogrpc "github.com/golang/protobuf/v2/cmd/protoc-gen-go-grpc/internal_gengogrpc"
	gengo "github.com/golang/protobuf/v2/cmd/protoc-gen-go/internal_gengo"
	"github.com/golang/protobuf/v2/internal/detrand"
	"github.com/golang/protobuf/v2/protogen"
	"github.com/golang/protobuf/v2/reflect/protoreflect"
)

func init() {
	// Determine repository root path.
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").CombinedOutput()
	check(err)
	repoRoot = strings.TrimSpace(string(out))

	// Determine the module path.
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Path}}")
	cmd.Dir = repoRoot
	out, err = cmd.CombinedOutput()
	check(err)
	modulePath = strings.TrimSpace(string(out))

	// When the environment variable RUN_AS_PROTOC_PLUGIN is set,
	// we skip running main and instead act as a protoc plugin.
	// This allows the binary to pass itself to protoc.
	if plugins := os.Getenv("RUN_AS_PROTOC_PLUGIN"); plugins != "" {
		// Disable deliberate output instability for generated files.
		// This is reasonable since we fully control the output.
		detrand.Disable()

		protogen.Run(nil, func(gen *protogen.Plugin) error {
			for _, plugin := range strings.Split(plugins, ",") {
				for _, file := range gen.Files {
					if file.Generate {
						switch plugin {
						case "go":
							gengo.GenerateFile(gen, file)
							generateFieldNumbers(gen, file)
						case "gogrpc":
							gengogrpc.GenerateFile(gen, file)
						}
					}
				}
			}
			return nil
		})
		os.Exit(0)
	}
}

var (
	run        bool
	protoRoot  string
	repoRoot   string
	modulePath string

	generatedPreamble = []string{
		"// Copyright 2019 The Go Authors. All rights reserved.",
		"// Use of this source code is governed by a BSD-style.",
		"// license that can be found in the LICENSE file.",
		"",
		"// Code generated by generate-protos. DO NOT EDIT.",
		"",
	}
)

func main() {
	flag.BoolVar(&run, "execute", false, "Write generated files to destination.")
	flag.StringVar(&protoRoot, "protoroot", os.Getenv("PROTOBUF_ROOT"), "The root of the protobuf source tree.")
	flag.Parse()
	if protoRoot == "" {
		panic("protobuf source root is not set")
	}

	generateLocalProtos()
	generateRemoteProtos()
}

func generateLocalProtos() {
	tmpDir, err := ioutil.TempDir(repoRoot, "tmp")
	check(err)
	defer os.RemoveAll(tmpDir)

	// Generate all local proto files (except version-locked files).
	dirs := []struct {
		path        string
		grpcPlugin  bool
		annotateFor map[string]bool
	}{
		{path: "cmd/protoc-gen-go/testdata", annotateFor: map[string]bool{"annotations/annotations.proto": true}},
		{path: "cmd/protoc-gen-go-grpc/testdata", grpcPlugin: true},
		{path: "internal/testprotos"},
		{path: "encoding/testprotos"},
		{path: "reflect/protoregistry/testprotos"},
	}
	semVerRx := regexp.MustCompile(`v[0-9]+\.[0-9]+\.[0-9]+`)
	for _, d := range dirs {
		subDirs := map[string]bool{}

		dstDir := filepath.Join(tmpDir, filepath.FromSlash(d.path))
		check(os.MkdirAll(dstDir, 0775))

		srcDir := filepath.Join(repoRoot, filepath.FromSlash(d.path))
		filepath.Walk(srcDir, func(srcPath string, _ os.FileInfo, _ error) error {
			if !strings.HasSuffix(srcPath, ".proto") || semVerRx.MatchString(srcPath) {
				return nil
			}
			relPath, err := filepath.Rel(srcDir, srcPath)
			check(err)
			subDirs[filepath.Dir(relPath)] = true

			// Emit a .meta file for certain files.
			var opts string
			if d.annotateFor[filepath.ToSlash(relPath)] {
				opts = ",annotate_code"
			}

			// Determine which set of plugins to use.
			plugins := "go"
			if d.grpcPlugin {
				plugins += ",gogrpc"
			}

			protoc(plugins, "-I"+filepath.Join(protoRoot, "src"), "-I"+srcDir, "--go_out=paths=source_relative"+opts+":"+dstDir, relPath)
			return nil
		})

		// For directories in testdata, generate a test that links in all
		// generated packages to ensure that it builds and initializes properly.
		// This is done because "go build ./..." does not build sub-packages
		// under testdata.
		if filepath.Base(d.path) == "testdata" {
			var imports []string
			for sd := range subDirs {
				imports = append(imports, fmt.Sprintf("_ %q", path.Join(modulePath, d.path, filepath.ToSlash(sd))))
			}
			sort.Strings(imports)

			s := strings.Join(append(generatedPreamble, []string{
				"package main",
				"",
				"import (" + strings.Join(imports, "\n") + ")",
			}...), "\n")
			b, err := format.Source([]byte(s))
			check(err)
			check(ioutil.WriteFile(filepath.Join(tmpDir, filepath.FromSlash(d.path+"/gen_test.go")), b, 0664))
		}
	}

	syncOutput(repoRoot, tmpDir)
}

func generateRemoteProtos() {
	tmpDir, err := ioutil.TempDir(repoRoot, "tmp")
	check(err)
	defer os.RemoveAll(tmpDir)

	// Generate all remote proto files.
	files := []struct{ prefix, path string }{
		{"", "conformance/conformance.proto"},
		{"src", "google/protobuf/any.proto"},
		{"src", "google/protobuf/api.proto"},
		{"src", "google/protobuf/descriptor.proto"},
		{"src", "google/protobuf/duration.proto"},
		{"src", "google/protobuf/empty.proto"},
		{"src", "google/protobuf/field_mask.proto"},
		{"src", "google/protobuf/source_context.proto"},
		{"src", "google/protobuf/struct.proto"},
		{"src", "google/protobuf/test_messages_proto2.proto"},
		{"src", "google/protobuf/test_messages_proto3.proto"},
		{"src", "google/protobuf/timestamp.proto"},
		{"src", "google/protobuf/type.proto"},
		{"src", "google/protobuf/wrappers.proto"},
		{"src", "google/protobuf/compiler/plugin.proto"},
	}
	for _, f := range files {
		protoc("go", "-I"+filepath.Join(protoRoot, f.prefix), "--go_out="+tmpDir, f.path)
	}

	syncOutput(repoRoot, filepath.Join(tmpDir, modulePath))
}

func protoc(plugins string, args ...string) {
	cmd := exec.Command("protoc", "--plugin=protoc-gen-go="+os.Args[0])
	cmd.Args = append(cmd.Args, args...)
	cmd.Env = append(os.Environ(), "RUN_AS_PROTOC_PLUGIN="+plugins)
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("executing: %v\n%s\n", strings.Join(cmd.Args, " "), out)
	}
	check(err)
}

// generateFieldNumbers generates an internal package for descriptor.proto
// and well-known types.
func generateFieldNumbers(gen *protogen.Plugin, file *protogen.File) {
	if file.Desc.Package() != "google.protobuf" {
		return
	}

	importPath := modulePath + "/internal/fieldnum"
	base := strings.TrimSuffix(path.Base(file.Desc.Path()), ".proto")
	g := gen.NewGeneratedFile(importPath+"/"+base+"_gen.go", protogen.GoImportPath(importPath))
	for _, s := range generatedPreamble {
		g.P(s)
	}
	g.P("package ", path.Base(importPath))
	g.P("")

	var processMessages func([]*protogen.Message)
	processMessages = func(messages []*protogen.Message) {
		for _, message := range messages {
			g.P("// Field numbers for ", message.Desc.FullName(), ".")
			g.P("const (")
			for _, field := range message.Fields {
				fd := field.Desc
				typeName := fd.Kind().String()
				switch fd.Kind() {
				case protoreflect.EnumKind:
					typeName = string(fd.Enum().FullName())
				case protoreflect.MessageKind, protoreflect.GroupKind:
					typeName = string(fd.Message().FullName())
				}
				g.P(message.GoIdent.GoName, "_", field.GoName, "=", fd.Number(), "// ", fd.Cardinality(), " ", typeName)
			}
			g.P(")")
			processMessages(message.Messages)
		}
	}
	processMessages(file.Messages)
}

func syncOutput(dstDir, srcDir string) {
	filepath.Walk(srcDir, func(srcPath string, _ os.FileInfo, _ error) error {
		if !strings.HasSuffix(srcPath, ".go") && !strings.HasSuffix(srcPath, ".meta") {
			return nil
		}
		relPath, err := filepath.Rel(srcDir, srcPath)
		check(err)
		dstPath := filepath.Join(dstDir, relPath)

		if run {
			fmt.Println("#", relPath)
			b, err := ioutil.ReadFile(srcPath)
			check(err)
			check(os.MkdirAll(filepath.Dir(dstPath), 0775))
			check(ioutil.WriteFile(dstPath, b, 0664))
		} else {
			cmd := exec.Command("diff", dstPath, srcPath, "-N", "-u")
			cmd.Stdout = os.Stdout
			cmd.Run()
		}
		return nil
	})
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
