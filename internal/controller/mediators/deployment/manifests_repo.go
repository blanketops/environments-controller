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

	utils "github.com/ntlaletsi70/blanketops-environments/pkg/utils"
	deploymentResolution "github.com/ntlaletsi70/blanketops-environments/resolution/deployment"
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

	log := m.Log.WithValues(
		"deployment", deploy.Name,
		"namespace", deploy.Namespace,
	)

	if spec.ManifestsRepo == nil {
		log.V(1).Info("Manifests repository not requested, skipping bootstrap")
		return nil
	}

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN must be set for private repo creation")
	}

	owner := spec.GitOwner
	repo := fmt.Sprintf("%s-manifests", deploy.Name)
	branch := "main"

	sshURL := fmt.Sprintf("ssh://git@github.com/%s/%s.git", owner, repo)
	localPath := filepath.Join(os.TempDir(), repo)

	log.Info("Starting manifests bootstrap", "repo", sshURL)

	m.event(
		deploy,
		corev1.EventTypeNormal,
		"BootstrapStarted",
		fmt.Sprintf("Bootstrapping manifests repo %s/%s", owner, repo),
	)

	//------------------------------------------------
	// Ensure GitHub repository exists
	//------------------------------------------------

	log.Info("Ensuring GitHub repository exists")

	if err := ensureGitHubRepoPrivate(owner, repo, token); err != nil {

		log.Error(err, "Failed ensuring GitHub repository")

		m.event(
			deploy,
			corev1.EventTypeWarning,
			"RepoEnsureFailed",
			err.Error(),
		)

		return fmt.Errorf("ensure remote repo: %w", err)
	}

	m.event(
		deploy,
		corev1.EventTypeNormal,
		"RepoEnsured",
		"GitHub repository ensured",
	)

	//------------------------------------------------
	// Ensure deploy key
	//------------------------------------------------

	log.Info("Ensuring deploy key")

	publicKey, err := m.extractPublicKey(ctx, resolved)
	if err != nil {
		return err
	}

	if err := ensureDeployKey(owner, repo, publicKey, token); err != nil {

		log.Error(err, "Failed ensuring deploy key")

		m.event(
			deploy,
			corev1.EventTypeWarning,
			"DeployKeyFailed",
			err.Error(),
		)

		return fmt.Errorf("ensure deploy key: %w", err)
	}

	m.event(
		deploy,
		corev1.EventTypeNormal,
		"DeployKeyEnsured",
		"Deploy key registered",
	)

	//------------------------------------------------
	// SSH key for git operations
	//------------------------------------------------

	sshKeyPath, cleanup, err := m.writeSSHKeyToDisk(ctx, resolved)
	if err != nil {
		return err
	}

	defer cleanup()

	gitSSHCmd := fmt.Sprintf(
		`ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null`,
		sshKeyPath,
	)

	//------------------------------------------------
	// Clone or initialize repository
	//------------------------------------------------

	gitDir := filepath.Join(localPath, ".git")

	if _, err := os.Stat(gitDir); os.IsNotExist(err) {

		log.Info("Cloning repository", "repo", sshURL)

		if _, err := utils.RunGitWithEnv(
			"",
			[]string{"GIT_SSH_COMMAND=" + gitSSHCmd},
			"clone", sshURL, localPath,
		); err != nil {

			log.Info("Remote repo empty, initializing local repo")

			if err := os.MkdirAll(localPath, 0755); err != nil {
				return err
			}

			utils.RunGit(localPath, "init")
			utils.RunGit(localPath, "checkout", "-b", branch)
			utils.RunGit(localPath, "remote", "add", "origin", sshURL)
		}

	} else {

		log.Info("Local repo exists, syncing")

		utils.RunGitWithEnv(localPath,
			[]string{"GIT_SSH_COMMAND=" + gitSSHCmd},
			"fetch", "--all",
		)

		utils.RunGit(localPath, "checkout", branch)

		utils.RunGitWithEnv(localPath,
			[]string{"GIT_SSH_COMMAND=" + gitSSHCmd},
			"pull", "--ff-only", "origin", branch,
		)
	}

	//------------------------------------------------
	// Ensure base + overlays
	//------------------------------------------------

	log.Info("Ensuring base manifests and overlays")

	basePath := filepath.Join(localPath, "base", "manifests")

	if err := os.MkdirAll(basePath, 0755); err != nil {
		return err
	}

	baseKust := filepath.Join(basePath, "kustomization.yaml")

	if _, err := os.Stat(baseKust); os.IsNotExist(err) {

		os.WriteFile(baseKust, []byte(
			"apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n",
		), 0644)
	}

	for _, env := range overlayEnvs {

		overlayPath := filepath.Join(localPath, "overlays", env)

		os.MkdirAll(overlayPath, 0755)

		kustFile := filepath.Join(overlayPath, "kustomization.yaml")

		if _, err := os.Stat(kustFile); os.IsNotExist(err) {

			os.WriteFile(kustFile, []byte(
				"apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ../../base/manifests\n  - environment.yaml\n",
			), 0644)
		}

		envFile := filepath.Join(overlayPath, "environment.yaml")

		if _, err := os.Stat(envFile); os.IsNotExist(err) {

			os.WriteFile(envFile, []byte(fmt.Sprintf(
				"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\ndata:\n  ENV: %s\n",
				deploy.Name, env,
			)), 0644)
		}
	}

	//------------------------------------------------
	// Commit & push
	//------------------------------------------------

	utils.RunGit(localPath, "add", ".")

	if _, err := utils.RunGit(localPath, "diff", "--cached", "--quiet"); err != nil {

		utils.RunGit(localPath, "config", "user.email", "ntlaletsi86@gmail.com")
		utils.RunGit(localPath, "config", "user.name", "BlanketOps")

		utils.RunGit(localPath, "commit", "-m", "bootstrap: ensure base and overlays")
	}

	log.Info("Pushing manifests repository")

	if out, err := utils.RunGitWithEnv(
		localPath,
		[]string{"GIT_SSH_COMMAND=" + gitSSHCmd},
		"push", "-u", "origin", branch,
	); err != nil {

		log.Error(err, "Git push failed", "output", out)

		m.event(
			deploy,
			corev1.EventTypeWarning,
			"GitPushFailed",
			string(out),
		)

		return fmt.Errorf("git push failed: %s", out)
	}

	m.event(
		deploy,
		corev1.EventTypeNormal,
		"BootstrapComplete",
		"Manifests repository bootstrapped",
	)

	log.Info("Manifests bootstrap complete", "repo", sshURL)

	return nil
}

func (m *Mediator) event(obj client.Object, eventType, reason, message string) {
	if m.Recorder != nil {
		m.Recorder.Event(obj, eventType, reason, message)
	}
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
	if err != nil {
		return "", nil, err
	}
	if _, err := f.Write(secret.Data["identity"]); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	f.Close()
	// SSH requires 0600
	if err := os.Chmod(f.Name(), 0600); err != nil {
		os.Remove(f.Name())
		return "", nil, err
	}

	cleanup := func() { os.Remove(f.Name()) }
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
	defer resp.Body.Close()

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
		return fmt.Errorf("add deploy key: %w", err)
	}
	defer addResp.Body.Close()

	if addResp.StatusCode != http.StatusCreated {
		var buf bytes.Buffer
		buf.ReadFrom(addResp.Body)
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
	defer resp.Body.Close()

	switch resp.StatusCode {
	case 201:
		fmt.Printf("[bootstrap] remote private repo created: %s/%s\n", owner, repo)
	case 422:
		fmt.Printf("[bootstrap] remote repo already exists: %s/%s\n", owner, repo)
	case 401, 403:
		return fmt.Errorf("unauthorized: check GITHUB_TOKEN permissions")
	default:
		return fmt.Errorf("failed to create repo, status: %d", resp.StatusCode)
	}
	return nil
}
