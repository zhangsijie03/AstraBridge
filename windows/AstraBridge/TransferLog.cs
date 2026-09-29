using System.Runtime.InteropServices;
using System.Text.Json.Serialization;

namespace AstraBridge;

internal enum TransferStage { Prepare, Upload, Connect, Headers, Streaming, Repair, Completed, Failed, Cancelled }
internal sealed record TransferTrace(
    [property: JsonPropertyName("request_id")] string RequestId,
    string Time, TransferStage Stage, string Message,
    [property: JsonPropertyName("elapsed_ms")] long ElapsedMs,
    [property: JsonPropertyName("quiet_ms")] long QuietMs,
    [property: JsonPropertyName("upstream_bytes")] long UpstreamBytes,
    [property: JsonPropertyName("client_events")] long ClientEvents,
    [property: JsonPropertyName("tool_calls")] long ToolCalls,
    int Attempt,
    [property: JsonPropertyName("http_status")] int? HttpStatus)
{
    public bool Terminal => Stage is TransferStage.Completed or TransferStage.Failed or TransferStage.Cancelled;
    public string Summary => $"{RequestId} · {Message} · {ElapsedMs / 1000}s";
    public string Line => $"[{Time}] {RequestId}  {Message}\r\n  总耗时 {ElapsedMs / 1000}s · 距本轮上游数据 {QuietMs / 1000}s · 收到 {UpstreamBytes} B · 客户端事件 {ClientEvents} · 工具 {ToolCalls} · 请求次数 {Attempt}" +
        (HttpStatus is { } status ? $" · HTTP {status}" : "") +
        (!Terminal && Attempt > 0 && QuietMs >= 30000 ? " · 上游暂无新数据（不代表已断开）" : "") + "\r\n";
}

internal sealed class TransferLog : Form
{
    private const int Capacity = 1000;
    private readonly Queue<string> lines = new();
    private readonly Dictionary<string, string> active = new();
    private readonly TextBox log = new() { Multiline = true, ReadOnly = true, ScrollBars = ScrollBars.Both, WordWrap = false, Dock = DockStyle.Fill };
    private readonly Label overview = InterfaceStyle.Label("暂无转发请求", 10);
    private readonly CheckBox follow = new() { Text = "自动滚动", Checked = true, AutoSize = true };
    public string Summary => active.Count == 0 ? "暂无进行中的转发" : $"{active.Count} 个请求转发中";
    public TransferLog()
    {
        Text = "星桥 · 转发日志"; ClientSize = new Size(940, 560); MinimumSize = new Size(760, 420);
        AutoScaleMode = AutoScaleMode.Dpi; StartPosition = FormStartPosition.CenterParent;
        var root = new TableLayoutPanel { Dock = DockStyle.Fill, ColumnCount = 1, RowCount = 4, Padding = new Padding(16) };
        root.ColumnStyles.Add(new ColumnStyle(SizeType.Percent, 100));
        root.RowStyles.Add(new RowStyle(SizeType.AutoSize)); root.RowStyles.Add(new RowStyle(SizeType.AutoSize));
        root.RowStyles.Add(new RowStyle(SizeType.Percent, 100)); root.RowStyles.Add(new RowStyle(SizeType.AutoSize));
        var hint = InterfaceStyle.Label("每 5 秒更新 · 最近 1000 条 · 不含密钥、正文或工具代码。收到数据不等于模型已有答案，客户端工具执行不在星桥内。", 9, secondary: true);
        hint.MaximumSize = new Size(860, 0); root.Controls.Add(hint, 0, 0);
        overview.MaximumSize = new Size(860, 180); root.Controls.Add(overview, 0, 1);
        log.Font = new Font("Consolas", 10); root.Controls.Add(log, 0, 2);
        var buttons = new FlowLayoutPanel { AutoSize = true, Dock = DockStyle.Fill };
        var copy = InterfaceStyle.Button("复制日志"); copy.Click += (_, _) => CopyLog();
        var save = InterfaceStyle.Button("导出…"); save.Click += (_, _) => SaveLog();
        var clear = InterfaceStyle.Button("清空显示"); clear.Click += (_, _) => { lines.Clear(); RefreshLog(); };
        buttons.Controls.AddRange(new Control[] { follow, copy, save, clear }); root.Controls.Add(buttons, 0, 3); Controls.Add(root);
        // 关闭窗口只隐藏，本次应用会话内保留有界历史；主窗口退出时统一释放。
        FormClosing += (_, e) => { if (e.CloseReason == CloseReason.UserClosing) { e.Cancel = true; Hide(); } };
    }
    public void Append(TransferTrace value)
    {
        if (value.Terminal) active.Remove(value.RequestId); else active[value.RequestId] = value.Summary;
        lines.Enqueue(value.Line); while (lines.Count > Capacity) lines.Dequeue();
        overview.Text = active.Count == 0 ? "暂无进行中的转发 · 最近：" + value.Summary : string.Join("\r\n", active.Values);
        RefreshLog();
    }
    public void Stopped() { active.Clear(); overview.Text = "中转未运行 · 历史日志保留在本次窗口会话"; }
    private void RefreshLog()
    {
        if (!Visible && lines.Count != 0) return;
        int selection = log.SelectionStart;
        log.Text = string.Join("\r\n", lines);
        log.SelectionStart = follow.Checked ? log.TextLength : Math.Min(selection, log.TextLength);
        if (follow.Checked) log.ScrollToCaret();
    }
    protected override void OnVisibleChanged(EventArgs e) { base.OnVisibleChanged(e); if (Visible) RefreshLog(); }
    private void CopyLog()
    {
        if (lines.Count == 0) return;
        try { Clipboard.SetText(string.Join("\r\n", lines)); }
        catch (ExternalException) { MessageBox.Show(this, "剪贴板暂时不可用，请重试。", "复制失败"); }
    }
    private void SaveLog()
    {
        using var picker = new SaveFileDialog { FileName = "AstraBridge-transfer.log", Filter = "日志文件|*.log", OverwritePrompt = true };
        if (picker.ShowDialog(this) != DialogResult.OK) return;
        try { File.WriteAllText(picker.FileName, string.Join("\r\n", lines)); }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException) { MessageBox.Show(this, "日志无法写入所选位置，请选择其他目录。", "导出失败"); }
    }
}
