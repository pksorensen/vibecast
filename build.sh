#!/bin/bash
set -e
cd "$(dirname "$0")"
go build -o vibecast ./main.go
echo "Built vibecast CLI successfully."
