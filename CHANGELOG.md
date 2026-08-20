## [0.4.3] - 2026-08-20

### 🐛 Bug Fixes

- *(lint)* Replace deprecated Result.Requeue with RequeueAfter
- *(templates)* Move issue templates to the path GitHub actually reads
- *(templates)* Remove Contract Change issue type -- doesn't belong here
- *(lint)* Replace deprecated Result.Requeue with RequeueAfter (deployment.go)

### 💼 Other

- Merge release/v0.4.3 into main

### 📚 Documentation

- Refresh generated code docs via gomarkdoc

### ⚙️ Miscellaneous Tasks

- *(release)* Update changelog for v0.4.2
- Drop unnecessary App-token auth from read-only build/scan jobs
- *(ci)* Bump actions/upload-artifact from 4 to 7
- *(ci)* Bump softprops/action-gh-release from 2 to 3
- *(ci)* Bump actions/create-github-app-token from 2 to 3
- *(ci)* Bump actions/checkout from 4 to 7
- *(ci)* Bump sigstore/cosign-installer from 3.7.0 to 4.1.2
- Bring lint fix + template fixes into main
- Bring deployment.go lint fix into main
## [0.4.2] - 2026-08-20

### 🚀 Features

- *(networks)* Register Route and Domain reconcilers

### 🐛 Bug Fixes

- Gofmt import ordering, rewrite README negatives as positives

### 📚 Documentation

- Fix stale "migrating to" phrasing in README
- Auto-generate code documentation [skip ci]

### ⚙️ Miscellaneous Tasks

- *(release)* Update changelog for v0.4.1
- Sync develop with main after release/v0.4.1
- Add dependabot.yml (gomod, github-actions)
- Bring dependabot.yml into main (Dependabot reads config from default branch)
- Bring gofmt fix + README rewrite into main
## [0.4.1] - 2026-08-19

### 💼 Other

- Merge release/v0.4.1 into main

### 📚 Documentation

- Auto-generate code documentation [skip ci]

### ⚙️ Miscellaneous Tasks

- Sync develop with main after release/v0.4.0
- *(release)* Update changelog for v0.4.0
- Add code docs generation and richer coverage reporting
- Sync develop with main (code docs + coverage workflows)
- Sync develop with main (dependency bump)
## [0.4.0] - 2026-07-19

### 🐛 Bug Fixes

- Update imports for environments v0.7.5's package reorg

### 💼 Other

- Fix golangci-lint findings surfaced now that CI auth actually works
- Fix golangci-lint findings surfaced now that CI auth actually works
- Merge release/v0.4.0 into main

### 🧪 Testing

- Add domain layer coverage, fix .gitignore and two nil-pointer bugs found along the way
- Add mediator and cache layer coverage

### ⚙️ Miscellaneous Tasks

- Sync develop with main after release/v0.3.9
- Migrate all workflows off the retired GH_PAT secret, consolidate security scans
- *(release)* Update changelog for v0.3.9
- Migrate all workflows off the retired GH_PAT secret, consolidate security scans
- Sync develop with main before cutting release v0.4.0
## [0.3.9] - 2026-07-16

### 🐛 Bug Fixes

- Register readyz health check

### ⚙️ Miscellaneous Tasks

- *(release)* Update changelog for v0.3.8
- Sync develop with main after release/v0.3.8
## [0.3.8] - 2026-07-15

### ⚙️ Miscellaneous Tasks

- Reconcile develop with squashed main history
