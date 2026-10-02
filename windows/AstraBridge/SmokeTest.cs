using System.Text.Json;

namespace AstraBridge;

internal static class SmokeTest
{
    public static void VerifyEmbeddedLog(MainForm form)
    {
        static IEnumerable<Control> Descendants(Control root) => root.Controls.Cast<Control>()
            .SelectMany(child => new[] { child }.Concat(Descendants(child)));
        TransferLog panel = Descendants(form).OfType<TransferLog>().Single();
        Control[] controls = Descendants(panel).ToArray();
        DataGridView log = controls.OfType<DataGridView>().Single();
        string Cell(int row, int column) => log.Rows[row].Cells[column].Value?.ToString() ?? "";
        CheckBox follow = controls.OfType<CheckBox>().Single();
        Button copy = controls.OfType<Button>().Single(button => button.Text == "复制日志");
        Button clear = controls.OfType<Button>().Single(button => button.Text == "清空显示");
        if (panel.FindForm() != form || !panel.Visible || log.ClientSize.Height < 100 || copy.Enabled || log.Rows.Count != 0 ||
            log.Columns.Count != 5 || log.ScrollBars != ScrollBars.Vertical)
            throw new InvalidOperationException("内嵌日志布局或空状态不正确");
        if (Descendants(form).OfType<Button>().Any(button => button.Text == "转发日志"))
            throw new InvalidOperationException("仍保留旧日志窗口入口");
        static TransferTrace Fixture(int index, TransferStage stage = TransferStage.Completed, int? semantic = null) => new(
            $"R{index:D6}", "2026-09-30T12:00:00Z", stage, "离线日志测试", 35000, 5000, 4096, 6, 0, 1, 200, semantic);
        // 合成事件只覆盖显示链路，不读取账号，也不会启动引擎或发起远程请求。
        panel.Append(Fixture(1, TransferStage.Streaming));
        if (Cell(0, 0) != "…") throw new InvalidOperationException("进行中图标错误");
        panel.Append(Fixture(1, TransferStage.Streaming)); panel.Append(Fixture(1));
        if (log.Rows.Count != 1 || Cell(0, 0) != "✓") throw new InvalidOperationException("请求没有合并为一行或成功图标错误");
        panel.Append(Fixture(2, TransferStage.Failed, 429));
        if (Cell(1, 0) != "✕" || !Cell(1, 4).Contains("429") ||
            !log.Rows[1].Cells[4].ToolTipText.Contains("错误分类状态 429") || !log.Rows[1].Cells[4].ToolTipText.Contains("上游 HTTP 200"))
            throw new InvalidOperationException("流内错误被 HTTP 200 掩盖或详情丢失");
        panel.Append(Fixture(3, TransferStage.Cancelled));
        if (Cell(2, 0) != "—") throw new InvalidOperationException("取消状态不应标记成功或失败");
        clear.PerformClick();
        follow.Checked = false;
        for (int index = 1; index <= 1001; index++) panel.Append(Fixture(index));
        if (log.Rows.Count != 1000 || Cell(0, 2) != "R000002" || Cell(999, 2) != "R001001")
            throw new InvalidOperationException("日志历史上限或淘汰顺序错误");
        log.CurrentCell = log.Rows[498].Cells[0]; log.Rows[498].Selected = true;
        log.FirstDisplayedScrollingRowIndex = 12;
        string top = Cell(log.FirstDisplayedScrollingRowIndex, 2);
        panel.Append(Fixture(1002));
        if (log.CurrentRow?.Tag as string != "R000500" || Cell(log.FirstDisplayedScrollingRowIndex, 2) != top)
            throw new InvalidOperationException("新日志破坏了历史选中请求或阅读位置");
        follow.Checked = true;
        if (log.FirstDisplayedScrollingRowIndex < 900) throw new InvalidOperationException("自动滚动没有恢复");
        clear.PerformClick();
        if (copy.Enabled || clear.Enabled || log.Rows.Count != 0)
            throw new InvalidOperationException("清空日志未恢复空状态");
        panel.Append(Fixture(2000, TransferStage.Streaming));
        if (!controls.OfType<Label>().Any(label => label.Text.StartsWith("1 个请求转发中", StringComparison.Ordinal)))
            throw new InvalidOperationException("进行中的请求未显示在主界面");
        panel.Stopped();
        if (log.Rows.Count != 1 || Cell(0, 2) != "R002000" || Cell(0, 0) != "—" ||
            !controls.OfType<Label>().Any(label => label.Text.StartsWith("中转未运行", StringComparison.Ordinal)))
            throw new InvalidOperationException("停止后日志丢失或活跃状态未清理");
        clear.PerformClick();
    }

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
        EngineEvent trace = Protocol.Read("""{"type":"trace","requests":0,"trace":{"request_id":"R000001","time":"2026-09-29T12:00:00Z","stage":"streaming","message":"正在接收","elapsed_ms":35000,"quiet_ms":32000,"upstream_bytes":1024,"client_events":0,"tool_calls":0,"attempt":1,"http_status":200}}""");
        if (trace.Trace is not { Stage: TransferStage.Streaming, QuietMs: 32000 } || !trace.Trace.Line.Contains("上游暂无新数据"))
            throw new InvalidOperationException("转发日志事件解析失败");
        EngineEvent limited = Protocol.Read("""{"type":"trace","requests":1,"trace":{"request_id":"R000002","time":"2026-09-29T12:00:00Z","stage":"failed","message":"上游限流","elapsed_ms":10,"quiet_ms":0,"upstream_bytes":100,"client_events":1,"tool_calls":0,"attempt":1,"http_status":200,"semantic_status":429,"limit_hints":[{"name":"retry-after","value":"30s"}]}}""");
        if (limited.Trace is not { Terminal: true, SemanticStatus: 429, HttpStatus: 200 } || !limited.Trace.Line.Contains("retry-after=30s"))
            throw new InvalidOperationException("限流提示或流内错误状态丢失");
        foreach (string malformed in new[] { "{}", "null", "{\"type\":\"trace\",\"requests\":0}", "{\"type\":\"state\",\"phase\":\"unknown\"}",
            "{\"type\":\"request\",\"requests\":-1}", "{\"type\":\"state\",\"phase\":\"idle\",\"base_url\":\"https://example.com\"}" })
        {
            try { Protocol.Read(malformed); }
            catch (JsonException) { continue; }
            throw new InvalidOperationException("无效后台事件被错误接受");
        }
    }

    public static async Task VerifyStartupErrorAsync()
    {
        string directory = Path.Combine(Path.GetTempPath(), "AstraBridge-invalid-" + Guid.NewGuid().ToString("N"));
        string stateDirectory = Path.Combine(directory, "state");
        Directory.CreateDirectory(stateDirectory);
        string settings = Path.Combine(stateDirectory, "relay.json");
        await File.WriteAllTextAsync(settings, "invalid-synthetic-settings");
        string? stateError = null;
        var exited = new TaskCompletionSource<string>(TaskCreationOptions.RunContinuationsAsynchronously);
        await using var engine = new EngineClient();
        engine.Received += value => { if (value.Phase == EnginePhase.Error) stateError = value.Message; };
        engine.Faulted += message => exited.TrySetResult(message);
        try
        {
            engine.Start(directory);
            string finalError = await exited.Task.WaitAsync(TimeSpan.FromSeconds(15));
            if (string.IsNullOrWhiteSpace(stateError) || finalError != stateError || !finalError.Contains("relay.json", StringComparison.Ordinal))
                throw new InvalidOperationException("引擎退出覆盖了具体的配置修复提示");
            if (engine.Available || await File.ReadAllTextAsync(settings) != "invalid-synthetic-settings")
                throw new InvalidOperationException("配置失败后引擎仍可操作，或损坏配置被意外改写");
        }
        finally
        {
            await engine.DisposeAsync();
            Directory.Delete(directory, recursive: true);
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
            Task shutdown = engine.DisposeAsync().AsTask();
            Task concurrentShutdown = engine.DisposeAsync().AsTask();
            if (!shutdown.IsCompleted && concurrentShutdown.IsCompleted)
                throw new InvalidOperationException("并发关闭在后台进程结束之前返回");
            await Task.WhenAll(shutdown, concurrentShutdown);
            if (engine.Available) throw new InvalidOperationException("关闭输入后引擎仍在运行");
        }
        finally
        {
            await engine.DisposeAsync();
            Directory.Delete(directory, recursive: true);
        }
    }
}
