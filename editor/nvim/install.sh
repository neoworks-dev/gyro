#!/bin/sh
# Install Gyro editor support into a Neovim config: compiles both
# tree-sitter parsers into <config>/parser/ and symlinks the query
# directories into <config>/queries/. Idempotent; rerun after grammar
# changes.
#
# Usage: editor/nvim/install.sh [nvim-config-dir]   (default ~/.config/nvim)

set -eu

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
config_dir=${1:-"$HOME/.config/nvim"}
compiler=${CC:-cc}

mkdir -p "$config_dir/parser" "$config_dir/queries"

"$compiler" -shared -fPIC -O2 \
  -I "$repo_root/tree-sitter-gyro/src" \
  "$repo_root/tree-sitter-gyro/src/parser.c" \
  "$repo_root/tree-sitter-gyro/src/scanner.c" \
  -o "$config_dir/parser/gyro.so"

"$compiler" -shared -fPIC -O2 \
  -I "$repo_root/tree-sitter-metals/src" \
  "$repo_root/tree-sitter-metals/src/parser.c" \
  -o "$config_dir/parser/metals.so"

ln -sfn "$repo_root/tree-sitter-gyro/queries" "$config_dir/queries/gyro"
ln -sfn "$repo_root/tree-sitter-metals/queries" "$config_dir/queries/metals"

echo "installed: parser/gyro.so parser/metals.so queries/gyro queries/metals"
echo "plugin spec: copy editor/nvim/gyro.lua into $config_dir/lua/plugins/"
