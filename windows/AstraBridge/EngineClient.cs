using System.Diagnostics;
using System.Text;
using System.Text.Json;

namespace AstraBridge;

internal sealed class EngineClient : IAsyncDisposable
{
    private readonly SemaphoreSlim inputGate = new(1, 1);
    private Process? process;
    private Task? monitor;
    private bool closing;
    private bool failed;
    public event Action<EngineEvent>? Received;
    public event Action<string>? Faulted;
    public bool Available => !closing && !failed && process is { HasExited: false };

    public void Start(string? isolatedDirectory = null)
    {
        if (process is not null) throw new InvalidOperationException("后台进程已经启动");
        var start = new ProcessStartInfo(Path.Combine(AppContext.BaseDirectory, "engine", Product.EngineName))
        {
            UseShellExecute = false, CreateNoWindow = true,
            RedirectStandardInput = true, RedirectStandardOutput = true, RedirectStandardError = true,
            StandardInputEncoding = new UTF8Encoding(false), StandardOutputEncoding = Encoding.UTF8,
            StandardErrorEncoding = Encoding.UTF8, WorkingDirectory = AppContext.BaseDirectory
        };
        // 默认完整继承 CODEX_HOME、HTTPS_PROXY 等环境；不覆盖用户的代理选择。
        if (isolatedDirectory is not null)
        {
            start.Environment["CODEX_HOME"] = Path.Combine(isolatedDirectory, "codex");
            start.ArgumentList.Add("--state-dir");
            start.ArgumentList.Add(Path.Combine(isolatedDirectory, "state"));
        }
        process = Process.Start(start) ?? throw new IOException("无法创建后台进程");
        process.StandardInput.AutoFlush = true;
        monitor = MonitorAsync(process);
    }

    public async Task SendAsync(EngineAction action)
    {
        await inputGate.WaitAsync();
        try
        {
            if (!Available || process is null) throw new IOException("后台服务未运行，请重新打开应用");
            await process.StandardInput.WriteLineAsync(Protocol.Command(action));
        }
        finally { inputGate.Release(); }
    }

    private async Task MonitorAsync(Process child)
    {
        // 持续排空 stderr，防止管道满后阻塞；不把可能含敏感信息的原始输出写入日志。
        Task errors = DrainErrorsAsync(child);
        try
        {
            while (await child.StandardOutput.ReadLineAsync() is { } line)
            {
                if (line.Length > 1_048_576) throw new JsonException("后台状态过长");
                Received?.Invoke(Protocol.Read(line));
            }
            await child.WaitForExitAsync();
            await errors;
            if (!closing) ReportFault($"后台进程已退出（代码 {child.ExitCode}），请重新打开应用。");
        }
        catch (Exception error) when (error is IOException or InvalidOperationException or JsonException)
        {
            if (!closing) ReportFault("无法读取后台状态，请重新打开应用。");
        }
    }

    private async Task DrainErrorsAsync(Process child)
    {
        try
        {
            char[] buffer = new char[2048];
            while (await child.StandardError.ReadAsync(buffer) > 0) { /* 仅排空；用户错误由 JSON 契约承载。 */ }
        }
        catch (IOException)
        {
            if (!closing) ReportFault("后台诊断管道已中断，请重新打开应用。");
        }
    }

    private void ReportFault(string message)
    {
        failed = true;
        Faulted?.Invoke(message);
    }

    public async ValueTask DisposeAsync()
    {
        if (closing) return;
        closing = true;
        if (process is not { } child) return;
        try
        {
            await inputGate.WaitAsync();
            try
            {
                // EOF 让引擎取消正在进行的探测/上传；不等待排在探测之后的 quit 命令。
                child.StandardInput.Close();
            }
            catch (IOException) { Faulted?.Invoke("关闭后台输入时连接已断开，正在确认进程退出。"); }
            finally { inputGate.Release(); }
            try { await child.WaitForExitAsync().WaitAsync(TimeSpan.FromSeconds(5)); }
            catch (TimeoutException)
            {
                child.Kill(entireProcessTree: true);
                await child.WaitForExitAsync().WaitAsync(TimeSpan.FromSeconds(3));
            }
            if (monitor is not null) await monitor;
        }
        finally { child.Dispose(); process = null; }
    }
}
