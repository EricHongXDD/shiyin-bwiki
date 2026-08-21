#ifndef MyAppVersion
#define MyAppVersion "0.0.0"
#endif
#define MyAppName "拾音 Shiyin · BWIKI 语音下载器"

[Setup]
AppId={{8B8B8F8E-6C53-4B9A-9E49-4A6F0E1D6C34}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppVerName={#MyAppName} {#MyAppVersion}
DefaultDirName={autopf}\Shiyin
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
OutputDir=..\..\dist
OutputBaseFilename=Shiyin-v{#MyAppVersion}-windows-amd64-setup
Compression=lzma
SolidCompression=yes
WizardStyle=modern
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=admin
UninstallDisplayIcon={app}\Shiyin.exe
VersionInfoDescription={#MyAppName}
VersionInfoProductName={#MyAppName}
VersionInfoVersion={#MyAppVersion}.0
VersionInfoProductVersion={#MyAppVersion}.0


[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式"; GroupDescription: "附加快捷方式："; Flags: unchecked

[Files]
Source: "..\..\build\bin\Shiyin.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\README.md"; DestDir: "{app}"; Flags: isreadme
Source: "..\..\LICENSE"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\拾音 Shiyin"; Filename: "{app}\Shiyin.exe"; WorkingDir: "{app}"; IconFilename: "{app}\Shiyin.exe"
Name: "{autodesktop}\拾音 Shiyin"; Filename: "{app}\Shiyin.exe"; WorkingDir: "{app}"; IconFilename: "{app}\Shiyin.exe"; Tasks: desktopicon

[Run]
Filename: "{app}\Shiyin.exe"; Description: "启动拾音 Shiyin"; WorkingDir: "{app}"; Flags: nowait postinstall skipifsilent
