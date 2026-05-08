#!/bin/bash

# Check if terraform is already deployed through tfenv
if terraform version >/dev/null 2>&1; then
  echo "Terraform is already deployed through tfenv."
  terraform version
else
  tfenv use 1.11.1
fi

# Notify about dev binaries deployed to /AI-evo1-dev/bin
if [ -d /AI-evo1-dev/bin ] && [ -n "$(ls -A /AI-evo1-dev/bin 2>/dev/null)" ]; then
  echo "--- DEV BINARIES ACTIVE (/AI-evo1-dev/bin) ---"
  ls /AI-evo1-dev/bin | while read -r bin; do echo "  $bin"; done
  echo "-----------------------------------------------"
fi
