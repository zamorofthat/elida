# ELIDA Configuration via Group Policy (GPO)

This guide explains how to deploy ELIDA proxy configuration via Active Directory Group Policy.

## Environment Variables via GPO

### Steps

1. **Open Group Policy Management Console**
   - Run: `gpmc.msc`

2. **Create or Edit a GPO**
   - Right-click your target OU → "Create a GPO in this domain, and Link it here..."
   - Name it: "ELIDA AI Proxy Configuration"

3. **Navigate to Environment Variables**
   - Computer Configuration → Preferences → Windows Settings → Environment

4. **Add Environment Variables**

   For each variable, right-click → New → Environment Variable:

   | Name | Value | Action |
   |------|-------|--------|
   | `ANTHROPIC_BASE_URL` | `http://elida.corp.local:8080` | Create |
   | `OPENAI_BASE_URL` | `http://elida.corp.local:8080/v1` | Create |
   | `OPENAI_API_BASE` | `http://elida.corp.local:8080/v1` | Create |
   | `MISTRAL_API_BASE` | `http://elida.corp.local:8080` | Create |

   Settings for each:
   - **Action**: Create (or Update if already exists)
   - **User variable / System variable**: System variable
   - **Variable name**: (as above)
   - **Variable value**: (as above)

5. **Apply and Test**
   - Run `gpupdate /force` on a test machine
   - Open new command prompt and verify: `echo %OPENAI_BASE_URL%`

## Registry Keys (Alternative Method)

Environment variables are stored in the registry. You can also deploy via registry preferences:

### Path
```
HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment
```

### Values
| Value Name | Type | Data |
|------------|------|------|
| `ANTHROPIC_BASE_URL` | REG_SZ | `http://elida.corp.local:8080` |
| `OPENAI_BASE_URL` | REG_SZ | `http://elida.corp.local:8080/v1` |
| `OPENAI_API_BASE` | REG_SZ | `http://elida.corp.local:8080/v1` |
| `MISTRAL_API_BASE` | REG_SZ | `http://elida.corp.local:8080` |

### GPO Registry Preference Steps

1. Navigate to: Computer Configuration → Preferences → Windows Settings → Registry
2. Right-click → New → Registry Item
3. Configure:
   - **Action**: Create
   - **Hive**: HKEY_LOCAL_MACHINE
   - **Key Path**: `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
   - **Value name**: (e.g., `ANTHROPIC_BASE_URL`)
   - **Value type**: REG_SZ
   - **Value data**: `http://elida.corp.local:8080`

## Deploying Config Files via GPO

To deploy the Claude Code and Continue.dev configuration files:

### Option 1: Logon Script

1. Create a network share: `\\fileserver\scripts$\elida\`
2. Copy `Deploy-ElidaConfig.ps1` to the share
3. In GPO: User Configuration → Policies → Windows Settings → Scripts → Logon
4. Add PowerShell script: `\\fileserver\scripts$\elida\Deploy-ElidaConfig.ps1`

### Option 2: Scheduled Task

1. Computer Configuration → Preferences → Control Panel Settings → Scheduled Tasks
2. New → Scheduled Task (At least Windows 7)
3. Configure:
   - **Action**: Create
   - **Name**: "Deploy ELIDA Configuration"
   - **Trigger**: At log on
   - **Action**: Start a program
     - Program: `powershell.exe`
     - Arguments: `-ExecutionPolicy Bypass -File \\fileserver\scripts$\elida\Deploy-ElidaConfig.ps1`

### Option 3: Files Preference

Deploy configuration files directly:

1. Computer Configuration → Preferences → Windows Settings → Files
2. New → File
3. Configure for Claude Code:
   - **Action**: Create
   - **Source file**: `\\fileserver\config$\elida\claude-settings.json`
   - **Destination file**: `%USERPROFILE%\.claude\settings.json`

Repeat for Continue.dev config.

## Targeting

Use GPO Item-Level Targeting to deploy only to specific machines:

1. On any preference item, click "Common" tab
2. Check "Item-level targeting"
3. Click "Targeting..."
4. Add conditions, e.g.:
   - Security Group: "AI-Developers"
   - Computer Name: matches "DEV-*"
   - Operating System: Windows 10/11

## Verification

On a target machine:

```powershell
# Check environment variables
[Environment]::GetEnvironmentVariable("ANTHROPIC_BASE_URL", "Machine")
[Environment]::GetEnvironmentVariable("OPENAI_BASE_URL", "Machine")

# Test connectivity to ELIDA
Invoke-WebRequest -Uri "http://elida.corp.local:8080/health"

# Check GPO application
gpresult /r
```

## Troubleshooting

### Variables not applying
- Run `gpupdate /force`
- Check `gpresult /h gpreport.html` for errors
- Verify GPO is linked to correct OU
- Check security filtering allows target computers/users

### Applications not using proxy
- Some apps cache environment variables - restart them
- Sign out and back in to refresh user session
- Restart the machine for system-wide changes

### Firewall blocking ELIDA
- Add firewall rule via GPO:
  - Computer Configuration → Policies → Windows Settings → Security Settings → Windows Firewall
  - Outbound Rule: Allow TCP to `elida.corp.local` port 8080
