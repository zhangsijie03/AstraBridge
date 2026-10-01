using System.IO.Compression;
using System.Diagnostics;
using System.Net;
using System.Net.Http.Headers;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;

namespace AstraBridge;

internal sealed record AppUpdate(string Version, string Notes, Uri ArchiveUri, Uri ChecksumUri);
internal sealed record StagedAppUpdate(string Version, string PackageDirectory, string StagingDirectory);

internal sealed class UpdateChecker : IDisposable
{
    private const string Repository = "zhangsijie03/AstraBridge";
    private const string ChecksumAsset = "SHA256SUMS.txt";
    private readonly HttpClient client;

    public UpdateChecker()
    {
        var handler = new HttpClientHandler();
        var proxyValue = Environment.GetEnvironmentVariable("HTTPS_PROXY") ?? Environment.GetEnvironmentVariable("HTTP_PROXY");
        if (Uri.TryCreate(proxyValue, UriKind.Absolute, out var proxyUri))
        {
            handler.Proxy = new WebProxy(proxyUri);
            handler.UseProxy = true;
        }
        client = new HttpClient(handler) { Timeout = TimeSpan.FromSeconds(20) };
        client.DefaultRequestHeaders.Accept.Add(new MediaTypeWithQualityHeaderValue("application/vnd.github+json"));
        client.DefaultRequestHeaders.UserAgent.ParseAdd($"AstraBridge/{Product.Version}");
    }

    public async Task<AppUpdate?> CheckAsync(string currentVersion, CancellationToken cancellationToken = default)
    {
        using var response = await client.GetAsync($"https://api.github.com/repos/{Repository}/releases/latest", cancellationToken);
        response.EnsureSuccessStatusCode();
        await using var stream = await response.Content.ReadAsStreamAsync(cancellationToken);
        using var document = await JsonDocument.ParseAsync(stream, cancellationToken: cancellationToken);
        var root = document.RootElement;
        var tag = root.TryGetProperty("tag_name", out var tagProperty) ? tagProperty.GetString() : null;
        if (!TryParseVersion(tag, out var latest)) throw new InvalidDataException("GitHub 发行版本号无效。");
        if (!TryParseVersion(currentVersion, out var current)) throw new InvalidDataException("当前应用版本号无效。");
        if (latest <= current) return null;
        var archiveName = $"AstraBridge-{latest}-Windows-x64.zip";
        Uri? archiveUri = null;
        Uri? checksumUri = null;
        if (root.TryGetProperty("assets", out var assets))
        {
            foreach (var asset in assets.EnumerateArray())
            {
                var name = asset.TryGetProperty("name", out var nameProperty) ? nameProperty.GetString() : null;
                var rawUrl = asset.TryGetProperty("browser_download_url", out var urlProperty) ? urlProperty.GetString() : null;
                if (!TryGetTrustedAssetUrl(rawUrl, out var url)) continue;
                if (name == archiveName) archiveUri = url;
                if (name == ChecksumAsset) checksumUri = url;
            }
        }
        if (archiveUri is null || checksumUri is null) throw new FileNotFoundException($"发行包缺少 {archiveName} 或 {ChecksumAsset}。");
        var notes = root.TryGetProperty("body", out var bodyProperty) ? bodyProperty.GetString() ?? "" : "";
        return new AppUpdate(latest.ToString(3), notes, archiveUri, checksumUri);
    }

    public async Task<StagedAppUpdate> StageAsync(AppUpdate update, CancellationToken cancellationToken = default)
    {
        var staging = Path.Combine(Path.GetTempPath(), $"AstraBridge-update-{Guid.NewGuid():N}");
        Directory.CreateDirectory(staging);
        try
        {
            var archive = Path.Combine(staging, "update.zip");
            var checksum = Path.Combine(staging, ChecksumAsset);
            await DownloadToAsync(update.ArchiveUri, archive, cancellationToken);
            await DownloadToAsync(update.ChecksumUri, checksum, cancellationToken);
            VerifyChecksum(archive, checksum, $"AstraBridge-{update.Version}-Windows-x64.zip");
            var extracted = Path.Combine(staging, "extracted");
            ZipFile.ExtractToDirectory(archive, extracted, overwriteFiles: true);
            var executable = Directory.EnumerateFiles(extracted, "AstraBridge.exe", SearchOption.AllDirectories).FirstOrDefault();
            if (executable is null) throw new InvalidDataException("更新包中未找到 AstraBridge.exe。");
            var packageDirectory = Path.GetDirectoryName(executable) ?? throw new InvalidDataException("更新包目录无效。");
            return new StagedAppUpdate(update.Version, packageDirectory, staging);
        }
        catch
        {
            TryDelete(staging);
            throw;
        }
    }

    public void EnsureInstallLocationWritable()
    {
        var probe = Path.Combine(AppContext.BaseDirectory, $".update-probe-{Guid.NewGuid():N}");
        try
        {
            File.WriteAllText(probe, "AstraBridge update probe", Encoding.UTF8);
            File.Delete(probe);
        }
        catch (Exception error) when (error is UnauthorizedAccessException or IOException)
        {
            throw new UnauthorizedAccessException("当前应用目录不可写，请将 AstraBridge 放入用户可写的目录后重试。", error);
        }
    }

    public void LaunchUpdater(StagedAppUpdate staged)
    {
        EnsureInstallLocationWritable();
        var script = Path.Combine(staged.StagingDirectory, "apply-update.ps1");
        File.WriteAllText(script, """
param([string]$target, [string]$source, [int]$processId, [string]$cleanup)
$ErrorActionPreference = 'Stop'
for ($attempt = 0; $attempt -lt 240; $attempt++) {
    if (-not (Get-Process -Id $processId -ErrorAction SilentlyContinue)) { break }
    Start-Sleep -Milliseconds 250
}
Start-Sleep -Milliseconds 500
Get-ChildItem -LiteralPath $source -Force | Copy-Item -Destination $target -Recurse -Force
Start-Process -FilePath (Join-Path $target 'AstraBridge.exe')
Remove-Item -LiteralPath $cleanup -Recurse -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $PSCommandPath -Force -ErrorAction SilentlyContinue
""", Encoding.UTF8);
        var start = new ProcessStartInfo("powershell.exe")
        {
            UseShellExecute = false,
            CreateNoWindow = true,
            WindowStyle = ProcessWindowStyle.Hidden
        };
        start.ArgumentList.Add("-NoProfile");
        start.ArgumentList.Add("-ExecutionPolicy");
        start.ArgumentList.Add("Bypass");
        start.ArgumentList.Add("-File");
        start.ArgumentList.Add(script);
        start.ArgumentList.Add(AppContext.BaseDirectory);
        start.ArgumentList.Add(staged.PackageDirectory);
        start.ArgumentList.Add(Environment.ProcessId.ToString());
        start.ArgumentList.Add(staged.StagingDirectory);
        if (Process.Start(start) is null) throw new InvalidOperationException("无法启动 PowerShell 更新程序。");
    }

    public void Dispose() => client.Dispose();

    private async Task DownloadToAsync(Uri uri, string destination, CancellationToken cancellationToken)
    {
        using var response = await client.GetAsync(uri, HttpCompletionOption.ResponseHeadersRead, cancellationToken);
        response.EnsureSuccessStatusCode();
        await using var source = await response.Content.ReadAsStreamAsync(cancellationToken);
        await using var target = File.Create(destination);
        await source.CopyToAsync(target, cancellationToken);
    }

    private static void VerifyChecksum(string archive, string checksumFile, string expectedName)
    {
        var expected = File.ReadLines(checksumFile, Encoding.UTF8)
            .Select(line => line.Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries))
            .Where(parts => parts.Length >= 2 && parts[^1].TrimStart('*') == expectedName)
            .Select(parts => parts[0].Trim().ToLowerInvariant())
            .FirstOrDefault();
        if (expected is null) throw new InvalidDataException($"校验文件缺少 {expectedName}。");
        using var stream = File.OpenRead(archive);
        var actual = Convert.ToHexString(SHA256.HashData(stream)).ToLowerInvariant();
        if (!CryptographicOperations.FixedTimeEquals(Encoding.ASCII.GetBytes(expected), Encoding.ASCII.GetBytes(actual)))
            throw new InvalidDataException("更新包 SHA-256 校验失败。");
    }

    private static bool TryParseVersion(string? raw, out Version version)
    {
        version = new Version();
        var value = raw?.Trim().TrimStart('v');
        if (value is null) return false;
        var parts = value.Split('.');
        if (parts.Length != 3 || parts.Any(part => !int.TryParse(part, out var number) || number < 0)) return false;
        if (!Version.TryParse(value, out var parsed) || parsed is null) return false;
        version = parsed;
        return true;
    }

    private static bool TryGetTrustedAssetUrl(string? raw, out Uri url)
    {
        if (Uri.TryCreate(raw, UriKind.Absolute, out url!) && url.Scheme == Uri.UriSchemeHttps &&
            url.Host.Equals("github.com", StringComparison.OrdinalIgnoreCase) &&
            url.AbsolutePath.Contains($"/{Repository}/releases/download/", StringComparison.Ordinal)) return true;
        url = null!;
        return false;
    }

    private static void TryDelete(string path)
    {
        try { if (Directory.Exists(path)) Directory.Delete(path, recursive: true); }
        catch (IOException) { }
        catch (UnauthorizedAccessException) { }
    }
}
