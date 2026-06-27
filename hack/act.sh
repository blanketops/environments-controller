# Test the pull_request trigger
act pull_request --secret-file .secrets

# Test the push trigger (act defaults to push, but be explicit)
act push --secret-file .secrets

# Run only the build job
act pull_request --job build --secret-file .secrets

# Use a larger image if the default is too minimal (Go tooling needs more than the slim image)
act pull_request --secret-file .secrets -P ubuntu-latest=catthehacker/ubuntu:act-latest