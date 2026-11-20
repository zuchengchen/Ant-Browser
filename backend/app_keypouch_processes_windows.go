//go:build windows

package backend

import (
	"context"
	"encoding/json"
)

func keyPouchRunningDirs(ctx context.Context) (map[string]browserRuntimeDetection, error) {
	// Query WMI once per snapshot regardless of the number of bound instances.
	// Query failure must stay distinguishable from no running browser.
	const query = `$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$rows = @(Get-CimInstance Win32_Process -Property ProcessId,CommandLine | Where-Object { $_.CommandLine -like '*--user-data-dir*' } | Select-Object ProcessId,CommandLine)
ConvertTo-Json -InputObject $rows -Compress`
	b, err := runPowerShellJSONContext(ctx, query)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ProcessId   int
		CommandLine string
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, err
	}
	dirs := make(map[string]browserRuntimeDetection)
	for _, row := range rows {
		indexKeyPouchProcess(dirs, row.ProcessId, splitProcessCommandLine(row.CommandLine))
	}
	return dirs, nil
}

func keyPouchHasVisibleWindow(pid int) bool {
	if pid <= 0 {
		return false
	}
	for _, w := range findTopLevelWindowsForProfileMarker(uint32(pid)) {
		if !w.owned && (w.visible || w.minimized) {
			return true
		}
	}
	return false
}
