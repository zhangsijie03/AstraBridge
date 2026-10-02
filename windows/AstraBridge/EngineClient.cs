using System.Diagnostics;
using System.Text;
using System.Text.Json;

namespace AstraBridge;

internal sealed class EngineClient : IAsyncDisposable
{
    private readonly SemaphoreSlim inputGate = new(1, 1);
    private Process? process;
    private Task? monitor;
    private Task? disposal;
    private volatile bool closing;
    private volatile bool failed;
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

    public async Task SendAsync(EngineAction action, string? model = null)
    {
        await inputGate.WaitAsync();
        try
        {
            if (!Available || process is null) throw new IOException("后台服务未运行，请重新打开应用");
            await process.StandardInput.WriteLineAsync(Protocol.Command(action, model));
        }
        finally { inputGate.Release(); }
    }

    private async Task MonitorAsync(Process child)
    {
        // 持续排空 stderr，防止管道满后阻塞；不把可能含敏感信息的原始输出写入日志。
        Task errors = DrainErrorsAsync(child);
        string? lastStateError = null;
        try
        {
            while (await child.StandardOutput.ReadLineAsync() is { } line)
            {
                if (line.Length > 1_048_576) throw new JsonException("后台状态过长");
                EngineEvent value = Protocol.Read(line);
                if (value.Type == EventKind.State)
                    lastStateError = value.Phase == EnginePhase.Error ? value.Message : null;
                Received?.Invoke(value);
            }
            await child.WaitForExitAsync();
            await errors;
            // 启动失败通常先发具体修复提示再退出，不能用笼统的退出提示覆盖它。
            if (!closing) ReportFault(lastStateError ?? $"后台进程已退出（代码 {child.ExitCode}），请重新打开应用。");
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
        // 更新与窗口关闭可能同时请求退出；所有调用者都必须等到同一次清理完成。
        var pending = disposal ??= DisposeCoreAsync();
        try { await pending; }
        catch
        {
            if (ReferenceEquals(disposal, pending)) disposal = null;
            throw;
        }
    }

    private async Task DisposeCoreAsync()
    {
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
                try { child.Kill(entireProcessTree: true); }
                catch (InvalidOperationException) when (child.HasExited) { /* 超时与自然退出竞争时，退出结果已经满足要求。 */ }
                await child.WaitForExitAsync().WaitAsync(TimeSpan.FromSeconds(3));
            }
            if (monitor is not null) await monitor;
        }
        catch
        {
            // 清理失败保留进程句柄，允许用户再次关闭时重试终止，避免失去子进程控制权。
            closing = false;
            throw;
        }
        child.Dispose();
        process = null;
    }
}
