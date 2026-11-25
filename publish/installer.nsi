; Ant Browser NSIS Installer Script
; Usage: makensis /DVERSION=1.9.1 /DSTAGINGDIR=C:\path\to\staging installer.nsi

Unicode True
!ifdef BESTCOMPRESSION
  SetCompressor /SOLID lzma
!else
  SetCompressor lzma
!endif

!ifndef VERSION
  !define VERSION "1.9.1"
!endif
!ifndef STAGINGDIR
  !define STAGINGDIR "..\publish\staging"
!endif

!define PRODUCT_NAME    "Ant Browser"
!define PRODUCT_EXE     "ant-chrome.exe"
!define PRODUCT_ICON    "AntBrowser.ico"
!define UNINSTALL_KEY   "Software\Microsoft\Windows\CurrentVersion\Uninstall\AntBrowser"
; 新用户默认装到当前用户目录：免管理员、目录可写，data 不会再落到 Program Files。
!define INSTALL_DIR_USER    "$LOCALAPPDATA\Programs\Ant Browser"
; 历史版本默认位置，仅用于识别老用户并沿用其安装路径。
!define INSTALL_DIR_LEGACY  "$PROGRAMFILES64\Ant Browser"
!define PROTOCOL_KEY "Software\Classes\ant-browser"
!define POWERSHELL_EXE  "$SYSDIR\WindowsPowerShell\v1.0\powershell.exe"

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"

!macro RequireAdmin EXE
  System::Call 'shell32::IsUserAnAdmin() i .r0'
  ${If} $0 == 0
    ${GetParameters} $R0
    ClearErrors
    ExecShell "runas" "${EXE}" "$R0"
    ${If} ${Errors}
      MessageBox MB_ICONSTOP|MB_OK "此操作需要管理员权限，请在 UAC 提示中允许操作后重试。" /SD IDOK
      SetErrorLevel 1
    ${EndIf}
    Quit
  ${EndIf}
!macroend

!macro WriteCloseProcessScript HANDLE
  FileWrite ${HANDLE} "param([string]$$InstallDir, [string]$$ExcludePath)$\r$\n"
  FileWrite ${HANDLE} "$$ErrorActionPreference = 'SilentlyContinue'$\r$\n"
  FileWrite ${HANDLE} "if ([string]::IsNullOrWhiteSpace($$InstallDir) -or -not (Test-Path -LiteralPath $$InstallDir)) { exit 0 }$\r$\n"
  FileWrite ${HANDLE} "$$root = [System.IO.Path]::GetFullPath($$InstallDir).TrimEnd('\') + '\'$\r$\n"
  FileWrite ${HANDLE} "$$exclude = ''$\r$\n"
  FileWrite ${HANDLE} "if (-not [string]::IsNullOrWhiteSpace($$ExcludePath)) { $$exclude = [System.IO.Path]::GetFullPath($$ExcludePath) }$\r$\n"
  FileWrite ${HANDLE} "$$coreNames = @('xray.exe', 'sing-box.exe', 'mihomo.exe', 'clash.exe')$\r$\n"
  FileWrite ${HANDLE} "function Get-AntBrowserProcesses {$\r$\n"
  FileWrite ${HANDLE} "  @(Get-CimInstance Win32_Process | Where-Object {$\r$\n"
  FileWrite ${HANDLE} "    $$_.ExecutablePath -and $$_.ExecutablePath.StartsWith($$root, [System.StringComparison]::OrdinalIgnoreCase) -and ($$exclude -eq '' -or -not $$_.ExecutablePath.Equals($$exclude, [System.StringComparison]::OrdinalIgnoreCase))$\r$\n"
  FileWrite ${HANDLE} "  })$\r$\n"
  FileWrite ${HANDLE} "}$\r$\n"
  FileWrite ${HANDLE} "$$procs = @(Get-AntBrowserProcesses)$\r$\n"
  FileWrite ${HANDLE} "if (-not $$procs -or $$procs.Count -eq 0) { exit 0 }$\r$\n"
  FileWrite ${HANDLE} "$$coreProcs = @($$procs | Where-Object { $$coreNames -contains $$_.Name.ToLowerInvariant() })$\r$\n"
  FileWrite ${HANDLE} "$$killed = @()$\r$\n"
  FileWrite ${HANDLE} "foreach ($$proc in $$coreProcs) {$\r$\n"
  FileWrite ${HANDLE} "  Stop-Process -Id $$proc.ProcessId -Force -ErrorAction SilentlyContinue$\r$\n"
  FileWrite ${HANDLE} "  if ($$?) { $$killed += ($$proc.Name + '#' + $$proc.ProcessId) }$\r$\n"
  FileWrite ${HANDLE} "}$\r$\n"
  FileWrite ${HANDLE} "if ($$killed.Count -gt 0) {$\r$\n"
  FileWrite ${HANDLE} "  Write-Host ('killed leftover cores: ' + ($$killed -join ', '))$\r$\n"
  FileWrite ${HANDLE} "  Start-Sleep -Milliseconds 600$\r$\n"
  FileWrite ${HANDLE} "}$\r$\n"
  FileWrite ${HANDLE} "$$left = @(Get-AntBrowserProcesses)$\r$\n"
  FileWrite ${HANDLE} "if (-not $$left -or $$left.Count -eq 0) { exit 0 }$\r$\n"
  FileWrite ${HANDLE} "$$names = ($$left | ForEach-Object { $$_.Name + '#' + $$_.ProcessId }) -join ', '$\r$\n"
  FileWrite ${HANDLE} "Write-Host ('running: ' + $$names)$\r$\n"
  FileWrite ${HANDLE} "exit 1$\r$\n"
!macroend

Function CloseInstalledProcesses
  IfFileExists "$INSTDIR" 0 done

retry_powershell:
  IfFileExists "${POWERSHELL_EXE}" 0 fallback_taskkill

  GetTempFileName $0
  Delete $0
  StrCpy $0 "$0.ps1"
  FileOpen $1 $0 w
  !insertmacro WriteCloseProcessScript $1
  FileClose $1

  DetailPrint "检查安装目录中的运行进程: $INSTDIR"
  ExecWait '"${POWERSHELL_EXE}" -NoProfile -ExecutionPolicy Bypass -File "$0" -InstallDir "$INSTDIR" -ExcludePath ""' $2
  Delete $0

  ${If} $2 == 0
    Goto done
  ${EndIf}

  MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "检测到安装目录中仍有运行进程。$\r$\n$\r$\n目录：$INSTDIR$\r$\n$\r$\n请先从窗口或托盘菜单正常退出应用，再点击“重试”重新检查；点击“取消”将终止本次安装。" IDRETRY retry_powershell IDCANCEL install_abort

install_abort:
  Abort "安装已取消：安装目录中的旧进程仍未退出。"

fallback_taskkill:
  MessageBox MB_OK|MB_ICONSTOP "无法安全检查安装目录中的运行进程。为避免强制中断，安装已取消。"
  Abort "安装已取消：无法安全检查安装目录中的运行进程。"

done:
FunctionEnd

Function un.CloseInstalledProcesses
  IfFileExists "$INSTDIR" 0 done

retry_powershell:
  IfFileExists "${POWERSHELL_EXE}" 0 fallback_taskkill

  GetTempFileName $0
  Delete $0
  StrCpy $0 "$0.ps1"
  FileOpen $1 $0 w
  !insertmacro WriteCloseProcessScript $1
  FileClose $1

  DetailPrint "检查安装目录中的运行进程: $INSTDIR"
  ExecWait '"${POWERSHELL_EXE}" -NoProfile -ExecutionPolicy Bypass -File "$0" -InstallDir "$INSTDIR" -ExcludePath "$INSTDIR\Uninstall.exe"' $2
  Delete $0

  ${If} $2 == 0
    Goto done
  ${EndIf}

  MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "检测到安装目录中仍有运行进程。$\r$\n$\r$\n目录：$INSTDIR$\r$\n$\r$\n请先从窗口或托盘菜单正常退出应用，再点击“重试”重新检查；点击“取消”将终止本次卸载。" IDRETRY retry_powershell IDCANCEL uninstall_abort

uninstall_abort:
  Abort "卸载已取消：安装目录中的旧进程仍未退出。"

fallback_taskkill:
  MessageBox MB_OK|MB_ICONSTOP "无法安全检查安装目录中的运行进程。为避免强制中断，卸载已取消。"
  Abort "卸载已取消：无法安全检查安装目录中的运行进程。"

done:
FunctionEnd

Function RegisterDiagnosticsWatcher
  IfFileExists "$INSTDIR\data\diagnostics\install-ant-chrome-diagnostics-watcher.ps1" 0 done
  DetailPrint "Registering read-only diagnostics watcher"
  ExecWait '"${POWERSHELL_EXE}" -NoProfile -ExecutionPolicy Bypass -File "$INSTDIR\data\diagnostics\install-ant-chrome-diagnostics-watcher.ps1" -InstallDir "$INSTDIR"' $0
  ${If} $0 == 0
    DetailPrint "Diagnostics watcher registered"
  ${Else}
    DetailPrint "Warning: diagnostics watcher registration failed"
  ${EndIf}
done:
FunctionEnd

Function un.UnregisterDiagnosticsWatcher
  IfFileExists "$INSTDIR\data\diagnostics\install-ant-chrome-diagnostics-watcher.ps1" 0 done
  DetailPrint "Removing read-only diagnostics watcher"
  ExecWait '"${POWERSHELL_EXE}" -NoProfile -ExecutionPolicy Bypass -File "$INSTDIR\data\diagnostics\install-ant-chrome-diagnostics-watcher.ps1" -InstallDir "$INSTDIR" -Uninstall' $0
done:
FunctionEnd

Function PreserveLegacyBackupLocalConfig
  StrCpy $4 "$INSTDIR"
  ReadRegStr $3 HKLM "${UNINSTALL_KEY}" "InstallLocation"
  ${If} "$3" != ""
    IfFileExists "$3\backup.local.yaml" 0 legacy_use_current_install
    StrCpy $4 "$3"
  ${EndIf}

legacy_use_current_install:
  IfFileExists "$APPDATA\${PRODUCT_NAME}\backup.local.yaml" legacy_done
  IfFileExists "$4\backup.local.yaml" 0 legacy_done

  ClearErrors
  CreateDirectory "$APPDATA\${PRODUCT_NAME}"
  IfErrors legacy_failed
  DetailPrint "Preserving legacy backup credentials"
  ClearErrors
  CopyFiles /SILENT "$4\backup.local.yaml" "$APPDATA\${PRODUCT_NAME}"
  IfErrors legacy_failed
  Goto legacy_done

legacy_failed:
  MessageBox MB_ICONSTOP|MB_OK "Could not preserve the legacy backup credentials. Installation is cancelled to prevent OpenList / S3 Token loss."
  Abort "Installation cancelled: legacy backup credential migration failed."

legacy_done:
FunctionEnd

Function un.PreserveLegacyBackupLocalConfig
  IfFileExists "$INSTDIR\backup.local.yaml" 0 done
  IfFileExists "$APPDATA\${PRODUCT_NAME}\backup.local.yaml" done

  ClearErrors
  CreateDirectory "$APPDATA\${PRODUCT_NAME}"
  IfErrors preserve_failed

  DetailPrint "迁移旧版本地备份凭据到 $APPDATA\${PRODUCT_NAME}"
  ClearErrors
  CopyFiles "$INSTDIR\backup.local.yaml" "$APPDATA\${PRODUCT_NAME}\"
  IfErrors preserve_failed
  Goto done

preserve_failed:
  MessageBox MB_ICONSTOP|MB_OK "无法保存旧版本地备份凭据：$APPDATA\${PRODUCT_NAME}\backup.local.yaml。为避免 OpenList / S3 Token 丢失，卸载已取消。请检查该目录权限后重试。"
  Abort "卸载已取消：旧版本地备份凭据迁移失败。"

done:
FunctionEnd

Var InstallScope

; .onInit：老用户沿用原安装路径（机器级，需要管理员）；新用户装到当前用户目录（免 UAC）。
Function .onInit
  StrCpy $InstallScope "user"
  ; 1) 机器级旧安装：HKLM 注册记录，或注册表丢失但 Program Files 下确有真实数据
  ReadRegStr $0 HKLM "${UNINSTALL_KEY}" "InstallLocation"
  ${If} $0 == ""
    ${If} ${FileExists} "${INSTALL_DIR_LEGACY}\data\app.db"
      StrCpy $0 "${INSTALL_DIR_LEGACY}"
    ${EndIf}
  ${EndIf}

  ${If} $0 != ""
    StrCpy $InstallScope "machine"
    StrCpy $INSTDIR "$0"
    !insertmacro RequireAdmin "$EXEPATH"
    Return
  ${EndIf}

  ; 2) 用户级旧安装（本方案升级过）：沿用其路径
  ReadRegStr $1 HKCU "${UNINSTALL_KEY}" "InstallLocation"
  ${If} $1 != ""
    StrCpy $INSTDIR "$1"
  ${Else}
    StrCpy $INSTDIR "${INSTALL_DIR_USER}"
  ${EndIf}
FunctionEnd

; 仅在受保护或不可写目录时警告：默认的用户目录不触发，避免打扰新用户。
Function WarnInstallDir
  StrCpy $0 "$INSTDIR"

  StrLen $1 "$PROGRAMFILES64"
  ${If} $1 > 0
    StrCpy $2 "$0" $1
    ${If} "$2" == "$PROGRAMFILES64"
      Goto warn_dir
    ${EndIf}
  ${EndIf}

  StrLen $1 "$PROGRAMFILES"
  ${If} $1 > 0
    StrCpy $2 "$0" $1
    ${If} "$2" == "$PROGRAMFILES"
      Goto warn_dir
    ${EndIf}
  ${EndIf}

  StrLen $1 "$PROGRAMFILES32"
  ${If} $1 > 0
    StrCpy $2 "$0" $1
    ${If} "$2" == "$PROGRAMFILES32"
      Goto warn_dir
    ${EndIf}
  ${EndIf}

  ; 目标父目录不可写（普通权限建不了目录）同样警告。
  ; 先创建父目录：$LOCALAPPDATA\Programs 在个别系统上不存在，直接探测会误报。
  ${GetParent} "$0" $3
  CreateDirectory "$3"
  ClearErrors
  FileOpen $4 "$3\.ant-browser-write-probe" w
  ${If} ${Errors}
    Goto warn_dir
  ${EndIf}
  FileClose $4
  Delete "$3\.ant-browser-write-probe"

  Return

warn_dir:
  MessageBox MB_ICONEXCLAMATION|MB_OKCANCEL|MB_DEFBUTTON2 "当前安装目录位于受权限保护的位置，或当前用户对它没有写权限。$\r$\n$\r$\nAnt Browser 会在安装目录写入 data、浏览器实例和运行时文件；普通权限运行时可能写入失败，导致再次启动闪退或浏览器实例启动失败。$\r$\n$\r$\n建议使用默认目录（当前用户目录，无需管理员），或改为 D:\software\Ant Browser 这类可写目录。$\r$\n$\r$\n点击“确定”继续安装到当前目录，点击“取消”返回修改安装路径。" IDOK continue_install IDCANCEL cancel_install

continue_install:
  Return

cancel_install:
  Abort
FunctionEnd

Name "${PRODUCT_NAME} ${VERSION}"
OutFile "..\publish\output\AntBrowser-Setup-${VERSION}.exe"
InstallDir "${INSTALL_DIR_USER}"
RequestExecutionLevel user

!define MUI_ICON "..\build\windows\icon.ico"
!define MUI_UNICON "..\build\windows\icon.ico"

!insertmacro MUI_PAGE_WELCOME
!define MUI_DIRECTORYPAGE_TEXT_TOP "默认安装到当前用户目录（推荐，无需管理员权限，数据也会写在这里）。如需自定义，可直接修改为 D:\software\Ant Browser 这类可写目录；不建议选 Program Files，否则普通权限运行时可能无法写入数据。$\r$\n$\r$\n如果之前已安装过（含便携版解压目录），请手动选择原安装目录，这样原有实例与配置会继续沿用。"
!define MUI_DIRECTORYPAGE_TEXT_DESTINATION "安装目录（默认当前用户目录）"
!define MUI_PAGE_CUSTOMFUNCTION_LEAVE WarnInstallDir
!insertmacro MUI_PAGE_DIRECTORY
!define MUI_COMPONENTSPAGE_SMALLDESC
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\${PRODUCT_EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "Launch Ant Browser"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "SimpChinese"

Section "Ant Browser (required)" SecMain
  SectionIn RO
  Call CloseInstalledProcesses
  SetShellVarContext current
  Call PreserveLegacyBackupLocalConfig
  ${If} $InstallScope == "machine"
    SetShellVarContext all
  ${EndIf}
  SetOutPath "$INSTDIR"
  File "${STAGINGDIR}\${PRODUCT_EXE}"
  File /oname=${PRODUCT_ICON} "..\build\windows\icon.ico"
!if /FileExists "${STAGINGDIR}\config.yaml"
  IfFileExists "$INSTDIR\config.yaml" +2 0
    File "${STAGINGDIR}\config.yaml"
!else
  !echo "Warning: ${STAGINGDIR}\\config.yaml not found, installer will use runtime defaults."
!endif
!if /FileExists "${STAGINGDIR}\chrome\*"
  SetOutPath "$INSTDIR\chrome"
  File /r "${STAGINGDIR}\chrome\*"
  SetOutPath "$INSTDIR"
!endif
  CreateDirectory "$INSTDIR\data"
  SetOutPath "$INSTDIR\data\diagnostics"
  File /r "${STAGINGDIR}\data\diagnostics\*"
  SetOutPath "$INSTDIR"
  WriteRegStr SHCTX "${PROTOCOL_KEY}" "" "URL:Ant Browser"
  WriteRegStr SHCTX "${PROTOCOL_KEY}" "URL Protocol" ""
  WriteRegStr SHCTX "${PROTOCOL_KEY}\shell\open\command" "" '"$INSTDIR\${PRODUCT_EXE}" "%1"'

  WriteRegStr SHCTX "${UNINSTALL_KEY}" "DisplayName"     "${PRODUCT_NAME}"
  WriteRegStr SHCTX "${UNINSTALL_KEY}" "DisplayVersion"  "${VERSION}"
  WriteRegStr SHCTX "${UNINSTALL_KEY}" "Publisher"       "Ant Chrome Team"
  WriteRegStr SHCTX "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr SHCTX "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegStr SHCTX "${UNINSTALL_KEY}" "DisplayIcon"     "$INSTDIR\${PRODUCT_ICON}"
  WriteRegStr SHCTX "${UNINSTALL_KEY}" "NoModify"        "1"
  WriteRegStr SHCTX "${UNINSTALL_KEY}" "NoRepair"        "1"
  WriteUninstaller "$INSTDIR\Uninstall.exe"
  ; 诊断看护注册的是系统级计划任务，需要管理员；用户级安装跳过，不影响主功能。
  ${If} $InstallScope == "machine"
    Call RegisterDiagnosticsWatcher
  ${Else}
    DetailPrint "用户级安装：跳过系统级诊断看护注册（需要管理员权限）"
  ${EndIf}
  CreateDirectory "$SMPROGRAMS\${PRODUCT_NAME}"
  CreateShortcut "$SMPROGRAMS\${PRODUCT_NAME}\${PRODUCT_NAME}.lnk" "$INSTDIR\${PRODUCT_EXE}" "" "$INSTDIR\${PRODUCT_ICON}"
  CreateShortcut "$SMPROGRAMS\${PRODUCT_NAME}\Uninstall.lnk" "$INSTDIR\Uninstall.exe" "" "$INSTDIR\${PRODUCT_ICON}"
SectionEnd

Section "Proxy Runtime (xray / sing-box)" SecRuntime
  SectionIn RO
  SetOutPath "$INSTDIR\bin"
  File "${STAGINGDIR}\bin\xray.exe"
  File "${STAGINGDIR}\bin\sing-box.exe"
SectionEnd

Section /o "Desktop Shortcut" SecDesktop
  CreateShortcut "$DESKTOP\${PRODUCT_NAME}.lnk" "$INSTDIR\${PRODUCT_EXE}" "" "$INSTDIR\${PRODUCT_ICON}"
SectionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecMain}    "Ant Browser main program and default config (required)"
  !insertmacro MUI_DESCRIPTION_TEXT ${SecRuntime} "xray and sing-box proxy tools (vless/vmess/hysteria2)"
  !insertmacro MUI_DESCRIPTION_TEXT ${SecDesktop} "Create a shortcut on the desktop"
!insertmacro MUI_FUNCTION_DESCRIPTION_END

Function un.onInit
  StrCpy $InstallScope "user"
  ReadRegStr $0 HKLM "${UNINSTALL_KEY}" "InstallLocation"
  ReadRegStr $1 HKCU "${UNINSTALL_KEY}" "InstallLocation"
  ${If} $0 == $INSTDIR
    StrCpy $InstallScope "machine"
  ${ElseIf} $0 == ""
  ${AndIf} $1 != $INSTDIR
  ${AndIf} $INSTDIR == "${INSTALL_DIR_LEGACY}"
    StrCpy $InstallScope "machine"
  ${EndIf}

  ${If} $InstallScope == "machine"
    !insertmacro RequireAdmin "$INSTDIR\Uninstall.exe"
  ${Else}
    ClearErrors
    GetTempFileName $0 "$INSTDIR"
    ${If} ${Errors}
      !insertmacro RequireAdmin "$INSTDIR\Uninstall.exe"
      MessageBox MB_ICONSTOP|MB_OK "无法写入安装目录，卸载已取消。请检查该目录权限后重试。" /SD IDOK
      SetErrorLevel 1
      Quit
    ${Else}
      Delete "$0"
    ${EndIf}
  ${EndIf}
FunctionEnd

Section "Uninstall"
  Call un.CloseInstalledProcesses
  SetShellVarContext current
  Call un.PreserveLegacyBackupLocalConfig
  ${If} $InstallScope == "machine"
    SetShellVarContext all
    Call un.UnregisterDiagnosticsWatcher
  ${EndIf}

  Delete /REBOOTOK "$INSTDIR\${PRODUCT_EXE}"
  Delete /REBOOTOK "$INSTDIR\${PRODUCT_ICON}"
  Delete /REBOOTOK "$INSTDIR\Uninstall.exe"
  ReadRegStr $0 SHCTX "${PROTOCOL_KEY}\shell\open\command" ""
  ${If} $0 == '"$INSTDIR\${PRODUCT_EXE}" "%1"'
    DeleteRegKey SHCTX "${PROTOCOL_KEY}"
  ${EndIf}
  ReadRegStr $0 SHCTX "${UNINSTALL_KEY}" "InstallLocation"
  ${If} $0 == $INSTDIR
    Delete /REBOOTOK "$SMPROGRAMS\${PRODUCT_NAME}\${PRODUCT_NAME}.lnk"
    Delete /REBOOTOK "$SMPROGRAMS\${PRODUCT_NAME}\Uninstall.lnk"
    RMDir /REBOOTOK "$SMPROGRAMS\${PRODUCT_NAME}"
    Delete /REBOOTOK "$DESKTOP\${PRODUCT_NAME}.lnk"
    DeleteRegKey SHCTX "${UNINSTALL_KEY}"
  ${EndIf}
  ; bin 和 chrome 也存放用户安装的代理内核与浏览器内核。
  MessageBox MB_ICONQUESTION|MB_YESNO|MB_DEFBUTTON2 "是否删除整个安装目录？$\r$\n$\r$\n选择“是”将删除此目录内的实例数据、代理配置和内核等文件。$\r$\n此操作不可恢复。" /SD IDNO IDYES un_remove_all_data
  Goto un_done

un_remove_all_data:
  SetOutPath "$TEMP"
  RMDir /r /REBOOTOK "$INSTDIR"
  IfFileExists "$INSTDIR\." 0 +2
    MessageBox MB_ICONEXCLAMATION|MB_OK "检测到部分文件仍被占用，已标记为重启后自动删除。" /SD IDOK

un_done:
SectionEnd
