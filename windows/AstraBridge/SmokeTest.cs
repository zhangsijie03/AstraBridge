using System.Text.Json;

namespace AstraBridge;

internal static class SmokeTest
{
    public static void VerifyProtocol()
    {
        if (Protocol.Command(EngineAction.Start) != "{\"action\":\"start\"}" ||
            Protocol.Command(EngineAction.Stop) != "{\"action\":\"stop\"}" ||
            Protocol.Command(EngineAction.Probe) != "{\"action\":\"probe\"}" ||
            Protocol.Command(EngineAction.Quit) != "{\"action\":\"quit\"}")
            throw new InvalidOperationException("命令 JSON 与引擎契约不匹配");
        EngineEvent request = Protocol.Read("""{"type":"request","requests":7,"result":{"success":true,"model":"gpt-6-astra","effort":"high","account":"de•••@example.com","warnings":[]}}""");
        if (request.Type != EventKind.Request || request.Requests != 7 || request.Result?.Success != true)
            throw new InvalidOperationException("请求事件解析失败");
        foreach (string malformed in new[] { "{}", "null", "{\"type\":\"state\",\"phase\":\"unknown\"}",
            "{\"type\":\"request\",\"requests\":-1}", "{\"type\":\"state\",\"phase\":\"idle\",\"base_url\":\"https://example.com\"}" })
        {
            try { Protocol.Read(malformed); }
            catch (JsonException) { continue; }
            throw new InvalidOperationException("无效后台事件被错误接受");
        }
    }

    public static async Task VerifyEngineAsync()
    {
        string directory = Path.Combine(Path.GetTempPath(), "AstraBridge-smoke-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        var idle = new TaskCompletionSource<EngineEvent>(TaskCreationOptions.RunContinuationsAsynchronously);
        await using var engine = new EngineClient();
        engine.Received += value =>
        {
            if (value.Phase == EnginePhase.Idle) idle.TrySetResult(value);
            if (value.Phase == EnginePhase.Error) idle.TrySetException(new InvalidOperationException(value.Message));
        };
        engine.Faulted += message => idle.TrySetException(new InvalidOperationException(message));
        try
        {
            // 使用空 CODEX_HOME，绝不读取开发者凭据，也不发送 start/probe 或任何上游请求。
            engine.Start(directory);
            EngineEvent ready = await idle.Task.WaitAsync(TimeSpan.FromSeconds(15));
            if (ready.Model != Product.Model || ready.ApiKey is not { Length: > 0 } || ready.BaseUrl is null || ready.Requests != 0)
                throw new InvalidOperationException("引擎初始连接契约不完整");
            await engine.DisposeAsync();
            if (engine.Available) throw new InvalidOperationException("关闭输入后引擎仍在运行");
        }
        finally
        {
            await engine.DisposeAsync();
            Directory.Delete(directory, recursive: true);
        }
    }
}
