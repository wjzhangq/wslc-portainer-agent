# Install Portainer Agent for WSLC.
# Run from an elevated PowerShell prompt.
#
# Service mode (default) runs in session 0 as the wslc user. If that cannot see
# the interactive wslc session, re-run with -Mode LogonTask. The task starts at
# logon in the user session, so the agent is down until somebody logs in.
#
# The default listen address is 127.0.0.1 and no inbound firewall rule is
# created. -ExposeLAN binds 0.0.0.0 and opens TCP 9001. Pass -RemoteAddress to
# limit that rule to the Portainer server.
param(
    [Parameter(Mandatory = $true)][string]$Secret,
    [string]$User = $env:USERNAME,
    [string]$Password,
    [string]$Exe = (Join-Path $PSScriptRoot "..\dist\agent-wslc.exe"),
    [ValidateSet("Service", "LogonTask")][string]$Mode = "Service",
    [switch]$ExposeLAN,
    [string]$RemoteAddress
)

$ErrorActionPreference = "Stop"
$ServiceName = "PortainerAgentWslc"
$Dest = Join-Path $env:ProgramData $ServiceName
$Exe = (Resolve-Path $Exe).Path
$FirewallName = "Portainer Agent WSLC"

if (-not (Test-Path $Exe)) {
    throw "agent binary not found: $Exe"
}

function Get-ProfilePath([string]$account) {
    $nt = New-Object System.Security.Principal.NTAccount($account)
    $sid = $nt.Translate([System.Security.Principal.SecurityIdentifier]).Value
    $profile = Get-CimInstance Win32_UserProfile | Where-Object { $_.SID -eq $sid }
    if (-not $profile) {
        throw "no profile for $account"
    }
    return $profile.LocalPath
}

function Resolve-AccountName([string]$account) {
    if ($account -like ".\*") {
        return "$env:COMPUTERNAME\$($account.Substring(2))"
    }
    return $account
}

function Set-PrivateAcl([string]$Path, [string]$Account) {
    $isDir = (Get-Item -LiteralPath $Path).PSIsContainer
    if ($isDir) {
        $acl = New-Object System.Security.AccessControl.DirectorySecurity
        $inherit = [System.Security.AccessControl.InheritanceFlags]"ContainerInherit,ObjectInherit"
        $prop = [System.Security.AccessControl.PropagationFlags]::None
    } else {
        $acl = New-Object System.Security.AccessControl.FileSecurity
    }
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($id in @("NT AUTHORITY\SYSTEM", "BUILTIN\Administrators", $Account)) {
        if ($isDir) {
            $rule = New-Object System.Security.AccessControl.FileSystemAccessRule($id, "FullControl", $inherit, $prop, "Allow")
        } else {
            $rule = New-Object System.Security.AccessControl.FileSystemAccessRule($id, "FullControl", "Allow")
        }
        $acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}

$account = $User
if ($account -notmatch '\\') {
    $account = ".\$account"
}
$resolved = Resolve-AccountName $account
$profile = Get-ProfilePath $account
$wslcHome = Join-Path $profile "AppData\Local\wslc"
if (-not (Test-Path $wslcHome)) {
    throw "wslc settings were not found at $wslcHome. The service account must be the user that owns the containers."
}

$listenHost = "127.0.0.1"
if ($ExposeLAN) {
    $listenHost = "0.0.0.0"
} elseif ($RemoteAddress) {
    Write-Warning "-RemoteAddress is used only with -ExposeLAN. The agent will keep listening on 127.0.0.1."
}

$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($existing -and $existing.Status -ne "Stopped") {
    Stop-Service -Name $ServiceName -Force
    $existing.WaitForStatus([System.ServiceProcess.ServiceControllerStatus]::Stopped, (New-TimeSpan -Seconds 30))
}
Stop-ScheduledTask -TaskName $ServiceName -ErrorAction SilentlyContinue
Unregister-ScheduledTask -TaskName $ServiceName -Confirm:$false -ErrorAction SilentlyContinue

New-Item -ItemType Directory -Force -Path $Dest | Out-Null
Set-PrivateAcl $Dest $resolved
$destExe = Join-Path $Dest "agent-wslc.exe"
Copy-Item -Force $Exe $destExe
Set-PrivateAcl $destExe $resolved

$rules = @(Get-NetFirewallRule -DisplayName $FirewallName -ErrorAction SilentlyContinue)
if ($rules) {
    $rules | Remove-NetFirewallRule
}
if ($ExposeLAN) {
    $firewall = @{
        DisplayName = $FirewallName
        Direction   = "Inbound"
        Action      = "Allow"
        Protocol    = "TCP"
        LocalPort   = 9001
    }
    if ($RemoteAddress) {
        $firewall.RemoteAddress = $RemoteAddress
    }
    New-NetFirewallRule @firewall | Out-Null
    if (-not $RemoteAddress) {
        Write-Warning "ExposeLAN did not set -RemoteAddress, so any remote host can open TCP 9001. With TLS verification skipped, a captured agent signature can be replayed against every implemented API."
    }
}

$envLines = @(
    "WSLC=1",
    "AGENT_SECRET=$Secret",
    "AGENT_PORT=9001",
    "AGENT_HOST=$listenHost",
    "LOG_LEVEL=INFO"
)
$envFile = Join-Path $Dest "agent.env"
$launcher = Join-Path $Dest "run-agent.ps1"
$oldLauncher = Join-Path $Dest "run-agent.cmd"
if (Test-Path $oldLauncher) { Remove-Item -Force $oldLauncher }

if ($Mode -eq "LogonTask") {
    if ($existing) {
        sc.exe delete $ServiceName | Out-Host
        if ($LASTEXITCODE -ne 0) { throw "sc.exe delete failed" }
        Start-Sleep -Seconds 2
    }
    $utf8 = New-Object System.Text.UTF8Encoding $false
    [System.IO.File]::WriteAllLines($envFile, $envLines, $utf8)
    $runner = @'
$ErrorActionPreference = "Stop"
Set-Location -LiteralPath $PSScriptRoot
$envFile = Join-Path $PSScriptRoot "agent.env"
Get-Content -LiteralPath $envFile | ForEach-Object {
    $line = $_.Trim()
    if ($line -eq "" -or $line.StartsWith("#")) { return }
    $eq = $line.IndexOf("=")
    if ($eq -lt 1) { return }
    Set-Item -Path ("Env:" + $line.Substring(0, $eq)) -Value $line.Substring($eq + 1)
}
& (Join-Path $PSScriptRoot "agent-wslc.exe")
exit $LASTEXITCODE
'@
    [System.IO.File]::WriteAllText($launcher, $runner, $utf8)
    Set-PrivateAcl $envFile $resolved
    Set-PrivateAcl $launcher $resolved
    $action = New-ScheduledTaskAction -Execute "powershell.exe" -Argument "-NoProfile -ExecutionPolicy Bypass -File `"$launcher`""
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $resolved
    $principal = New-ScheduledTaskPrincipal -UserId $resolved -LogonType Interactive -RunLevel Limited
    Register-ScheduledTask -TaskName $ServiceName -Action $action -Trigger $trigger -Principal $principal -Force | Out-Null
    Write-Host "Logon task $ServiceName created. It starts after $resolved logs on, listening on ${listenHost}:9001."
    return
}

if (Test-Path $envFile) { Remove-Item -Force $envFile }
if (Test-Path $launcher) { Remove-Item -Force $launcher }

if (-not $Password) {
    $secure = Read-Host "Password for $account" -AsSecureString
    $Password = [Runtime.InteropServices.Marshal]::PtrToStringUni(
        [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure))
}

if (-not ([type]::GetType("ServiceLogonRight"))) {
    Add-Type -TypeDefinition @"
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Security.Principal;
public static class ServiceLogonRight {
    const uint POLICY_CREATE_ACCOUNT = 0x00000010;
    const uint POLICY_LOOKUP_NAMES = 0x00000800;
    [StructLayout(LayoutKind.Sequential)] struct LSA_UNICODE_STRING {
        public ushort Length; public ushort MaximumLength; public IntPtr Buffer;
    }
    [StructLayout(LayoutKind.Sequential)] struct LSA_OBJECT_ATTRIBUTES {
        public int Length; public IntPtr RootDirectory; public IntPtr ObjectName;
        public uint Attributes; public IntPtr SecurityDescriptor; public IntPtr SecurityQualityOfService;
    }
    [DllImport("advapi32.dll")] static extern uint LsaOpenPolicy(IntPtr system, ref LSA_OBJECT_ATTRIBUTES oa, uint access, out IntPtr policy);
    [DllImport("advapi32.dll")] static extern uint LsaAddAccountRights(IntPtr policy, IntPtr sid, LSA_UNICODE_STRING[] rights, uint count);
    [DllImport("advapi32.dll")] static extern uint LsaClose(IntPtr policy);
    [DllImport("advapi32.dll")] static extern uint LsaNtStatusToWinError(uint status);
    public static void Grant(string sidText) {
        var oa = new LSA_OBJECT_ATTRIBUTES();
        oa.Length = Marshal.SizeOf(oa);
        IntPtr policy;
        uint status = LsaOpenPolicy(IntPtr.Zero, ref oa, POLICY_CREATE_ACCOUNT | POLICY_LOOKUP_NAMES, out policy);
        if (status != 0) throw new Win32Exception((int)LsaNtStatusToWinError(status));
        try {
            var sid = new SecurityIdentifier(sidText);
            byte[] sidBuf = new byte[sid.BinaryLength];
            sid.GetBinaryForm(sidBuf, 0);
            IntPtr pSid = Marshal.AllocHGlobal(sidBuf.Length);
            try {
                Marshal.Copy(sidBuf, 0, pSid, sidBuf.Length);
                string right = "SeServiceLogonRight";
                var us = new LSA_UNICODE_STRING();
                us.Length = (ushort)(right.Length * 2);
                us.MaximumLength = (ushort)((right.Length + 1) * 2);
                us.Buffer = Marshal.StringToHGlobalUni(right);
                try {
                    status = LsaAddAccountRights(policy, pSid, new[] { us }, 1);
                    if (status != 0) throw new Win32Exception((int)LsaNtStatusToWinError(status));
                } finally { Marshal.FreeHGlobal(us.Buffer); }
            } finally { Marshal.FreeHGlobal(pSid); }
        } finally { LsaClose(policy); }
    }
}
"@
}

$nt = New-Object System.Security.Principal.NTAccount($account)
[ServiceLogonRight]::Grant($nt.Translate([System.Security.Principal.SecurityIdentifier]).Value)

if ($existing) {
    sc.exe delete $ServiceName | Out-Host
    if ($LASTEXITCODE -ne 0) { throw "sc.exe delete failed" }
    Start-Sleep -Seconds 2
}

$binary = "`"$destExe`""
sc.exe create $ServiceName binPath= $binary start= auto obj= $account password= $Password DisplayName= "Portainer Agent for WSLC" | Out-Host
if ($LASTEXITCODE -ne 0) { throw "sc.exe create failed" }
sc.exe description $ServiceName "Portainer Agent backed by wslc" | Out-Null

$reg = "HKLM:\SYSTEM\CurrentControlSet\Services\$ServiceName"
New-ItemProperty -Path $reg -Name Environment -PropertyType MultiString -Value $envLines -Force | Out-Null
Start-Service -Name $ServiceName
Write-Host "Service $ServiceName started as $account, listening on ${listenHost}:9001."
Write-Host "Check https://127.0.0.1:9001/ping (204) before adding the environment in Portainer."
Write-Host "If the service cannot see this user's containers, agent.log records the wslc ps probe. Re-run with -Mode LogonTask."
