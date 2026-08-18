#!/bin/sh
# Sourced by the VHS tape: point the shell at the synthetic demo world.
DEMO="${DEMO_ROOT:-/tmp/sp-demo}"
export HOME="$DEMO/home"
export CODEX_HOME="$HOME/.codex"
export SESSION_PROTECT_CONFIG="$DEMO/config.toml"
export PATH="$DEMO/bin:$PATH"
export GIT_AUTHOR_NAME=demo GIT_AUTHOR_EMAIL=demo@example.invalid
export GIT_COMMITTER_NAME=demo GIT_COMMITTER_EMAIL=demo@example.invalid
cd "$HOME/projects"
