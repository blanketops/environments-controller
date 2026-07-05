//go:build mage

/*
Copyright 2026 The BlanketOps Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
	http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

// Tool versions — bump here to roll the fleet.
const (
	controllerGenVersion = "v0.20.0"
	golangciVersion      = "v2.7.2"
)

// -----------------------------------------------------------------------------
// Tool management
// -----------------------------------------------------------------------------

// localBin returns the absolute path to the local bin directory.
func localBin() string {
	wd, _ := os.Getwd()
	return filepath.Join(wd, "bin")
}

// goInstallTool installs pkg@version into localBin as name-version,
// then symlinks name → name-version. No-ops if already installed.
func goInstallTool(name, pkg, version string) error {
	bin := localBin()
	versioned := filepath.Join(bin, name+"-"+version)
	link := filepath.Join(bin, name)

	if target, err := os.Readlink(link); err == nil && target == versioned {
		if _, err := os.Stat(versioned); err == nil {
			return nil
		}
	}

	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}

	fmt.Printf("Downloading %s@%s\n", pkg, version)
	if err := sh.RunWith(map[string]string{"GOBIN": bin}, "go", "install", pkg+"@"+version); err != nil {
		return err
	}

	plain := filepath.Join(bin, name)
	if err := os.Rename(plain, versioned); err != nil && !os.IsNotExist(err) {
		return err
	}

	_ = os.Remove(link)
	return os.Symlink(versioned, link)
}

func controllerGenBin() (string, error) {
	if err := goInstallTool("controller-gen", "sigs.k8s.io/controller-tools/cmd/controller-gen", controllerGenVersion); err != nil {
		return "", err
	}
	return filepath.Join(localBin(), "controller-gen"), nil
}

func golangciLintBin() (string, error) {
	if err := goInstallTool("golangci-lint", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint", golangciVersion); err != nil {
		return "", err
	}
	return filepath.Join(localBin(), "golangci-lint"), nil
}

// setupEnvtestBin installs setup-envtest pinned to the controller-runtime
// release branch derived from go.mod.
func setupEnvtestBin() (string, error) {
	if err := goInstallTool("setup-envtest", "sigs.k8s.io/controller-runtime/tools/setup-envtest", envtestVersion()); err != nil {
		return "", err
	}
	return filepath.Join(localBin(), "setup-envtest"), nil
}

// envtestVersion reads sigs.k8s.io/controller-runtime from go.mod
// and returns "release-X.Y".
func envtestVersion() string {
	out, err := sh.Output("go", "list", "-m", "-f",
		`{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}`,
		"sigs.k8s.io/controller-runtime",
	)
	if err != nil || out == "" {
		return "latest"
	}
	var major, minor int
	fmt.Sscanf(strings.TrimSpace(out), "v%d.%d", &major, &minor)
	return fmt.Sprintf("release-%d.%d", major, minor)
}

// k8sEnvtestVersion reads k8s.io/api from go.mod and returns "1.NN".
func k8sEnvtestVersion() string {
	out, err := sh.Output("go", "list", "-m", "-f",
		`{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}`,
		"k8s.io/api",
	)
	if err != nil || out == "" {
		return "1.31"
	}
	var major, minor int
	fmt.Sscanf(strings.TrimSpace(out), "v%d.%d", &major, &minor)
	return fmt.Sprintf("1.%d", minor)
}

// -----------------------------------------------------------------------------
// General
// -----------------------------------------------------------------------------

// Help lists all available targets.
func Help() error {
	return sh.RunV("mage", "-l")
}

// -----------------------------------------------------------------------------
// Code generation
// -----------------------------------------------------------------------------

// Manifests generates the ClusterRole from kubebuilder RBAC markers
// into config/rbac. The install repo syncs role.yaml from here —
// this path is a cross-repo contract; do not move it.
func Manifests() error {
	cgen, err := controllerGenBin()
	if err != nil {
		return err
	}
	return sh.Run(cgen,
		"rbac:roleName=manager-role",
		"paths=./...",
		"output:rbac:artifacts:config=config/rbac",
	)
}

// Generate generates DeepCopy, DeepCopyInto, and DeepCopyObject implementations.
func Generate() error {
	cgen, err := controllerGenBin()
	if err != nil {
		return err
	}
	return sh.Run(cgen,
		"object:headerFile=hack/boilerplate.go.txt",
		"paths=./...",
	)
}

// -----------------------------------------------------------------------------
// Code quality
// -----------------------------------------------------------------------------

// Fmt runs go fmt against all packages.
func Fmt() error {
	return sh.Run("go", "fmt", "./...")
}

// Vet runs go vet against all packages.
func Vet() error {
	return sh.Run("go", "vet", "./...")
}

// Lint runs golangci-lint.
func Lint() error {
	lint, err := golangciLintBin()
	if err != nil {
		return err
	}
	return sh.Run(lint, "run")
}

// LintFix runs golangci-lint and applies fixes.
func LintFix() error {
	lint, err := golangciLintBin()
	if err != nil {
		return err
	}
	return sh.Run(lint, "run", "--fix")
}

// LintConfig verifies the golangci-lint configuration.
func LintConfig() error {
	lint, err := golangciLintBin()
	if err != nil {
		return err
	}
	return sh.Run(lint, "config", "verify")
}

// -----------------------------------------------------------------------------
// Build
// -----------------------------------------------------------------------------

// Build builds the manager binary into bin/manager.
func Build() error {
	mg.Deps(Manifests, Generate, Fmt, Vet)
	return sh.Run("go", "build", "-o", "bin/manager", "cmd/main.go")
}

// Run runs the controller directly from source.
func Run() error {
	mg.Deps(Manifests, Generate, Fmt, Vet)
	return sh.Run("go", "run", "./cmd/main.go")
}

// -----------------------------------------------------------------------------
// Test
// -----------------------------------------------------------------------------

// Test runs the unit test suite.
func Test() error {
	mg.Deps(Manifests, Generate, Fmt, Vet)

	envtestBin, err := setupEnvtestBin()
	if err != nil {
		return err
	}

	k8sVer := k8sEnvtestVersion()
	assetsPath, err := sh.Output(envtestBin, "use", k8sVer, "--bin-dir", localBin(), "-p", "path")
	if err != nil {
		return fmt.Errorf("setup-envtest: %w", err)
	}

	return sh.RunWith(
		map[string]string{"KUBEBUILDER_ASSETS": strings.TrimSpace(assetsPath)},
		"go", "test", "-coverprofile=cover.out", "./internal/...", "./cmd/...",
	)
}

// -----------------------------------------------------------------------------
// Image
// -----------------------------------------------------------------------------

func img() string {
	if v := os.Getenv("IMG"); v != "" {
		return v
	}
	return "controller:latest"
}

func containerTool() string {
	if v := os.Getenv("CONTAINER_TOOL"); v != "" {
		return v
	}
	return "docker"
}

// DockerBuild builds the manager image.
func DockerBuild() error {
	return sh.Run(containerTool(), "build", "-t", img(), ".")
}

// DockerPush pushes the manager image.
func DockerPush() error {
	return sh.Run(containerTool(), "push", img())
}

// -----------------------------------------------------------------------------
// Clean
// -----------------------------------------------------------------------------

// Clean removes build artifacts.
func Clean() error {
	_ = os.Remove("bin/manager")
	_ = os.Remove("cover.out")
	return nil
}
