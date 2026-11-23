@echo off
setlocal EnableExtensions

set "SCRIPT_DIR=%~dp0"
pushd "%SCRIPT_DIR%" >nul || exit /b 1

set "PATH_FILE=%SCRIPT_DIR%path.txt"
set "LUBAN_DLL=%SCRIPT_DIR%Tools\Luban\Luban.dll"
set "CONF_FILE=%SCRIPT_DIR%luban.conf"
set "CUSTOM_TEMPLATE_DIR=%SCRIPT_DIR%CustomTemplates"

if not exist "%PATH_FILE%" (
    echo [ERROR] Missing output path file: %PATH_FILE%
    goto :ON_ERROR
)

if not exist "%LUBAN_DLL%" (
    echo [ERROR] Missing Luban executable: %LUBAN_DLL%
    goto :ON_ERROR
)

set /p "path_content="<"%PATH_FILE%"
if not defined path_content (
    echo [ERROR] path.txt must contain a non-empty output directory.
    goto :ON_ERROR
)

if defined LUBAN_OUTPUT_ROOT (
    set "output_root_input=%LUBAN_OUTPUT_ROOT%"
) else (
    set "output_root_input=%path_content%"
)

for %%I in ("%SCRIPT_DIR%..") do set "PROJECT_ROOT=%%~fI"
for %%I in ("%output_root_input%") do set "OUTPUT_ROOT=%%~fI"

set "ASSET_ROOT=%PROJECT_ROOT%\Knight-Client\Textures"
set "PATH_DATA_ROOT=%OUTPUT_ROOT%\DataTable"
set "PATH_GEN_CSHARP=%OUTPUT_ROOT%\Scripts\Base\Gen\DataTable"
set "PATH_DATA_JSON=%PATH_DATA_ROOT%"

if not exist "%PATH_GEN_CSHARP%" mkdir "%PATH_GEN_CSHARP%"
if not exist "%PATH_DATA_ROOT%" mkdir "%PATH_DATA_ROOT%"

echo [INFO] Project root: %PROJECT_ROOT%
echo [INFO] C# output:   %PATH_GEN_CSHARP%
echo [INFO] JSON output: %PATH_DATA_JSON%

rem Gen contains only generated C# files, so Luban may clean stale outputs.
dotnet "%LUBAN_DLL%" ^
    -t client ^
    -c cs-bin ^
    --conf "%CONF_FILE%" ^
    --customTemplateDir "%CUSTOM_TEMPLATE_DIR%" ^
    -x "cs-bin.outputCodeDir=%PATH_GEN_CSHARP%" ^
    -x "pathValidator.rootDir=%ASSET_ROOT%" ^
    -x "lineEnding=lf"

set "exit_code=%ERRORLEVEL%"
if not "%exit_code%"=="0" (
    echo [ERROR] Luban C# export failed with exit code %exit_code%.
    goto :ON_ERROR
)

rem json and bin are both intermediates: json2tres merges them into .tres
rem (row array + base64 bin) and deletes both afterwards.
rem DataTable may later hold non-Luban files, so do not let Luban clean it.
dotnet "%LUBAN_DLL%" ^
    -t client ^
    -d json ^
    --conf "%CONF_FILE%" ^
    -x "json.outputDataDir=%PATH_DATA_JSON%" ^
    -x "pathValidator.rootDir=%ASSET_ROOT%" ^
    -x "cleanUpOutputDir=false"

dotnet "%LUBAN_DLL%" ^
    -t client ^
    -d bin ^
    --conf "%CONF_FILE%" ^
    -x "bin.outputDataDir=%PATH_DATA_JSON%" ^
    -x "pathValidator.rootDir=%ASSET_ROOT%" ^
    -x "cleanUpOutputDir=false"

set "exit_code=%ERRORLEVEL%"
if not "%exit_code%"=="0" (
    echo [ERROR] Luban data export failed with exit code %exit_code%.
    goto :ON_ERROR
)

rem Convert exported json tables to Godot .tres resources (json is removed afterwards).
set "KNIGHT_MCP_EXE=%PROJECT_ROOT%\Knight-Tools\knight-dev-mcp\bin\knight-dev-mcp.exe"
if exist "%KNIGHT_MCP_EXE%" (
    "%KNIGHT_MCP_EXE%" json2tres "%PATH_DATA_ROOT%"
    rem Consistency pre-check: Luban table consistency / generated file headers / entity scene .tscn existence.
    "%KNIGHT_MCP_EXE%" check
) else (
    echo [WARN] knight-dev-mcp.exe not found; keep json output. Build it in Knight-Tools\knight-dev-mcp
)

echo [INFO] Luban export completed.
popd
echo.
pause
exit /b 0

:ON_ERROR
popd
echo.
pause
exit /b 1

