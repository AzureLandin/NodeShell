$ErrorActionPreference = 'Stop'
$exe = $args[0]
if (-not (Test-Path -LiteralPath $exe)) {
  $dir = Split-Path -Parent $exe
  $candidates = @(Get-ChildItem -LiteralPath $dir -Filter 'nodeshell*.exe' -ErrorAction SilentlyContinue |
    Sort-Object Name | ForEach-Object { $_.FullName })
  if ($candidates.Count -eq 0) {
    $listing = '(missing dir)'
    if (Test-Path -LiteralPath $dir) {
      $listing = @(Get-ChildItem -LiteralPath $dir -ErrorAction SilentlyContinue | ForEach-Object { $_.Name }) -join ', '
    }
    throw "MCP target not found: $exe (dir contents: $listing)"
  }
  $exe = $candidates[0]
  Write-Host "MCP smoke using $exe"
}
$tmp = New-Item -ItemType Directory -Path ([System.IO.Path]::GetTempPath() + "mcp-smoke-$([guid]::NewGuid())") -Force
$in = Join-Path $tmp 'in.jsonl'
$outFile = Join-Path $tmp 'out.txt'
$errFile = Join-Path $tmp 'err.txt'
try {
  $lines = @(
    '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"ci","version":"1"}}}',
    '{"jsonrpc":"2.0","method":"notifications/initialized"}',
    '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}',
    '{"jsonrpc":"2.0","id":3,"method":"ping","params":{}}'
  )
  # PS 5.1 Set-Content -Encoding utf8 prepends a BOM. Write UTF-8 without BOM.
  $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
  [System.IO.File]::WriteAllLines($in, $lines, $utf8NoBom)

  # Drive the exe through cmd.exe file redirection. .NET Process + StreamWriter
  # has repeatedly corrupted or dropped MCP stdio on windows-latest even after
  # -windowsconsole; cmd's < / > handles match how MCP clients spawn the binary.
  $exeFull = (Resolve-Path -LiteralPath $exe).Path
  $cmd = '"{0}" --mcp < "{1}" > "{2}" 2> "{3}"' -f $exeFull, $in, $outFile, $errFile
  $p = Start-Process -FilePath 'cmd.exe' -ArgumentList @('/c', $cmd) -Wait -PassThru -NoNewWindow
  $code = $p.ExitCode
  if ($null -eq $code) { $code = $LASTEXITCODE }
  $out = ''
  $err = ''
  if (Test-Path -LiteralPath $outFile) {
    $out = [System.IO.File]::ReadAllText($outFile, $utf8NoBom)
  }
  if (Test-Path -LiteralPath $errFile) {
    $err = [System.IO.File]::ReadAllText($errFile, $utf8NoBom)
  }
  if ($code -ne 0) {
    throw "MCP exit $code`n--- stdout ---`n$out`n--- stderr ---`n$err"
  }
  if ($err.Trim().Length -ne 0) {
    throw "MCP stderr not empty:`n$err`n--- stdout ---`n$out"
  }
  $responses = @($out -split "`r?`n" | Where-Object { $_ })
  if ($responses.Count -lt 3) {
    throw "expected at least 3 responses, got $($responses.Count)`n--- stdout ---`n$out"
  }
  # String-match like scripts/mcp-smoke.sh — avoid PS 5.1 ConvertFrom-Json
  # depth quirks on the tools/list schema payload.
  $initLine = $null
  $toolsLine = $null
  $pingLine = $null
  foreach ($line in $responses) {
    if ($line -match '"id"\s*:\s*1\b' -and -not $initLine) { $initLine = $line }
    if ($line -match '"id"\s*:\s*2\b' -and -not $toolsLine) { $toolsLine = $line }
    if ($line -match '"id"\s*:\s*3\b' -and -not $pingLine) { $pingLine = $line }
  }
  if (-not $initLine) { throw "missing initialize response (id=1)`n--- stdout ---`n$out" }
  if ($initLine -notmatch '"protocolVersion"\s*:\s*"2024-11-05"') {
    throw "protocol mismatch: $initLine"
  }
  if (-not $toolsLine) { throw "missing tools/list response (id=2)`n--- stdout ---`n$out" }
  $toolNames = [regex]::Matches($toolsLine, '"name"\s*:')
  if ($toolNames.Count -ne 10) {
    throw "expected 10 tools, got $($toolNames.Count)`n--- tools line ---`n$toolsLine"
  }
  if (-not $pingLine) { throw "missing ping response (id=3)`n--- stdout ---`n$out" }
  Write-Host 'MCP smoke OK'
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
