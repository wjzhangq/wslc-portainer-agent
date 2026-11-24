# Install Portainer Agent for WSLC.
# Run from an elevated PowerShell prompt.
#
# Service mode (default) runs in session 0 as the wslc user. If that cannot see
# the interactive wslc session, re-run with -Mode LogonTask. The task starts at
# logon in the user session, so the agent is down until somebody logs in.
param(
    [Parameter(Mandatory = $true)][string]$Secret,
    [string]$User = $env:USERNAME,
    [string]$Password,
    [string]$Exe = (Join-Path $PSScriptRoot "..\dist\agent-wslc.exe"),
    [ValidateSet("Service", "LogonTask")][string]$Mode = "Service"
)

$ErrorActionPreference = "Stop"
$ServiceName = "PortainerAgentWslc"
$Dest = Join-Path $env:ProgramData $ServiceName
$Exe = (Resolve-Path $Exe).Path

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

$account = $User
if ($account -notmatch '\\') {
    $account = ".\$account"
}
$profile = Get-ProfilePath $account
$wslcHome = Join-Path $profile "AppData\Local\wslc"
if (-not (Test-Path $wslcHome)) {
    throw "wslc settings were not found at $wslcHome. The service account must be the user that owns the containers."
}

New-Item -ItemType Directory -Force -Path $Dest | Out-Null
Copy-Item -Force $Exe (Join-Path $Dest "agent-wslc.exe")

$firewall = Get-NetFirewallRule -DisplayName "Portainer Agent WSLC" -ErrorAction SilentlyContinue
if (-not $firewall) {
    New-NetFirewallRule -DisplayName "Portainer Agent WSLC" -Direction Inbound -Action Allow -Protocol TCP -LocalPort 9001 | Out-Null
}

$envLines = @(
    "WSLC=1",
    "AGENT_SECRET=$Secret",
    "AGENT_PORT=9001",
    "AGENT_HOST=0.0.0.0",
    "LOG_LEVEL=INFO"
)

if ($Mode -eq "LogonTask") {
    $launcher = Join-Path $Dest "run-agent.cmd"
    @(
        "@echo off",
        "set WSLC=1",
        "set AGENT_SECRET=$Secret",
        "set AGENT_PORT=9001",
        "set AGENT_HOST=0.0.0.0",
        "set LOG_LEVEL=INFO",
        "cd /d `"$Dest`"",
        "`"$Dest\agent-wslc.exe`""
    ) | Set-Content -Path $launcher -Encoding ascii
    schtasks /Create /TN $ServiceName /SC ONLOGON /RL LIMITED /F /TR "`"$launcher`"" | Out-Host
    Write-Host "Logon task $ServiceName created. It starts after $account logs on."
    return
}

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

$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($existing) {
    if ($existing.Status -ne "Stopped") {
        Stop-Service -Name $ServiceName -Force
    }
    sc.exe delete $ServiceName | Out-Host
    Start-Sleep -Seconds 2
}

$binary = "`"$Dest\agent-wslc.exe`""
sc.exe create $ServiceName binPath= $binary start= auto obj= $account password= $Password DisplayName= "Portainer Agent for WSLC" | Out-Host
if ($LASTEXITCODE -ne 0) { throw "sc.exe create failed" }
sc.exe description $ServiceName "Portainer Agent backed by wslc" | Out-Null

$reg = "HKLM:\SYSTEM\CurrentControlSet\Services\$ServiceName"
New-ItemProperty -Path $reg -Name Environment -PropertyType MultiString -Value $envLines -Force | Out-Null
Start-Service -Name $ServiceName
Write-Host "Service $ServiceName started as $account."
Write-Host "Check https://127.0.0.1:9001/ping (204) before adding the environment in Portainer."
