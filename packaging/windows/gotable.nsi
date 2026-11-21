; GoTable's Windows installer (NSIS), built by scripts/package.sh windows:
;   makensis -DVERSION=0.7.0 -DNUMVER=0.7.0.0 -DSRC=build/windows -DOUT=dist/x.exe gotable.nsi
;
; It installs for the current user, in %LOCALAPPDATA%\Programs\GoTable, so
; it needs no administrator and GoTable can update itself there later
; (internal/update). A Start menu shortcut, an optional desktop one, and an
; entry in Settings -> Apps to uninstall it. Uninstalling keeps the saved
; connections and queries (in %APPDATA%\gotable).

Unicode true
SetCompressor /SOLID lzma
RequestExecutionLevel user
ManifestDPIAware true

!define APP "GoTable"
!define EXE "gotable.exe"
!define UNINSTALLER "Uninstall GoTable.exe"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoTable"
!define URL "https://github.com/umairge66/go-table"

Name "${APP}"
Caption "${APP} ${VERSION} Setup"
OutFile "${OUT}"
InstallDir "$LOCALAPPDATA\Programs\GoTable"
InstallDirRegKey HKCU "${UNINST_KEY}" "InstallLocation"
BrandingText "${APP} ${VERSION}"

VIProductVersion "${NUMVER}"
VIAddVersionKey "ProductName" "${APP}"
VIAddVersionKey "FileDescription" "${APP} Setup"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "GoTable contributors"

!include "MUI2.nsh"
!include "FileFunc.nsh"

!define MUI_ICON "gotable.ico"
!define MUI_UNICON "gotable.ico"
!define MUI_ABORTWARNING
; The side picture of the first and last pages (164 x 314, BMP).
!define MUI_WELCOMEFINISHPAGE_BITMAP "welcome.bmp"

!define MUI_WELCOMEPAGE_TEXT "This installs ${APP} ${VERSION} for your account. No administrator rights are needed.$\r$\n$\r$\nIf ${APP} is running, close it first."
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
; Finish: start GoTable, and (unticked) a desktop shortcut.
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "Start ${APP}"
!define MUI_FINISHPAGE_SHOWREADME ""
!define MUI_FINISHPAGE_SHOWREADME_TEXT "Create a desktop shortcut"
!define MUI_FINISHPAGE_SHOWREADME_NOTCHECKED
!define MUI_FINISHPAGE_SHOWREADME_FUNCTION DesktopShortcut
!insertmacro MUI_PAGE_FINISH

!define MUI_UNCONFIRMPAGE_TEXT_TOP "${APP} will be removed from your computer. Your saved connections and queries are kept."
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

; CloseGoTable waits until the installed program can be replaced: a running
; program can't be deleted. Asks (or, when silent, waits up to 30 s).
!macro CloseGoTable un
Function ${un}CloseGoTable
  StrCpy $1 0
  retry:
    IfFileExists "$INSTDIR\${EXE}" 0 done
    ClearErrors
    Delete "$INSTDIR\${EXE}"
    IfErrors 0 done
    IfSilent 0 ask
      IntOp $1 $1 + 1
      IntCmp $1 60 0 0 give_up
      Sleep 500
      Goto retry
    ask:
      MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "${APP} is running. Close it, then choose Retry." IDRETRY retry
    give_up:
      Abort "${APP} is still running."
  done:
FunctionEnd
!macroend
!insertmacro CloseGoTable ""
!insertmacro CloseGoTable "un."

Function DesktopShortcut
  CreateShortCut "$DESKTOP\${APP}.lnk" "$INSTDIR\${EXE}" "" "$INSTDIR\${EXE}" 0
FunctionEnd

Section "Install"
  SetOutPath "$INSTDIR"
  Call CloseGoTable
  File "${SRC}\${EXE}"
  File "${SRC}\LICENSE"
  File "${SRC}\README.md"
  Delete "$INSTDIR\${EXE}.old" ; left by an update
  WriteUninstaller "$INSTDIR\${UNINSTALLER}"

  CreateShortCut "$SMPROGRAMS\${APP}.lnk" "$INSTDIR\${EXE}" "" "$INSTDIR\${EXE}" 0

  ; Settings -> Apps (and the update check reads InstallLocation).
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "${APP}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "GoTable"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${EXE},0"
  WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "URLInfoAbout" "${URL}"
  WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\${UNINSTALLER}"'
  WriteRegStr HKCU "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\${UNINSTALLER}" /S'
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKCU "${UNINST_KEY}" "EstimatedSize" "$0"
SectionEnd

Section "Uninstall"
  Call un.CloseGoTable
  Delete "$INSTDIR\${EXE}.old"
  Delete "$INSTDIR\LICENSE"
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\${UNINSTALLER}"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\${APP}.lnk"
  Delete "$DESKTOP\${APP}.lnk"
  DeleteRegKey HKCU "${UNINST_KEY}"
SectionEnd
