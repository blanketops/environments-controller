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
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

// Tool versions — bump here to roll the fleet.
const (
	kustomizeVersion     = "v5.7.1"
	controllerGenVersion = "v0.20.0"
	golangciVersion      = "v2.7.2"
)

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

func kustomizeBin() (string, error) {
	if err := goInstallTool("kustomize", "sigs.k8s.io/kustomize/kustomize/v5", kustomizeVersion); err != nil {
		return "", err
	}
	return filepath.Join(localBin(), "kustomize"), nil
}

func golangciLintBin() (string, error) {
	if err := goInstallTool("golangci-lint", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint", golangciVersion); err != nil {
		return "", err
	}
	return filepath.Join(localBin(), "golangci-lint"), nil
}

// -----------------------------------------------------------------------------
// Code generation
// -----------------------------------------------------------------------------

// Manifests generates WebhookConfiguration, ClusterRole, and CRD objects.
func Manifests() error {
	cgen, err := controllerGenBin()
	if err != nil {
		return err
	}
	return sh.Run(cgen,
		"rbac:roleName=manager-role", "crd", "webhook",
		"paths=./...",
		"output:crd:artifacts:config=config/crd/bases",
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

// Test runs the unit test suite (excludes e2e).
func Test() error {
	mg.Deps(Manifests, Generate, Fmt, Vet)

	k8sVer := k8sEnvtestVersion()
	envtestBin := filepath.Join(localBin(), "setup-envtest")
	assetsPath, err := sh.Output(envtestBin, "use", k8sVer, "--bin-dir", localBin(), "-p", "path")
	if err != nil {
		return fmt.Errorf("setup-envtest: %w", err)
	}

	return sh.RunWith(
		map[string]string{"KUBEBUILDER_ASSETS": strings.TrimSpace(assetsPath)},
		"go", "test", "-coverprofile=cover.out", "./internal/...", "./cmd/...",
	)
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
// Docker
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
// Cluster
// -----------------------------------------------------------------------------

// Install installs CRDs into the cluster.
func Install() error {
	mg.Deps(Manifests)
	kust, err := kustomizeBin()
	if err != nil {
		return err
	}
	out, err := sh.Output(kust, "build", "config/crd")
	if err != nil || strings.TrimSpace(out) == "" {
		fmt.Println("No CRDs to install; skipping.")
		return nil
	}
	return pipeToKubectl(out, "apply", "-f", "-")
}

// Uninstall removes CRDs from the cluster.
func Uninstall() error {
	mg.Deps(Manifests)
	kust, err := kustomizeBin()
	if err != nil {
		return err
	}
	out, err := sh.Output(kust, "build", "config/crd")
	if err != nil || strings.TrimSpace(out) == "" {
		fmt.Println("No CRDs to delete; skipping.")
		return nil
	}
	return pipeToKubectl(out, "delete", "--ignore-not-found=true", "-f", "-")
}

// Deploy deploys the controller to the cluster.
func Deploy() error {
	mg.Deps(Manifests)
	kust, err := kustomizeBin()
	if err != nil {
		return err
	}
	// patch image tag in config/manager
	_ = sh.Run(kust, "-C", "config/manager", "edit", "set", "image", "controller="+img())
	out, err := sh.Output(kust, "build", "config/default")
	if err != nil {
		return err
	}
	return pipeToKubectl(out, "apply", "-f", "-")
}

// Undeploy removes the controller from the cluster.
func Undeploy() error {
	kust, err := kustomizeBin()
	if err != nil {
		return err
	}
	out, err := sh.Output(kust, "build", "config/default")
	if err != nil {
		return err
	}
	return pipeToKubectl(out, "delete", "--ignore-not-found=true", "-f", "-")
}

// pipeToKubectl pipes a YAML string into kubectl via stdin.
func pipeToKubectl(yaml string, args ...string) error {
	cmd := exec.Command("kubectl", args...)
	cmd.Stdin = strings.NewReader(yaml)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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
