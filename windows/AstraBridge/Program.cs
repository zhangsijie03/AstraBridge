using System.Text.Json;

namespace AstraBridge;

internal static class Program
{
    [STAThread]
    private static void Main(string[] args)
    {
        ApplicationConfiguration.Initialize();
        bool smoke = args.Contains("--smoke-test", StringComparer.Ordinal);
        using var form = new MainForm(smoke || args.Contains("--preview", StringComparer.Ordinal));
        if (smoke)
        {
            int reportIndex = Array.IndexOf(args, "--smoke-report");
            string report = reportIndex >= 0 && reportIndex + 1 < args.Length ? args[reportIndex + 1]
                : Path.Combine(Path.GetTempPath(), "AstraBridge-smoke.json");
            form.Shown += async (_, _) =>
            {
                var checks = new List<string>();
                string? failure = null;
                try
                {
                    form.VerifyPreview(); checks.Add("preview-ui-manual-start");
                    using (var preview = new Bitmap(form.Width, form.Height))
                    {
                        form.DrawToBitmap(preview, new Rectangle(Point.Empty, preview.Size));
                        preview.Save(Path.Combine(Path.GetDirectoryName(Path.GetFullPath(report))!, "windows-smoke-preview.png"));
                    }
                    checks.Add("preview-screenshot");
                    SmokeTest.VerifyEmbeddedLog(form); checks.Add("embedded-log-history-and-controls");
                    SmokeTest.VerifyProtocol(); checks.Add("typed-json-contract");
                    await SmokeTest.VerifyEngineAsync(); checks.Add("isolated-engine-idle-and-eof-exit");
                    await SmokeTest.VerifyStartupErrorAsync(); checks.Add("startup-error-preserved-after-exit");
                }
                catch (Exception error) { failure = error.GetType().Name + ": " + error.Message; }
                try
                {
                    await File.WriteAllTextAsync(report, JsonSerializer.Serialize(new
                    {
                        success = failure is null, version = Product.Version, checks, error = failure,
                        platform = Environment.OSVersion.ToString(), dpi = form.DeviceDpi
                    }, new JsonSerializerOptions { WriteIndented = true }));
                    Environment.ExitCode = failure is null ? 0 : 1;
                }
                catch (Exception error) when (error is IOException or UnauthorizedAccessException)
                { Environment.ExitCode = 2; }
                form.Close();
            };
        }
        Application.Run(form);
    }
}
