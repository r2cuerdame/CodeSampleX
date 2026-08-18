# Local credential helpers for the private production dashboard.
#
# The password is generated from the OS CSPRNG and persisted only as a
# current-user DPAPI ciphertext. Callers may derive its one-way SHA-256
# verifier without ever materializing the password as a managed String.

function Assert-CSXWindowsDPAPI {
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
        throw "admin credential storage requires Windows current-user DPAPI"
    }
}

function Get-CSXAdminCredentialPaths {
    Assert-CSXWindowsDPAPI
    $localData = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
    if ([string]::IsNullOrWhiteSpace($localData)) {
        throw "could not resolve LocalApplicationData for the admin credential"
    }
    $directory = Join-Path $localData "CodeSampleX"
    [pscustomobject]@{
        Directory = $directory
        Active    = Join-Path $directory "production-admin-password.dpapi"
        Pending   = Join-Path $directory "production-admin-password.pending.dpapi"
    }
}

function New-CSXAdminCredential {
    $random = New-Object byte[] 32
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    $secret = New-Object Security.SecureString
    $alphabet = "0123456789abcdef".ToCharArray()
    try {
        $rng.GetBytes($random)
        foreach ($octet in $random) {
            $secret.AppendChar($alphabet[$octet -shr 4])
            $secret.AppendChar($alphabet[$octet -band 15])
        }
        $secret.MakeReadOnly()
        return $secret
    } catch {
        $secret.Dispose()
        throw
    } finally {
        [Array]::Clear($random, 0, $random.Length)
        [Array]::Clear($alphabet, 0, $alphabet.Length)
        $rng.Dispose()
    }
}

function Get-CSXSecureStringSHA256([Security.SecureString]$Value) {
    # Copy directly from SecureString's BSTR into clearable buffers. This
    # avoids creating a managed plaintext String merely to derive the hash.
    $bstr = [IntPtr]::Zero
    $chars = $null
    $bytes = $null
    $digest = $null
    try {
        $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($Value)
        [int]$length = [Runtime.InteropServices.Marshal]::ReadInt32($bstr, -4) / 2
        $chars = New-Object char[] $length
        [Runtime.InteropServices.Marshal]::Copy($bstr, $chars, 0, $length)
        $bytes = [Text.Encoding]::UTF8.GetBytes($chars)
        $sha = [Security.Cryptography.SHA256]::Create()
        try {
            $digest = $sha.ComputeHash($bytes)
            return -join ($digest | ForEach-Object { $_.ToString("x2") })
        } finally {
            $sha.Dispose()
        }
    } finally {
        if ($null -ne $digest) { [Array]::Clear($digest, 0, $digest.Length) }
        if ($null -ne $bytes) { [Array]::Clear($bytes, 0, $bytes.Length) }
        if ($null -ne $chars) { [Array]::Clear($chars, 0, $chars.Length) }
        if ($bstr -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr) }
    }
}

function Save-CSXAdminCredential([Security.SecureString]$Secret, [string]$Path) {
    Assert-CSXWindowsDPAPI
    $directory = Split-Path -Parent $Path
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
    $temporary = Join-Path $directory (".{0}.tmp" -f [Guid]::NewGuid().ToString("N"))
    try {
        # With no key supplied, ConvertFrom-SecureString uses current-user
        # Windows DPAPI. The file therefore contains ciphertext, not password.
        $ciphertext = ConvertFrom-SecureString -SecureString $Secret
        [IO.File]::WriteAllText($temporary, $ciphertext + [Environment]::NewLine, (New-Object Text.UTF8Encoding($false)))
        Move-Item -LiteralPath $temporary -Destination $Path -Force
    } finally {
        $ciphertext = $null
        if (Test-Path -LiteralPath $temporary -PathType Leaf) {
            Remove-Item -LiteralPath $temporary -Force
        }
    }
}

function Read-CSXAdminCredential([string]$Path) {
    Assert-CSXWindowsDPAPI
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "admin credential does not exist in the current user's DPAPI store"
    }
    $ciphertext = [IO.File]::ReadAllText($Path).Trim()
    try {
        if ([string]::IsNullOrWhiteSpace($ciphertext)) { throw "admin credential file is empty" }
        return ConvertTo-SecureString -String $ciphertext
    } finally {
        $ciphertext = $null
    }
}

function Commit-CSXAdminCredential([string]$PendingPath, [string]$ActivePath) {
    if (-not (Test-Path -LiteralPath $PendingPath -PathType Leaf)) {
        throw "pending admin credential is missing"
    }
    if (Test-Path -LiteralPath $ActivePath -PathType Leaf) {
        # Both files live in the same LocalAppData directory, so Replace is an
        # atomic same-volume rotation and never leaves a plaintext fallback.
        # Windows PowerShell's .NET Framework rejects a null backup path, so
        # use a unique DPAPI-ciphertext backup and remove it immediately.
        $backup = $ActivePath + ".backup." + [Guid]::NewGuid().ToString("N")
        try {
            [IO.File]::Replace($PendingPath, $ActivePath, $backup)
        } finally {
            [IO.File]::Delete($backup)
        }
    } else {
        [IO.File]::Move($PendingPath, $ActivePath)
    }
}

function Invoke-CSXAdminAuthenticatedProbe([Security.SecureString]$Secret) {
    # This target is deliberately fixed so a caller cannot accidentally send
    # the production credential to an arbitrary URL. Redirects are disabled.
    $endpoint = New-Object Uri "https://codesamplex.dev/admin"
    $bstr = [IntPtr]::Zero
    $credentialChars = $null
    $credentialBytes = $null
    $basic = $null
    $handler = $null
    $client = $null
    $request = $null
    $response = $null
    try {
        $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($Secret)
        [int]$secretLength = [Runtime.InteropServices.Marshal]::ReadInt32($bstr, -4) / 2
        $prefix = "recuerdame:".ToCharArray()
        $credentialChars = New-Object char[] ($prefix.Length + $secretLength)
        [Array]::Copy($prefix, 0, $credentialChars, 0, $prefix.Length)
        [Runtime.InteropServices.Marshal]::Copy($bstr, $credentialChars, $prefix.Length, $secretLength)
        $credentialBytes = [Text.Encoding]::UTF8.GetBytes($credentialChars)
        $basic = [Convert]::ToBase64String($credentialBytes)

        Add-Type -AssemblyName System.Net.Http
        $handler = New-Object Net.Http.HttpClientHandler
        $handler.AllowAutoRedirect = $false
        $client = New-Object Net.Http.HttpClient($handler)
        $client.Timeout = [TimeSpan]::FromSeconds(15)
        $request = New-Object Net.Http.HttpRequestMessage([Net.Http.HttpMethod]::Get, $endpoint)
        $request.Headers.Authorization = New-Object Net.Http.Headers.AuthenticationHeaderValue("Basic", $basic)
        try {
            $response = $client.SendAsync($request).GetAwaiter().GetResult()
            return [int]$response.StatusCode
        } catch {
            # Do not propagate HttpClient diagnostics: a future runtime could
            # include request metadata. Callers only need a neutral failure.
            throw "admin authenticated request failed"
        }
    } finally {
        if ($null -ne $response) { $response.Dispose() }
        if ($null -ne $request) { $request.Dispose() }
        if ($null -ne $client) { $client.Dispose() }
        if ($null -ne $handler) { $handler.Dispose() }
        $basic = $null
        if ($null -ne $credentialBytes) { [Array]::Clear($credentialBytes, 0, $credentialBytes.Length) }
        if ($null -ne $credentialChars) { [Array]::Clear($credentialChars, 0, $credentialChars.Length) }
        if ($null -ne $prefix) { [Array]::Clear($prefix, 0, $prefix.Length) }
        if ($bstr -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr) }
    }
}

function Copy-CSXSecureStringToClipboard([Security.SecureString]$Secret) {
    $bstr = [IntPtr]::Zero
    $plaintext = $null
    try {
        $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($Secret)
        $plaintext = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
        Set-Clipboard -Value $plaintext
    } finally {
        $plaintext = $null
        if ($bstr -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr) }
    }
}
