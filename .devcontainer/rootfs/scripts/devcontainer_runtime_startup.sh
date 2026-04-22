#!/bin/bash

# Check if terraform is already deployed through tfenv
if terraform version >/dev/null 2>&1; then
  echo "Terraform is already deployed through tfenv."
  terraform version
else
  tfenv use 1.11.1
fi
