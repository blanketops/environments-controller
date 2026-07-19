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
package deployment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	utils "github.com/blanketops/environments/pkg/utils"
	deploymentResolution "github.com/blanketops/environments/resolution/deployment/resolve"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var overlayEnvs = []string{"dev", "int", "qa", "prd", "staging", "jumpbox", "sandbox"}

func (m *Mediator) ensureManifestsRepo(
	ctx context.Context,
	resolved *deploymentResolution.ResolvedDeployment,
) error {
	if resolved == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedDeployment (resolver bug)")
	}
	deploy := resolved.Deployment
	spec := resolved.Spec
	if spec.ManifestsRepo == nil {
		return nil
	}
	token := os.Getenv("GH_PAT")
	if token == "" {
		return fmt.Errorf("GH_PAT must be set for private repo creation")
	}
	owner := resolved.Spec.GitOwner
	repo := fmt.Sprintf("%s-manifests", deploy.Name)
	sshURL := fmt.Sprintf("ssh://git@github.com/%s/%s.git", owner, repo)
	localPath := filepath.Join(os.TempDir(), repo)
	branch := "main"
	fmt.Printf("[bootstrap] starting manifests bootstrap for %s/%s\n", owner, repo)
	// ------------------------------------------------
	// 1. Ensure remote private repo exists
	// ------------------------------------------------
	if err := ensureGitHubRepoPrivate(owner, repo, token); err != nil {
		return fmt.Errorf("ensure remote repo: %w", err)
	}
	// ------------------------------------------------
	// 2. Ensure deploy key is registered on GitHub
	//    MUST happen before any git operation
	// ------------------------------------------------
	publicKey, err := m.extractPublicKey(ctx, resolved)
	if err != nil {
		return fmt.Errorf("extract public key: %w", err)
	}
	if err := ensureDeployKey(owner, repo, publicKey, token); err != nil {
		return fmt.Errorf("ensure deploy key: %w", err)
	}
	// ------------------------------------------------
	// 3. Write ephemeral SSH key to disk for git ops
	// ------------------------------------------------
	sshKeyPath, cleanup, err := m.writeSSHKeyToDisk(ctx, resolved)
	if err != nil {
		return fmt.Errorf("write ssh key: %w", err)
	}
	defer cleanup()
	gitSSHCmd := fmt.Sprintf(
		`ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null`,
		sshKeyPath,
	)
	// ------------------------------------------------
	// 4. Clone or initialize local repo
	// ------------------------------------------------
	gitDir := filepath.Join(localPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		fmt.Printf("[bootstrap] cloning remote repo %s -> %s\n", sshURL, localPath)
		if _, err := utils.RunGitWithEnv(
			"", []string{"GIT_SSH_COMMAND=" + gitSSHCmd},
			"clone", sshURL, localPath,
		); err != nil {
			fmt.Printf("[bootstrap] remote empty, initializing new repo\n")
			if err := os.MkdirAll(localPath, 0755); err != nil {
				return fmt.Errorf("mkdir failed: %w", err)
			}
			_, err = utils.RunGit(localPath, "init")
			if err != nil {
				return err
			}
			_, err = utils.RunGit(localPath, "checkout", "-b", branch)
			if err != nil {
				return err
			}
			_, err = utils.RunGit(localPath, "remote", "add", "origin", sshURL)
			if err != nil {
				return err
			}
		}
	} else {
		fmt.Printf("[bootstrap] local path exists, syncing remote\n")
		_, err = utils.RunGitWithEnv(localPath,
			[]string{"GIT_SSH_COMMAND=" + gitSSHCmd},
			"fetch", "--all",
		)
		if err != nil {
			return err
		}
		_, err = utils.RunGit(localPath, "checkout", branch)
		if err != nil {
			return err
		}
		_, err = utils.RunGitWithEnv(localPath,
			[]string{"GIT_SSH_COMMAND=" + gitSSHCmd},
			"pull", "--ff-only", "origin", branch,
		)
		if err != nil {
			return err
		}
	}
	// ------------------------------------------------
	// 5. README
	// ------------------------------------------------
	readme := filepath.Join(localPath, "README.md")
	if _, err := os.Stat(readme); os.IsNotExist(err) {
		readmeContent := fmt.Sprintf("# %s\n\nManaged by BlanketOps.\n", repo)
		if err := os.WriteFile(readme, []byte(readmeContent), 0644); err != nil {
			return fmt.Errorf("write README.md: %w", err)
		}
	}
	// ------------------------------------------------
	// 6. Ensure base + overlays
	// ------------------------------------------------
	basePath := filepath.Join(localPath, "base", "manifests")
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return fmt.Errorf("mkdir base failed: %w", err)
	}
	baseKust := filepath.Join(basePath, "kustomization.yaml")
	if _, err := os.Stat(baseKust); os.IsNotExist(err) {
		if err := os.WriteFile(baseKust, []byte(
			"apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n",
		), 0644); err != nil {
			return fmt.Errorf("write base kustomization.yaml: %w", err)
		}
	}
	for _, env := range overlayEnvs {
		overlayPath := filepath.Join(localPath, "overlays", env)
		if err := os.MkdirAll(overlayPath, 0755); err != nil {
			return fmt.Errorf("mkdir overlay failed: %w", err)
		}
		kustFile := filepath.Join(overlayPath, "kustomization.yaml")
		if _, err := os.Stat(kustFile); os.IsNotExist(err) {
			if err := os.WriteFile(kustFile, []byte(
				"apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ../../base/manifests\n  - environment.yaml\n",
			), 0644); err != nil {
				return fmt.Errorf("write overlay kustomization.yaml: %w", err)
			}
		}
		envFile := filepath.Join(overlayPath, "environment.yaml")
		if _, err := os.Stat(envFile); os.IsNotExist(err) {
			if err := os.WriteFile(envFile, fmt.Appendf(nil,
				"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\ndata:\n  ENV: %s\n",
				deploy.Name, env,
			), 0644); err != nil {
				return fmt.Errorf("write environment.yaml: %w", err)
			}
		}
	}
	// ------------------------------------------------
	// 7. Commit & push
	// ------------------------------------------------
	_, err = utils.RunGit(localPath, "add", ".")
	if err != nil {
		return err
	}
	if _, err := utils.RunGit(localPath, "diff", "--cached", "--quiet"); err != nil {
		_, err = utils.RunGit(localPath, "config", "user.email", "ntlaletsi86@gmail.com")
		if err != nil {
			return err
		}
		_, err = utils.RunGit(localPath, "config", "user.name", "Neo Tlaletsi")
		if err != nil {
			return err
		}
		_, err = utils.RunGit(localPath, "commit", "-m", "bootstrap: ensure base and overlays")
		if err != nil {
			return err
		}
	}
	if out, err := utils.RunGitWithEnv(
		localPath,
		[]string{"GIT_SSH_COMMAND=" + gitSSHCmd},
		"push", "-u", "origin", branch,
	); err != nil {
		return fmt.Errorf("git push failed: %s", out)
	}
	fmt.Printf("[bootstrap] repository bootstrap complete: %s\n", sshURL)
	return nil
}

// teardownManifestsRepo reverses ensureManifestsRepo — deletes the remote
// GitOps manifests repository and the local working clone. The deploy key
// registered on the repository is removed implicitly with the repository
// itself. The manifests repository is declared into existence by the
// Deployment CR, so its lifecycle is bound to the CR: teardown is total.
// A repository that is already gone is not an error.
func (m *Mediator) teardownManifestsRepo(resolved *deploymentResolution.ResolvedDeployment) error {
	if resolved == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedDeployment (resolver bug)")
	}
	deploy := resolved.Deployment
	if resolved.Spec.ManifestsRepo == nil {
		return nil
	}
	token := os.Getenv("GH_PAT")
	if token == "" {
		return fmt.Errorf("GH_PAT must be set for repo deletion")
	}
	owner := resolved.Spec.GitOwner
	repo := fmt.Sprintf("%s-manifests", deploy.Name)
	localPath := filepath.Join(os.TempDir(), repo)
	fmt.Printf("[teardown] removing manifests repo %s/%s\n", owner, repo)
	// ------------------------------------------------
	// 1. Delete remote repository
	// ------------------------------------------------
	if err := deleteGitHubRepo(owner, repo, token); err != nil {
		return fmt.Errorf("delete remote repo: %w", err)
	}
	// ------------------------------------------------
	// 2. Remove local working clone
	// ------------------------------------------------
	if err := os.RemoveAll(localPath); err != nil {
		return fmt.Errorf("remove local clone: %w", err)
	}
	fmt.Printf("[teardown] manifests repo removed: %s/%s\n", owner, repo)
	return nil
}

// extractPublicKey reads identity.pub from the flux ssh secret.
func (m *Mediator) extractPublicKey(
	ctx context.Context,
	resolved *deploymentResolution.ResolvedDeployment,
) (string, error) {
	secretName := fmt.Sprintf("%s-flux-ssh", resolved.Deployment.Name)
	var secret corev1.Secret
	if err := m.Client.Get(ctx, client.ObjectKey{
		Name:      secretName,
		Namespace: resolved.Deployment.Namespace,
	}, &secret); err != nil {
		return "", fmt.Errorf("get flux ssh secret: %w", err)
	}
	// Prefer pre-stored identity.pub
	if pub, ok := secret.Data["identity.pub"]; ok {
		return string(pub), nil
	}
	// Fall back: derive from private key
	signer, err := ssh.ParsePrivateKey(secret.Data["identity"])
	if err != nil {
		return "", fmt.Errorf("parse private key: %w", err)
	}
	return string(ssh.MarshalAuthorizedKey(signer.PublicKey())), nil
}

// writeSSHKeyToDisk writes the private key to a temp file for git CLI use.
// Returns the file path and a cleanup func.
func (m *Mediator) writeSSHKeyToDisk(
	ctx context.Context,
	resolved *deploymentResolution.ResolvedDeployment,
) (string, func(), error) {
	secretName := fmt.Sprintf("%s-flux-ssh", resolved.Deployment.Name)
	var secret corev1.Secret
	if err := m.Client.Get(ctx, client.ObjectKey{
		Name:      secretName,
		Namespace: resolved.Deployment.Namespace,
	}, &secret); err != nil {
		return "", nil, fmt.Errorf("get flux ssh secret: %w", err)
	}
	f, err := os.CreateTemp("", "blanketops-ssh-*")
	if nil != err {
		return "", nil, err
	}
	if _, err := f.Write(secret.Data["identity"]); err != nil {
		err = f.Close()
		if err != nil {
			return "", nil, err
		}
		err = os.Remove(f.Name())
		if err != nil {
			return "", nil, err
		}
		return "", nil, err
	}
	err = f.Close()
	if err != nil {
		return "", nil, err
	}
	// SSH requires 0600
	if err := os.Chmod(f.Name(), 0600); err != nil {
		err = os.Remove(f.Name())
		if err != nil {
			return "", nil, err
		}
		return "", nil, err
	}
	cleanup := func() {
		err = os.Remove(f.Name())
		if err != nil {
			panic(err)
		}
	}
	return f.Name(), cleanup, nil
}

// ensureDeployKey registers the public key on the GitHub repo if not already present.
// Uses wire-format comparison so encoding differences don't cause false mismatches.
func ensureDeployKey(owner, repo, publicKey, token string) error {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/keys", owner, repo)
	// Parse our key to wire bytes for reliable comparison
	wantPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil {
		return fmt.Errorf("parse our public key: %w", err)
	}
	wantBytes := wantPub.Marshal()
	// List existing keys
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("list deploy keys: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("list deploy keys: status %d", resp.StatusCode)
	}
	var existing []struct {
		ID  int64  `json:"id"`
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&existing); err != nil {
		return fmt.Errorf("decode deploy keys: %w", err)
	}
	for _, k := range existing {
		gotPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.Key))
		if err != nil {
			continue
		}
		if bytes.Equal(gotPub.Marshal(), wantBytes) {
			fmt.Printf("[bootstrap] deploy key already present (id=%d)\n", k.ID)
			return nil
		}
	}
	// Register the key
	payload := map[string]any{
		"title":     "flux-" + repo,
		"key":       strings.TrimSpace(publicKey),
		"read_only": false,
	}
	body, _ := json.Marshal(payload)
	addReq, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	addReq.Header.Set("Authorization", "token "+token)
	addReq.Header.Set("Accept", "application/vnd.github+json")
	addReq.Header.Set("Content-Type", "application/json")
	addResp, err := http.DefaultClient.Do(addReq)
	if err != nil {
		return err
	}
	defer func() { _ = addResp.Body.Close() }()
	if addResp.StatusCode != http.StatusCreated {
		var buf bytes.Buffer
		_, err = buf.ReadFrom(addResp.Body)
		if err != nil {
			return err
		}
		return fmt.Errorf("failed to add deploy key: %d - %s", addResp.StatusCode, buf.String())
	}
	fmt.Printf("[bootstrap] deploy key added for %s/%s\n", owner, repo)
	return nil
}

func ensureGitHubRepoPrivate(owner, repo, token string) error {
	payload, _ := json.Marshal(map[string]any{
		"name":    repo,
		"private": true,
	})
	req, _ := http.NewRequest("POST",
		"https://api.github.com/user/repos",
		bytes.NewBuffer(payload),
	)
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("create repo request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case 201:
		fmt.Printf("[bootstrap] remote private repo created: %s/%s\n", owner, repo)
	case 422:
		fmt.Printf("[bootstrap] remote repo already exists: %s/%s\n", owner, repo)
	case 401, 403:
		return fmt.Errorf("unauthorized: check GH_PAT permissions")
	default:
		return fmt.Errorf("failed to create repo, status: %d", resp.StatusCode)
	}
	return nil
}

// deleteGitHubRepo deletes the repository on GitHub. Idempotent — a 404
// means the repository is already gone and is treated as success.
// Requires the delete_repo scope on GH_PAT — repo alone does not
// grant deletion.
func deleteGitHubRepo(owner, repo, token string) error {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)
	req, err := http.NewRequest("DELETE", apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete repo request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNoContent:
		fmt.Printf("[teardown] remote repo deleted: %s/%s\n", owner, repo)
		return nil
	case http.StatusNotFound:
		fmt.Printf("[teardown] remote repo already gone: %s/%s\n", owner, repo)
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("unauthorized: GH_PAT requires the delete_repo scope")
	default:
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return fmt.Errorf("failed to delete repo: %d - %s", resp.StatusCode, buf.String())
	}
}
