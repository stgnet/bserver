#!/usr/bin/env bash
#
# Forwarding stub: the real script now lives in scripts/install-service.sh.
#
# This file must stay. Servers still running the pre-scripts/ copy of
# install-service.sh will, during `update`, `git pull` this change and then
# exec "<repo>/install-service.sh rebuild" (the path they knew about). Without
# this stub that exec would fail and leave the server on a stale binary.
# It also keeps the old `sudo ./install-service.sh` habit working.
exec "$(dirname "$0")/scripts/install-service.sh" "$@"
