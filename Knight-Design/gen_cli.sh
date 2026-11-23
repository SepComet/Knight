#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
PROJECT_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)
ASSET_ROOT="$PROJECT_ROOT/Knight-Client/Textures"
PATH_FILE="$SCRIPT_DIR/path.txt"
LUBAN_DLL="$SCRIPT_DIR/Tools/Luban/Luban.dll"
CONF_FILE="$SCRIPT_DIR/luban.conf"
CUSTOM_TEMPLATE_DIR="$SCRIPT_DIR/CustomTemplates"

if [[ ! -f "$PATH_FILE" ]]; then
    echo "[ERROR] Missing output path file: $PATH_FILE" >&2
    exit 1
fi

if [[ ! -f "$LUBAN_DLL" ]]; then
    echo "[ERROR] Missing Luban executable: $LUBAN_DLL" >&2
    exit 1
fi

path_content=$(head -n 1 "$PATH_FILE")
path_content=${path_content%$'\r'}

if [[ -z "$path_content" ]]; then
    echo "[ERROR] path.txt must contain a non-empty output directory." >&2
    exit 1
fi

output_root_input=${LUBAN_OUTPUT_ROOT:-$path_content}
if [[ "$output_root_input" = /* ]]; then
    OUTPUT_ROOT=$output_root_input
else
    OUTPUT_ROOT="$SCRIPT_DIR/$output_root_input"
fi

mkdir -p "$OUTPUT_ROOT"
OUTPUT_ROOT=$(cd "$OUTPUT_ROOT" && pwd)

PATH_DATA_ROOT="$OUTPUT_ROOT/DataTable"
PATH_GEN_CSHARP="$OUTPUT_ROOT/Scripts/Base/Gen/DataTable"
PATH_DATA_JSON="$PATH_DATA_ROOT"

mkdir -p "$PATH_GEN_CSHARP" "$PATH_DATA_ROOT"

echo "[INFO] Project root: $PROJECT_ROOT"
echo "[INFO] C# output:   $PATH_GEN_CSHARP"
echo "[INFO] JSON output: $PATH_DATA_JSON"

# Gen contains only generated C# files, so Luban may clean stale outputs.
dotnet "$LUBAN_DLL" \
    -t client \
    -c cs-bin \
    --conf "$CONF_FILE" \
    --customTemplateDir "$CUSTOM_TEMPLATE_DIR" \
    -x "cs-bin.outputCodeDir=$PATH_GEN_CSHARP" \
    -x "pathValidator.rootDir=$ASSET_ROOT" \
    -x "lineEnding=lf"

# json and bin are both intermediates: json2tres merges them into .tres
# (row array + base64 bin) and deletes both afterwards.
# DataTable may later hold non-Luban files, so do not let Luban clean it.
dotnet "$LUBAN_DLL" \
    -t client \
    -d json \
    --conf "$CONF_FILE" \
    -x "json.outputDataDir=$PATH_DATA_JSON" \
    -x "pathValidator.rootDir=$ASSET_ROOT" \
    -x "cleanUpOutputDir=false"

dotnet "$LUBAN_DLL" \
    -t client \
    -d bin \
    --conf "$CONF_FILE" \
    -x "bin.outputDataDir=$PATH_DATA_JSON" \
    -x "pathValidator.rootDir=$ASSET_ROOT" \
    -x "cleanUpOutputDir=false"

# Convert exported json tables to Godot .tres resources (json is removed afterwards).
KNIGHT_MCP_EXE="$PROJECT_ROOT/Knight-Tools/knight-dev-mcp/bin/knight-dev-mcp.exe"
if [[ -x "$KNIGHT_MCP_EXE" ]]; then
    "$KNIGHT_MCP_EXE" json2tres "$PATH_DATA_ROOT"
    # Consistency pre-check: Luban table consistency / generated file headers / entity scene .tscn existence.
    "$KNIGHT_MCP_EXE" check
else
    echo "[WARN] knight-dev-mcp.exe not found; keep json output. Build: cd Knight-Tools/knight-dev-mcp && go build -o bin/knight-dev-mcp.exe ." >&2
fi

echo "[INFO] Luban export completed."
