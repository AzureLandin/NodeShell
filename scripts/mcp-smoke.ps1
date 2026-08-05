$ErrorActionPreference = 'Stop'
$exe = $args[0]
$tmp = New-Item -ItemType Directory -Path ([System.IO.Path]::GetTempPath() + "mcp-smoke-$([guid]::NewGuid())") -Force
$in = Join-Path $tmp 'in.jsonl'
try {
  $lines = @(
    '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"ci","version":"1"}}}',
    '{"jsonrpc":"2.0","method":"notifications/initialized"}',
    '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}',
    '{"jsonrpc":"2.0","id":3,"method":"ping","params":{}}'
  )
  # PS 5.1 Set-Content -Encoding utf8 prepends a BOM, corrupting the first
  # JSON line (parse error -32700). Write UTF-8 without BOM instead.
  $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
  [System.IO.File]::WriteAllLines($in, $lines, $utf8NoBom)

  # Start-Process -PassThru reports a null ExitCode under PS 5.1; drive the
  # .NET Process directly so WaitForExit/ExitCode are reliable.
  $psi = New-Object System.Diagnostics.ProcessStartInfo
  $psi.FileName = $exe
  $psi.Arguments = '--mcp'
  $psi.UseShellExecute = $false
  $psi.RedirectStandardInput = $true
  $psi.RedirectStandardOutput = $true
  $psi.RedirectStandardError = $true
  $p = New-Object System.Diagnostics.Process
  $p.StartInfo = $psi
  $null = $p.Start()
  $outTask = $p.StandardOutput.ReadToEndAsync()
  $errTask = $p.StandardError.ReadToEndAsync()
  $p.StandardInput.Write([System.IO.File]::ReadAllText($in))
  $p.StandardInput.Close()
  if (-not $p.WaitForExit(30000)) { $p.Kill(); throw 'MCP process timed out' }
  $out = $outTask.Result
  $err = $errTask.Result
  if ($p.ExitCode -ne 0) { throw "MCP exit $($p.ExitCode): $err" }
  if ($err.Length -ne 0) { throw "MCP stderr not empty: $err" }
  $responses = @($out -split "`r?`n" | Where-Object { $_ })
  if ($responses.Count -ne 3) { throw "expected 3 responses, got $($responses.Count)" }
  $init = $responses[0] | ConvertFrom-Json
  if ($init.result.protocolVersion -ne '2024-11-05') { throw "protocol mismatch: $($init.result.protocolVersion)" }
  $tools = $responses[1] | ConvertFrom-Json
  if (@($tools.result.tools).Count -ne 10) { throw "expected 10 tools, got $(@($tools.result.tools).Count)" }
  Write-Host 'MCP smoke OK'
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
